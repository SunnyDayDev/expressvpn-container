package reconciler

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"detour/agent/internal/config"
	"detour/agent/internal/state"
	"detour/agent/internal/xvpn"
)

type fakeXVPN struct {
	connects, disconnects, settings atomic.Int64
	lastLocation, lastProtocol      string
	failConnect                     atomic.Bool
}

func (f *fakeXVPN) Connect(_ context.Context, location, protocol string) error {
	f.connects.Add(1)
	f.lastLocation, f.lastProtocol = location, protocol
	if f.failConnect.Load() {
		return &xvpn.CLIError{Code: "connect_failed", Message: "boom"}
	}
	return nil
}
func (f *fakeXVPN) Disconnect(context.Context) error { f.disconnects.Add(1); return nil }
func (f *fakeXVPN) ApplySettings(_ context.Context, _ config.ExpressVPN) error {
	f.settings.Add(1)
	return nil
}

type fakeUplink struct{ applies atomic.Int64 }

func (f *fakeUplink) Apply(_ context.Context, cfg config.Uplink) (state.Uplink, error) {
	f.applies.Add(1)
	return state.Uplink{Mode: cfg.Mode, Status: state.UplinkUp, UDPSupported: state.TriUnknown}, nil
}

type fakeKS struct{ applies atomic.Int64 }

func (f *fakeKS) Apply(_ context.Context, _ config.Uplink) error { f.applies.Add(1); return nil }

func newTestReconciler(t *testing.T) (*Reconciler, *fakeXVPN, *fakeUplink, *fakeKS, *state.Store, *config.Store) {
	t.Helper()
	cfg, err := config.NewStore(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	st := state.NewStore(state.Initial("test"))
	x, u, k := &fakeXVPN{}, &fakeUplink{}, &fakeKS{}
	r := New(st, cfg, Drivers{XVPN: x, Uplink: u, Killswitch: k}, slog.New(slog.DiscardHandler))
	return r, x, u, k, st, cfg
}

func TestReconcileIsIdempotent(t *testing.T) {
	r, x, u, k, _, _ := newTestReconciler(t)
	ctx := context.Background()
	r.reconcile(ctx)
	r.reconcile(ctx)
	r.reconcile(ctx)
	if got := u.applies.Load(); got != 1 {
		t.Fatalf("uplink applies=%d want 1", got)
	}
	if got := k.applies.Load(); got != 1 {
		t.Fatalf("killswitch applies=%d want 1", got)
	}
	if got := x.settings.Load(); got != 1 {
		t.Fatalf("settings applies=%d want 1", got)
	}
	if got := x.connects.Load(); got != 0 {
		t.Fatalf("connects=%d want 0 (desired=disconnected)", got)
	}
}

func TestDesiredConnectedTriggersConnect(t *testing.T) {
	r, x, _, _, st, _ := newTestReconciler(t)
	ctx := context.Background()
	r.reconcile(ctx)

	st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredConnected })
	r.reconcile(ctx)
	if got := x.connects.Load(); got != 1 {
		t.Fatalf("connects=%d want 1", got)
	}
	if got := st.Get().ExpressVPN.Connection; got != state.ConnConnected {
		t.Fatalf("connection=%v", got)
	}
	// Повторный проход без изменений не переподключает.
	r.reconcile(ctx)
	if got := x.connects.Load(); got != 1 {
		t.Fatalf("connects=%d want 1 (idempotent)", got)
	}
}

func TestLocationChangeReconnects(t *testing.T) {
	r, x, _, _, st, cfg := newTestReconciler(t)
	ctx := context.Background()
	st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredConnected })
	r.reconcile(ctx)
	if x.connects.Load() != 1 {
		t.Fatal("setup connect missing")
	}

	if _, err := cfg.ApplyMergePatch([]byte(`{"expressvpn":{"location":"de-frankfurt-1"}}`)); err != nil {
		t.Fatal(err)
	}
	r.reconcile(ctx)
	if got := x.connects.Load(); got != 2 {
		t.Fatalf("connects=%d want 2 (reconnect on location change)", got)
	}
	if x.lastLocation != "de-frankfurt-1" {
		t.Fatalf("lastLocation=%q", x.lastLocation)
	}
}

func TestDesiredDisconnectedDisconnects(t *testing.T) {
	r, x, _, _, st, _ := newTestReconciler(t)
	ctx := context.Background()
	st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredConnected })
	r.reconcile(ctx)

	st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredDisconnected })
	r.reconcile(ctx)
	if got := x.disconnects.Load(); got != 1 {
		t.Fatalf("disconnects=%d want 1", got)
	}
	if got := st.Get().ExpressVPN.Connection; got != state.ConnDisconnected {
		t.Fatalf("connection=%v", got)
	}
	r.reconcile(ctx)
	if got := x.disconnects.Load(); got != 1 {
		t.Fatalf("disconnects=%d want 1 (idempotent)", got)
	}
}

func TestConfigChangeWakesLoop(t *testing.T) {
	r, _, u, _, _, cfg := newTestReconciler(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()

	waitFor(t, func() bool { return u.applies.Load() == 1 })
	if _, err := cfg.ApplyMergePatch([]byte(`{"uplink":{"mode":"socks5","socks5":{"host":"h","port":1080}}}`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return u.applies.Load() == 2 })
	cancel()
	<-done
}

// forceRetryDue сбрасывает таймер очередной попытки, не трогая счётчик неудач.
func forceRetryDue(r *Reconciler) {
	r.retryMu.Lock()
	r.nextRetryAt = time.Time{}
	r.retryMu.Unlock()
}

func TestReconnectBackoffAndExhausted(t *testing.T) {
	r, x, _, _, st, _ := newTestReconciler(t)
	ctx := context.Background()
	x.failConnect.Store(true)
	st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredConnected })

	// Первая неудача.
	r.reconcile(ctx)
	if got := x.connects.Load(); got != 1 {
		t.Fatalf("connects=%d want 1", got)
	}
	if st.Get().LastError.Code != "connect_failed" {
		t.Fatalf("lastError=%+v", st.Get().LastError)
	}
	// Backoff ещё не истёк — повторный проход не пытается снова.
	r.reconcile(ctx)
	if got := x.connects.Load(); got != 1 {
		t.Fatalf("connects=%d want 1 (backoff gate)", got)
	}

	// Доводим до исчерпания: 10 подряд неудач.
	for i := int64(1); i < 10; i++ {
		forceRetryDue(r)
		r.reconcile(ctx)
	}
	if got := x.connects.Load(); got != 10 {
		t.Fatalf("connects=%d want 10", got)
	}
	if got := st.Get().LastError.Code; got != "reconnect_exhausted" {
		t.Fatalf("lastError=%q want reconnect_exhausted", got)
	}
	// Исчерпано: даже когда время пришло, попыток больше нет.
	forceRetryDue(r)
	r.reconcile(ctx)
	if got := x.connects.Load(); got != 10 {
		t.Fatalf("connects=%d want 10 (exhausted)", got)
	}

	// Восстановление uplink'а / действие пользователя: ResetRetries → попытка,
	// и после успеха состояние connected.
	x.failConnect.Store(false)
	r.ResetRetries()
	r.reconcile(ctx)
	if got := st.Get().ExpressVPN.Connection; got != state.ConnConnected {
		t.Fatalf("connection=%v", got)
	}
}

func TestEffectiveProtocolRecordedInState(t *testing.T) {
	r, x, _, _, st, cfg := newTestReconciler(t)
	ctx := context.Background()
	if _, err := cfg.ApplyMergePatch([]byte(`{"uplink":{"mode":"socks5","socks5":{"host":"h","port":1}}}`)); err != nil {
		t.Fatal(err)
	}
	st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredConnected })
	r.reconcile(ctx)

	// Uplink up, но udpSupported=unknown (фейк) → консервативный lightway_tcp.
	p := st.Get().ExpressVPN.Protocol
	if p.Requested != "auto" || p.Effective != "lightway_tcp" || p.Reason == "" {
		t.Fatalf("protocol=%+v", p)
	}
	if x.lastProtocol != "lightway_tcp" {
		t.Fatalf("driver got protocol=%q", x.lastProtocol)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

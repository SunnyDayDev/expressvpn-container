package reconciler

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
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
	// hold, если задан, держит Connect до закрытия; entered сообщает о входе.
	hold    chan struct{}
	entered chan struct{}
}

func (f *fakeXVPN) Connect(_ context.Context, location, protocol string) error {
	f.connects.Add(1)
	f.lastLocation, f.lastProtocol = location, protocol
	if f.hold != nil {
		f.entered <- struct{}{}
		<-f.hold
	}
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

type fakeUplink struct {
	applies, waits atomic.Int64
	// udp/udpAfter — результат UDP-пробы и когда он будет готов; udpNever —
	// проба не заканчивается (WaitUDP ждёт до отмены ctx).
	udp      state.TriState
	udpAfter time.Duration
	udpNever bool
	// earlyProbe — проба успела опубликовать результат до возврата Apply.
	earlyProbe func()
}

func (f *fakeUplink) Apply(_ context.Context, cfg config.Uplink) (state.Uplink, error) {
	f.applies.Add(1)
	if f.earlyProbe != nil {
		f.earlyProbe()
	}
	return state.Uplink{Mode: cfg.Mode, Status: state.UplinkUp, UDPSupported: state.TriUnknown}, nil
}

func (f *fakeUplink) WaitUDP(ctx context.Context) state.TriState {
	f.waits.Add(1)
	if f.udpNever {
		<-ctx.Done()
		return state.TriUnknown
	}
	select {
	case <-time.After(f.udpAfter):
	case <-ctx.Done():
		return state.TriUnknown
	}
	if f.udp == "" {
		return state.TriUnknown
	}
	return f.udp
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

// recHandler копит записи журнала для проверок.
type recHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *recHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" " + a.Key + "=" + a.Value.String())
		return true
	})
	h.mu.Lock()
	h.msgs = append(h.msgs, b.String())
	h.mu.Unlock()
	return nil
}
func (h *recHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recHandler) WithGroup(string) slog.Handler      { return h }

func (h *recHandler) has(sub string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

func recordLogs(r *Reconciler) *recHandler {
	h := &recHandler{}
	r.logger = slog.New(h)
	return h
}

func mustPatch(t *testing.T, cfg *config.Store, patch string) {
	t.Helper()
	if _, err := cfg.ApplyMergePatch([]byte(patch)); err != nil {
		t.Fatal(err)
	}
}

// connectedVia — подключённый VPN через socks5-uplink с закреплённым протоколом.
func connectedVia(t *testing.T, r *Reconciler, st *state.Store, cfg *config.Store) {
	t.Helper()
	mustPatch(t, cfg, `{"expressvpn":{"protocol":"lightway_tcp"},"uplink":{"mode":"socks5","socks5":{"host":"192.0.2.1","port":1080}}}`)
	st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredConnected })
	r.reconcile(context.Background())
	if st.Get().ExpressVPN.Connection != state.ConnConnected {
		t.Fatalf("setup: connection=%s", st.Get().ExpressVPN.Connection)
	}
}

// Сценарий отчёта: смена uplink'а при подключённом VPN — принудительное
// переподключение, а state не говорит connected, пока драйвер не подключил.
func TestUplinkChangeWhileConnectedForcesReconnect(t *testing.T) {
	r, x, _, _, st, cfg := newTestReconciler(t)
	connectedVia(t, r, st, cfg)
	h := recordLogs(r)
	at := time.Now().Add(-time.Hour).UTC()
	st.Update(func(s *state.State) { s.ExpressVPN.ConnectedAt = &at })

	x.hold, x.entered = make(chan struct{}), make(chan struct{}, 1)
	mustPatch(t, cfg, `{"uplink":{"socks5":{"host":"192.0.2.2"}}}`)
	done := make(chan struct{})
	go func() { r.reconcile(context.Background()); close(done) }()

	<-x.entered
	s := st.Get()
	if s.ExpressVPN.Connection != state.ConnReconnecting {
		t.Fatalf("while driver connects: connection=%s, want reconnecting", s.ExpressVPN.Connection)
	}
	if s.ExpressVPN.ConnectedAt == nil || !s.ExpressVPN.ConnectedAt.Equal(at) {
		t.Fatalf("connectedAt changed before the driver connected: %v", s.ExpressVPN.ConnectedAt)
	}
	close(x.hold)
	<-done

	if got := x.connects.Load(); got != 2 {
		t.Fatalf("connects=%d want 2", got)
	}
	if st.Get().ExpressVPN.Connection != state.ConnConnected {
		t.Fatalf("connection=%s", st.Get().ExpressVPN.Connection)
	}
	if !h.has("reconnecting expressvpn reason=uplink_changed from=reconnecting") || !h.has("expressvpn connected") {
		t.Fatalf("logs: %q", h.msgs)
	}
}

func TestReconnectActionForcesReconnect(t *testing.T) {
	r, x, _, _, st, cfg := newTestReconciler(t)
	connectedVia(t, r, st, cfg)
	h := recordLogs(r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)

	a := NewActions(r, nil, nil, Probers{})
	a.waitTimeout = 5 * time.Second
	if err := a.Reconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if got := x.connects.Load(); got != 2 {
		t.Fatalf("connects=%d want 2", got)
	}
	if !h.has("reason=user_request") {
		t.Fatalf("logs: %q", h.msgs)
	}
}

func TestProtocolChangeReconnects(t *testing.T) {
	r, x, _, _, st, cfg := newTestReconciler(t)
	st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredConnected })
	r.reconcile(context.Background())
	h := recordLogs(r)

	mustPatch(t, cfg, `{"expressvpn":{"protocol":"lightway_udp"}}`)
	r.reconcile(context.Background())
	if got := x.connects.Load(); got != 2 {
		t.Fatalf("connects=%d want 2", got)
	}
	if x.lastProtocol != "lightway_udp" {
		t.Fatalf("protocol=%s", x.lastProtocol)
	}
	if !h.has("reason=protocol_changed") {
		t.Fatalf("logs: %q", h.msgs)
	}
}

func TestReconnectReasonsLogged(t *testing.T) {
	ctx := context.Background()

	t.Run("connect", func(t *testing.T) {
		r, _, _, _, st, _ := newTestReconciler(t)
		h := recordLogs(r)
		st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredConnected })
		r.reconcile(ctx)
		if !h.has("connecting expressvpn reason=connect from=disconnected location=smart") {
			t.Fatalf("logs: %q", h.msgs)
		}
	})
	t.Run("location_changed", func(t *testing.T) {
		r, _, _, _, st, cfg := newTestReconciler(t)
		st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredConnected })
		r.reconcile(ctx)
		h := recordLogs(r)
		mustPatch(t, cfg, `{"expressvpn":{"location":"de-frankfurt-1"}}`)
		r.reconcile(ctx)
		if !h.has("reason=location_changed") || !h.has("expressvpn connected location=de-frankfurt-1") {
			t.Fatalf("logs: %q", h.msgs)
		}
	})
	t.Run("connection_lost", func(t *testing.T) {
		r, _, _, _, st, _ := newTestReconciler(t)
		st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredConnected })
		r.reconcile(ctx)
		h := recordLogs(r)
		// Supervise обнаружил разрыв.
		st.Update(func(s *state.State) { s.ExpressVPN.Connection = state.ConnReconnecting })
		r.reconcile(ctx)
		if !h.has("reconnecting expressvpn reason=connection_lost") {
			t.Fatalf("logs: %q", h.msgs)
		}
	})
	t.Run("retry", func(t *testing.T) {
		r, x, _, _, st, _ := newTestReconciler(t)
		x.failConnect.Store(true)
		st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredConnected })
		r.reconcile(ctx)
		h := recordLogs(r)
		x.failConnect.Store(false)
		forceRetryDue(r)
		r.reconcile(ctx)
		if !h.has("reconnecting expressvpn reason=retry from=error") {
			t.Fatalf("logs: %q", h.msgs)
		}
	})
}

func TestWaitsForUDPProbeBeforeChoosingProtocol(t *testing.T) {
	r, x, u, _, st, cfg := newTestReconciler(t)
	u.udp, u.udpAfter = state.TriTrue, 100*time.Millisecond
	mustPatch(t, cfg, `{"uplink":{"mode":"socks5","socks5":{"host":"192.0.2.1","port":1080}}}`)
	st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredConnected })
	r.reconcile(context.Background())

	p := st.Get().ExpressVPN.Protocol
	if x.lastProtocol != "auto" || p.Effective != "auto" || p.Reason != "" {
		t.Fatalf("driver protocol=%s state=%+v, want auto without reason", x.lastProtocol, p)
	}
	if got := st.Get().Uplink.UDPSupported; got != state.TriTrue {
		t.Fatalf("udpSupported=%s want true", got)
	}
}

func TestUDPProbeTimeoutFallsBackToTCP(t *testing.T) {
	r, x, u, _, st, cfg := newTestReconciler(t)
	u.udpNever = true
	r.udpWait = 20 * time.Millisecond
	mustPatch(t, cfg, `{"uplink":{"mode":"socks5","socks5":{"host":"192.0.2.1","port":1080}}}`)
	st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredConnected })
	r.reconcile(context.Background())

	p := st.Get().ExpressVPN.Protocol
	if x.lastProtocol != "lightway_tcp" || p.Reason != "uplink UDP support unknown" {
		t.Fatalf("driver protocol=%s state=%+v", x.lastProtocol, p)
	}
}

func TestNoUDPWaitWhenDisconnected(t *testing.T) {
	r, _, u, _, _, cfg := newTestReconciler(t)
	mustPatch(t, cfg, `{"uplink":{"mode":"socks5","socks5":{"host":"192.0.2.1","port":1080}}}`)
	r.reconcile(context.Background())
	if got := u.waits.Load(); got != 0 {
		t.Fatalf("WaitUDP calls=%d want 0 (desired=disconnected)", got)
	}
}

// Результат пробы, опубликованный раньше возврата Apply, не затирается его
// unknown.
func TestApplyDoesNotDowngradeProbeResult(t *testing.T) {
	r, _, u, _, st, cfg := newTestReconciler(t)
	u.earlyProbe = func() { st.Update(func(s *state.State) { s.Uplink.UDPSupported = state.TriFalse }) }
	mustPatch(t, cfg, `{"uplink":{"mode":"socks5","socks5":{"host":"192.0.2.1","port":1080}}}`)
	r.reconcile(context.Background())
	if got := st.Get().Uplink.UDPSupported; got != state.TriFalse {
		t.Fatalf("udpSupported=%s want false", got)
	}
}

package xvpn

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"detour/agent/internal/state"
)

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

func newTestManager(d *fakeDaemon) (*Manager, *state.Store, *recHandler) {
	st := state.NewStore(state.Initial("test"))
	h := &recHandler{}
	m := NewManager(NewCLIWithRunner(d), st, slog.New(h))
	m.t = timings{
		connectTimeout:    time.Second,
		connectPoll:       time.Millisecond,
		disconnectTimeout: 50 * time.Millisecond,
		disconnectPoll:    time.Millisecond,
		retryDelay:        time.Millisecond,
	}
	return m, st, h
}

func indexOf(cmds []string, prefix string) int {
	for i, c := range cmds {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var ce *CLIError
	if !errors.As(err, &ce) || ce.Code != code {
		t.Fatalf("err=%v, want code %s", err, code)
	}
}

func TestDisconnectWaitsForDisconnected(t *testing.T) {
	for _, from := range []string{"Connected", "Reconnecting"} {
		d := newFakeDaemon().connected("sg", "lightwaytcp")
		if from == "Reconnecting" {
			d.dropped()
		}
		m, st, _ := newTestManager(d)
		old := time.Now().Add(-time.Hour)
		st.Update(func(s *state.State) { s.ExpressVPN.ConnectedAt = &old })

		if err := m.Disconnect(context.Background()); err != nil {
			t.Fatalf("from %s: %v", from, err)
		}
		if got, _, _, _ := d.snapshot(); got != "Disconnected" {
			t.Fatalf("from %s: Disconnect returned while daemon is %s", from, got)
		}
		if st.Get().ExpressVPN.ConnectedAt != nil {
			t.Fatalf("from %s: connectedAt must be cleared", from)
		}
	}
}

func TestDisconnectFailsWhileStuckDisconnecting(t *testing.T) {
	d := newFakeDaemon().connected("sg", "lightwaytcp")
	d.hangDisconnect = true
	m, _, _ := newTestManager(d)
	wantCode(t, m.Disconnect(context.Background()), "disconnect_failed")
}

// Основной сценарий отчёта: демон считает мёртвую сессию живой — Connect
// обязан создать новую, а не принять no-op `connect` за подключение.
func TestConnectFromConnectedCreatesNewSession(t *testing.T) {
	d := newFakeDaemon().connected("sg", "lightwaytcp")
	m, st, h := newTestManager(d)
	old := time.Now().Add(-time.Hour)
	st.Update(func(s *state.State) { s.ExpressVPN.ConnectedAt = &old })

	if err := m.Connect(context.Background(), "sg", "lightway_tcp"); err != nil {
		t.Fatal(err)
	}
	cmds := d.commands()
	di, ci := indexOf(cmds, "disconnect"), indexOf(cmds, "connect sg")
	if di < 0 || ci < 0 || di > ci {
		t.Fatalf("want disconnect before connect, got %q", cmds)
	}
	if st0, sessions, _, _ := d.snapshot(); st0 != "Connected" || sessions != 2 {
		t.Fatalf("state=%s sessions=%d, want a new session (Connected/2)", st0, sessions)
	}
	if at := st.Get().ExpressVPN.ConnectedAt; at == nil || !at.After(old) {
		t.Fatalf("connectedAt=%v, want the new session's time", at)
	}
	if !h.has("disconnecting before new session daemonState=Connected") {
		t.Fatalf("no log about disconnect before new session: %q", h.msgs)
	}
	if !h.has("session established attempt=1 disconnect=") {
		t.Fatalf("no phase timings of the new session: %q", h.msgs)
	}
}

// Событие 2 отчёта: демон сам переподключается и не отвечает на `set` —
// Connect сначала останавливает его цикл и не упирается в daemon_not_ready.
func TestConnectFromReconnecting(t *testing.T) {
	d := newFakeDaemon().connected("sg", "lightwaytcp").dropped()
	m, _, _ := newTestManager(d)
	if err := m.Connect(context.Background(), "sg", "lightway_tcp"); err != nil {
		t.Fatal(err)
	}
	if st0, sessions, _, _ := d.snapshot(); st0 != "Connected" || sessions != 2 {
		t.Fatalf("state=%s sessions=%d", st0, sessions)
	}
}

func TestConnectFromDisconnectedSkipsDisconnect(t *testing.T) {
	d := newFakeDaemon()
	m, _, _ := newTestManager(d)
	if err := m.Connect(context.Background(), "sg", "lightway_tcp"); err != nil {
		t.Fatal(err)
	}
	if i := indexOf(d.commands(), "disconnect"); i >= 0 {
		t.Fatalf("unexpected disconnect: %q", d.commands())
	}
	if _, sessions, _, _ := d.snapshot(); sessions != 1 {
		t.Fatalf("sessions=%d", sessions)
	}
}

func TestConnectDoesNotConnectWhenDisconnectHangs(t *testing.T) {
	d := newFakeDaemon().connected("sg", "lightwaytcp")
	d.hangDisconnect = true
	m, _, _ := newTestManager(d)
	wantCode(t, m.Connect(context.Background(), "sg", "lightway_tcp"), "disconnect_failed")
	if i := indexOf(d.commands(), "connect"); i >= 0 {
		t.Fatalf("connect must not be issued while Disconnecting: %q", d.commands())
	}
}

// `set protocol` при Connected откладывается демоном — новая сессия должна
// идти по запрошенному протоколу.
func TestConnectAppliesProtocolToNewSession(t *testing.T) {
	d := newFakeDaemon().connected("sg", "lightwaytcp")
	m, _, _ := newTestManager(d)
	if err := m.Connect(context.Background(), "sg", "lightway_udp"); err != nil {
		t.Fatal(err)
	}
	if _, _, proto, _ := d.snapshot(); proto != ProtocolToCLI("lightway_udp") {
		t.Fatalf("session protocol=%s, want %s", proto, ProtocolToCLI("lightway_udp"))
	}
}

func TestConnectLocationChangeGoesThroughDisconnected(t *testing.T) {
	d := newFakeDaemon().connected("sg", "lightwaytcp")
	m, _, _ := newTestManager(d)
	if err := m.Connect(context.Background(), "nl", "lightway_tcp"); err != nil {
		t.Fatal(err)
	}
	cmds := d.commands()
	if di, ci := indexOf(cmds, "disconnect"), indexOf(cmds, "connect nl"); di < 0 || di > ci {
		t.Fatalf("want disconnect before connect, got %q", cmds)
	}
	if _, _, _, loc := d.snapshot(); loc != "nl" {
		t.Fatalf("location=%s", loc)
	}
}

// Отключение демона, пока агент сам переподключает, — не авария.
func TestSuperviseIgnoresAgentInitiatedDisconnect(t *testing.T) {
	for _, prev := range []state.Connection{state.ConnReconnecting, state.ConnConnecting} {
		d := newFakeDaemon()
		m, st, h := newTestManager(d)
		st.Update(func(s *state.State) {
			s.Desired.Connection = state.DesiredConnected
			s.ExpressVPN.Connection = prev
		})
		woken := false
		m.superviseOnce(context.Background(), func() { woken = true })
		if woken || st.Get().ExpressVPN.Connection != prev || h.has("connection dropped") {
			t.Fatalf("prev=%s: woken=%v state=%s logs=%q", prev, woken, st.Get().ExpressVPN.Connection, h.msgs)
		}
	}
}

func TestSuperviseDetectsDrop(t *testing.T) {
	d := newFakeDaemon().connected("sg", "lightwaytcp").dropped()
	m, st, h := newTestManager(d)
	st.Update(func(s *state.State) {
		s.Desired.Connection = state.DesiredConnected
		s.ExpressVPN.Connection = state.ConnConnected
	})
	woken := false
	m.superviseOnce(context.Background(), func() { woken = true })
	if !woken || st.Get().ExpressVPN.Connection != state.ConnReconnecting || !h.has("connection dropped") {
		t.Fatalf("woken=%v state=%s logs=%q", woken, st.Get().ExpressVPN.Connection, h.msgs)
	}
}

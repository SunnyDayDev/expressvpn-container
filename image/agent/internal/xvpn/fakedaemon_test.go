package xvpn

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDaemon — модель expressvpnctl по таблице «Семантика команд при
// активной сессии» (testdata/NOTES.md). Реализует Runner: `connect` и
// `disconnect` возвращаются до фактического перехода, переходы происходят по
// мере опросов `get connectionstate`.
type fakeDaemon struct {
	mu sync.Mutex

	state    string // как печатает `get connectionstate`
	location string
	pending  string // из `set protocol`: применяется при следующем подключении
	protocol string // протокол активной сессии
	sessions int    // сколько раз демон приходил в Connected

	steps int // опросов до следующего перехода

	connectPolls    int  // опросов от Connecting/Reconnecting до Connected
	disconnectPolls int  // опросов от Disconnecting до Disconnected
	hangDisconnect  bool // Disconnecting не заканчивается
	stuckReconnect  bool // собственный цикл переподключения демона не заканчивается

	cmds []string // все команды, кроме `get connectionstate`
}

func newFakeDaemon() *fakeDaemon {
	return &fakeDaemon{state: "Disconnected", connectPolls: 2, disconnectPolls: 2}
}

// connected — демон уже в сессии (как после подключения, до теста).
func (d *fakeDaemon) connected(location, protocol string) *fakeDaemon {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state, d.location, d.pending, d.protocol, d.sessions = "Connected", location, protocol, protocol, 1
	return d
}

// dropped — демон сам обнаружил разрыв и крутит свой цикл переподключения,
// который не может завершиться (uplink под ним мёртв).
func (d *fakeDaemon) dropped() *fakeDaemon {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state, d.stuckReconnect = "Reconnecting", true
	return d
}

func (d *fakeDaemon) snapshot() (st string, sessions int, protocol, location string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state, d.sessions, d.protocol, d.location
}

func (d *fakeDaemon) commands() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.cmds...)
}

func (d *fakeDaemon) Run(_ context.Context, _ string, args ...string) (string, int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(args) >= 2 && args[0] == "-t" {
		args = args[2:]
	}
	if strings.Join(args, " ") == "get connectionstate" {
		d.advance()
		return d.state + "\n", 0, nil
	}
	d.cmds = append(d.cmds, strings.Join(args, " "))
	// Пока демон переподключается сам, он не отвечает на изменения
	// (событие 2 отчёта: `daemon_not_ready`, таймаут 15 с).
	busy := d.state == "Reconnecting" || d.state == "DisconnectingToReconnect"
	const timedOut = "Timed out after 15.001 sec\n"

	switch args[0] {
	case "connect":
		loc := ""
		if len(args) > 1 {
			loc = args[1]
		}
		switch {
		case d.state == "Connected" && (loc == "" || loc == d.location) && d.pending == d.protocol:
			// no-op: демон считает сессию живой, и ничего не изменилось
		case d.state == "Connected":
			// Другая локация или отложенный `set protocol` — демон
			// переподключается сам.
			if loc != "" {
				d.location = loc
			}
			d.state, d.steps = "DisconnectingToReconnect", d.disconnectPolls
		case d.state == "Disconnected":
			if loc == "" {
				loc = "smart"
			}
			d.state, d.location, d.steps = "Connecting", loc, d.connectPolls
		default:
			return timedOut, 2, nil
		}
	case "disconnect":
		if d.state != "Disconnected" {
			d.state, d.steps = "Disconnecting", d.disconnectPolls
		}
	case "set":
		if len(args) >= 3 && args[1] == "protocol" {
			if busy {
				return timedOut, 2, nil
			}
			d.pending = args[2]
		}
	case "get":
		switch args[1] {
		case "region":
			return d.location + "\n", 0, nil
		case "pubip":
			return "203.0.113.7\n", 0, nil
		}
	}
	return "", 0, nil
}

// advance — переход по очередному опросу состояния.
func (d *fakeDaemon) advance() {
	switch {
	case d.state == "Disconnecting" && d.hangDisconnect,
		d.state == "Reconnecting" && d.stuckReconnect:
		return
	case d.state != "Connecting" && d.state != "Reconnecting" &&
		d.state != "Disconnecting" && d.state != "DisconnectingToReconnect":
		return
	}
	if d.steps > 1 {
		d.steps--
		return
	}
	d.steps = 0
	switch d.state {
	case "Connecting", "Reconnecting":
		d.state, d.protocol = "Connected", d.pending
		d.sessions++
	case "Disconnecting":
		d.state = "Disconnected"
	case "DisconnectingToReconnect":
		d.state, d.steps = "Reconnecting", d.connectPolls
	}
}

// pollUntil опрашивает состояние, пока оно не станет want (или сдаётся).
func pollUntil(t *testing.T, cli *CLI, want string) {
	t.Helper()
	for i := 0; i < 20; i++ {
		raw, err := cli.RawConnectionState(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if raw == want {
			return
		}
	}
	t.Fatalf("daemon never reached %s", want)
}

// Самопроверка модели — по строке таблицы NOTES на тест.

func TestFakeDaemonConnectSameLocationIsNoop(t *testing.T) {
	d := newFakeDaemon().connected("sg", "lightwaytcp")
	cli := NewCLIWithRunner(d)
	if err := cli.Connect(context.Background(), "sg", time.Second); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, cli, "Connected")
	if st, sessions, _, _ := d.snapshot(); st != "Connected" || sessions != 1 {
		t.Fatalf("state=%s sessions=%d, want Connected/1 (no-op)", st, sessions)
	}
}

func TestFakeDaemonSetProtocolWhileConnectedIsDeferred(t *testing.T) {
	d := newFakeDaemon().connected("sg", "lightwaytcp")
	cli := NewCLIWithRunner(d)
	if err := cli.Set(context.Background(), "protocol", "lightwayudp"); err != nil {
		t.Fatal(err)
	}
	pollUntil(t, cli, "Connected")
	if _, sessions, proto, _ := d.snapshot(); proto != "lightwaytcp" || sessions != 1 {
		t.Fatalf("protocol=%s sessions=%d, want lightwaytcp/1 until next connection", proto, sessions)
	}
}

func TestFakeDaemonConnectAfterProtocolChangeReconnectsItself(t *testing.T) {
	d := newFakeDaemon().connected("sg", "lightwaytcp")
	cli := NewCLIWithRunner(d)
	if err := cli.Set(context.Background(), "protocol", "lightwayudp"); err != nil {
		t.Fatal(err)
	}
	if err := cli.Connect(context.Background(), "sg", time.Second); err != nil {
		t.Fatal(err)
	}
	if st, _, _, _ := d.snapshot(); st != "DisconnectingToReconnect" {
		t.Fatalf("state=%s, want DisconnectingToReconnect", st)
	}
	pollUntil(t, cli, "Connected")
	if _, sessions, proto, _ := d.snapshot(); sessions != 2 || proto != "lightwayudp" {
		t.Fatalf("sessions=%d protocol=%s, want 2/lightwayudp", sessions, proto)
	}
}

func TestFakeDaemonConnectOtherLocationReconnectsItself(t *testing.T) {
	d := newFakeDaemon().connected("sg", "lightwaytcp")
	cli := NewCLIWithRunner(d)
	if err := cli.Connect(context.Background(), "nl", time.Second); err != nil {
		t.Fatal(err)
	}
	if st, _, _, _ := d.snapshot(); st != "DisconnectingToReconnect" {
		t.Fatalf("state=%s, want DisconnectingToReconnect right after connect", st)
	}
	pollUntil(t, cli, "Connected")
	if _, sessions, _, loc := d.snapshot(); sessions != 2 || loc != "nl" {
		t.Fatalf("sessions=%d location=%s, want 2/nl", sessions, loc)
	}
}

func TestFakeDaemonDisconnectReturnsBeforeDisconnected(t *testing.T) {
	for _, from := range []string{"Connected", "Reconnecting"} {
		d := newFakeDaemon().connected("sg", "lightwaytcp")
		if from == "Reconnecting" {
			d.dropped()
		}
		cli := NewCLIWithRunner(d)
		if err := cli.Disconnect(context.Background()); err != nil {
			t.Fatal(err)
		}
		if st, _, _, _ := d.snapshot(); st != "Disconnecting" {
			t.Fatalf("from %s: state=%s, want Disconnecting right after disconnect", from, st)
		}
		pollUntil(t, cli, "Disconnected")
	}
}

func TestFakeDaemonBusyWhileReconnecting(t *testing.T) {
	d := newFakeDaemon().connected("sg", "lightwaytcp").dropped()
	err := NewCLIWithRunner(d).Set(context.Background(), "protocol", "lightwayudp")
	var ce *CLIError
	if !errors.As(err, &ce) || ce.Code != "daemon_not_ready" {
		t.Fatalf("err=%v, want daemon_not_ready", err)
	}
}

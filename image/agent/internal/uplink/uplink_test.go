package uplink

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"detour/agent/internal/config"
	"detour/agent/internal/state"
)

func TestSingboxConfig(t *testing.T) {
	raw, err := SingboxConfig(config.Socks5{Host: "host.docker.internal", Port: 1086, UDP: "auto"}, "192.168.65.254")
	if err != nil {
		t.Fatal(err)
	}
	var conf map[string]any
	if err := json.Unmarshal(raw, &conf); err != nil {
		t.Fatal(err)
	}

	inb := conf["inbounds"].([]any)[0].(map[string]any)
	if inb["type"] != "tun" || inb["interface_name"] != "xup0" {
		t.Fatalf("inbound: %v", inb)
	}
	if inb["auto_route"] != false {
		t.Fatal("auto_route must be false: routes are managed by the agent (D3)")
	}
	if inb["mtu"] != float64(1500) {
		t.Fatalf("mtu=%v", inb["mtu"])
	}

	out := conf["outbounds"].([]any)[0].(map[string]any)
	if out["type"] != "socks" || out["server"] != "192.168.65.254" || out["server_port"] != float64(1086) {
		t.Fatalf("outbound: %v", out)
	}
	if out["bind_interface"] != "eth0" {
		t.Fatal("outbound must bind to eth0 to avoid routing loop")
	}
	if _, hasUser := out["username"]; hasUser {
		t.Fatal("no credentials configured — username must be absent")
	}

	route := conf["route"].(map[string]any)
	if route["final"] != "proxy-out" {
		t.Fatalf("route.final=%v", route["final"])
	}
	if route["auto_detect_interface"] != false {
		t.Fatal("auto_detect_interface must be false")
	}

	dns := conf["dns"].(map[string]any)
	srv := dns["servers"].([]any)[0].(map[string]any)
	if srv["type"] != "https" || srv["detour"] != "proxy-out" {
		t.Fatalf("dns server must be DoH via proxy: %v", srv)
	}
}

func TestSingboxConfigWithAuth(t *testing.T) {
	raw, err := SingboxConfig(config.Socks5{Host: "h", Port: 1, Username: "u", Password: "pw", UDP: "auto"}, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	var conf map[string]any
	json.Unmarshal(raw, &conf)
	out := conf["outbounds"].([]any)[0].(map[string]any)
	if out["username"] != "u" || out["password"] != "pw" {
		t.Fatalf("credentials missing: %v", out)
	}
}

type fakeRunner struct{ cmds []string }

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, int, error) {
	cmd := name + " " + strings.Join(args, " ")
	f.cmds = append(f.cmds, cmd)
	if strings.HasPrefix(cmd, "ip -4 route show default dev eth0") {
		return "default via 172.18.0.1 dev eth0\n", 0, nil
	}
	return "", 0, nil
}

func TestApplyRoutesSequence(t *testing.T) {
	fr := &fakeRunner{}
	ctx := context.Background()
	gw, err := detectGateway(ctx, fr)
	if err != nil {
		t.Fatal(err)
	}
	if gw != "172.18.0.1" {
		t.Fatalf("gw=%q", gw)
	}
	if err := applyRoutes(ctx, fr, "192.168.65.254", gw); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ip route replace 192.168.65.254/32 via 172.18.0.1 dev eth0",
		"ip route replace default via 172.18.0.1 dev eth0 metric 1000",
		// via <peer> обязателен: Lightway ищет default gateway-IP (S2)
		"ip route replace default via 172.29.0.2 dev xup0",
	}
	got := fr.cmds[1:] // [0] — detectGateway
	if len(got) != len(want) {
		t.Fatalf("cmds=%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cmd[%d]=%q want %q", i, got[i], want[i])
		}
	}
	// Порядок критичен: маршрут к прокси и запасной default — ДО подмены
	// основного default на xup0.
}

func TestRestoreHostRoutes(t *testing.T) {
	fr := &fakeRunner{}
	if err := restoreHostRoutes(context.Background(), fr, "192.168.65.254", "172.18.0.1"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fr.cmds[0], "ip route replace default via 172.18.0.1 dev eth0") {
		t.Fatalf("first restore cmd=%q", fr.cmds[0])
	}
}

func TestDNSQueryEncoding(t *testing.T) {
	q := dnsQuery("example.com")
	// header(12) + 1+7+1+3+1 (labels) + 4 (qtype+qclass)
	if len(q) != 12+13+4 {
		t.Fatalf("len=%d", len(q))
	}
	if q[12] != 7 || string(q[13:20]) != "example" || q[20] != 3 || string(q[21:24]) != "com" {
		t.Fatalf("labels wrong: %v", q[12:])
	}
}

// newProbeManager — Manager без Apply (маршруты, sing-box): проба запускается
// напрямую, функция пробы подменена.
func newProbeManager(t *testing.T, probe func(context.Context, string, config.Socks5, *slog.Logger) bool) (*Manager, *state.Store) {
	t.Helper()
	st := state.NewStore(state.Initial("test"))
	st.Update(func(s *state.State) { s.Uplink.Mode = "socks5" })
	m := NewManager(slog.New(slog.DiscardHandler), st)
	m.probe = probe
	return m, st
}

func startProbe(m *Manager, cfg config.Uplink, ip string) {
	m.mu.Lock()
	m.applied = &cfg
	m.startProbeLocked(cfg, ip)
	m.mu.Unlock()
}

var autoUplink = config.Uplink{Mode: "socks5", Socks5: config.Socks5{Host: "192.0.2.1", Port: 1080, UDP: "auto"}}

func TestWaitUDPWaitsForProbe(t *testing.T) {
	m, st := newProbeManager(t, func(context.Context, string, config.Socks5, *slog.Logger) bool {
		time.Sleep(50 * time.Millisecond)
		return true
	})
	startProbe(m, autoUplink, "192.0.2.1")
	if got := st.Get().Uplink.UDPSupported; got != state.TriUnknown {
		t.Fatalf("udpSupported=%s right after start, want unknown", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got := m.WaitUDP(ctx); got != state.TriTrue {
		t.Fatalf("WaitUDP=%s want true", got)
	}
	if got := st.Get().Uplink.UDPSupported; got != state.TriTrue {
		t.Fatalf("state udpSupported=%s want true", got)
	}
}

func TestWaitUDPImmediateWithoutProbe(t *testing.T) {
	m, _ := newProbeManager(t, nil)
	canceled, cancel := context.WithCancel(context.Background())
	cancel() // ответ не должен зависеть от ожидания
	for _, c := range []struct {
		cfg  config.Uplink
		want state.TriState
	}{
		{config.Uplink{Mode: "host"}, state.TriTrue},
		{config.Uplink{Mode: "socks5", Socks5: config.Socks5{UDP: "on"}}, state.TriTrue},
		{config.Uplink{Mode: "socks5", Socks5: config.Socks5{UDP: "off"}}, state.TriFalse},
	} {
		cfg := c.cfg
		m.mu.Lock()
		m.applied, m.probeRun = &cfg, nil
		m.mu.Unlock()
		if got := m.WaitUDP(canceled); got != c.want {
			t.Fatalf("%+v: WaitUDP=%s want %s", c.cfg, got, c.want)
		}
	}
}

func TestWaitUDPTimeoutIsUnknown(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	m, _ := newProbeManager(t, func(context.Context, string, config.Socks5, *slog.Logger) bool {
		<-release
		return true
	})
	startProbe(m, autoUplink, "192.0.2.1")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if got := m.WaitUDP(ctx); got != state.TriUnknown {
		t.Fatalf("WaitUDP=%s want unknown on timeout", got)
	}
}

// Проба прежнего uplink'а, закончившаяся позже, не должна записать свой
// результат поверх нового.
func TestStaleProbeDoesNotPublish(t *testing.T) {
	releaseOld := make(chan struct{})
	m, st := newProbeManager(t, func(_ context.Context, ip string, _ config.Socks5, _ *slog.Logger) bool {
		if ip == "192.0.2.1" { // прежний uplink: отвечает поздно и «с UDP»
			<-releaseOld
			return true
		}
		return false
	})
	startProbe(m, autoUplink, "192.0.2.1")
	m.mu.Lock()
	oldRun := m.probeRun
	m.mu.Unlock()

	startProbe(m, autoUplink, "192.0.2.2") // повторный Apply: новая проба
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got := m.WaitUDP(ctx); got != state.TriFalse {
		t.Fatalf("WaitUDP=%s want false (new probe)", got)
	}

	close(releaseOld)
	<-oldRun.done
	if got := st.Get().Uplink.UDPSupported; got != state.TriFalse {
		t.Fatalf("udpSupported=%s: stale probe overwrote the new result", got)
	}
}

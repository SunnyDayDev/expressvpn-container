package killswitch

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"detour/agent/internal/config"
	"detour/agent/internal/state"
)

func TestRulesetHostMode(t *testing.T) {
	p := Defaults()
	p.Mode = "host"
	p.DockerSubnet = "172.18.0.0/16"
	r := Ruleset(p)
	if !strings.Contains(r, `meta mark 0x5050 oifname "eth0" ip daddr 172.18.0.0/16 accept`) {
		t.Error("marked replies to docker subnet must be allowed (UDP relay)")
	}

	for _, want := range []string{
		"add table inet detour",
		"flush table inet detour",
		`oifname "lo" accept`,
		`meta mark 0x5050 oifname "tun0" accept`,
		"meta mark 0x5050 counter name dropped_marked drop",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("host ruleset missing %q:\n%s", want, r)
		}
	}
	// В host-режиме egress через eth0 не ограничивается.
	if strings.Contains(r, "dropped_egress") {
		t.Errorf("host ruleset must not restrict eth0 egress:\n%s", r)
	}
	// Атомарность: add+flush идут до определения таблицы.
	if strings.Index(r, "add table") > strings.Index(r, "chain output") {
		t.Error("add table must precede chain definitions")
	}
}

func TestRulesetSocks5Mode(t *testing.T) {
	p := Defaults()
	p.Mode = "socks5"
	p.ProxyIP = "192.168.65.254"
	p.ProxyPort = 1086
	p.DockerSubnet = "172.18.0.0/16"
	r := Ruleset(p)

	for _, want := range []string{
		`meta mark 0x5050 oifname "tun0" accept`,
		"meta mark 0x5050 counter name dropped_marked drop",
		`oifname "eth0" ip daddr 192.168.65.254 accept`,
		`oifname "eth0" ip daddr 172.18.0.0/16 accept`,
		`oifname "eth0" counter name dropped_egress drop`,
	} {
		if !strings.Contains(r, want) {
			t.Errorf("socks5 ruleset missing %q:\n%s", want, r)
		}
	}
	// Разрешение на прокси должно стоять ДО drop-правила eth0.
	if strings.Index(r, "ip daddr 192.168.65.254") > strings.Index(r, "dropped_egress drop") {
		t.Error("proxy accept must precede egress drop")
	}
	// Помеченный трафик дропается до eth0-разрешений: клиенты прокси не могут
	// уйти к uplink-прокси напрямую.
	if strings.Index(r, "dropped_marked drop") > strings.Index(r, "ip daddr 192.168.65.254") {
		t.Error("marked drop must precede eth0 accepts")
	}
}

type fakeRunner struct {
	calls    [][]string
	contents []string
	exit     int
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if name == "nft" && len(args) == 2 && args[0] == "-f" {
		raw, err := os.ReadFile(args[1])
		if err != nil {
			return "cannot read " + args[1], 1, nil
		}
		f.contents = append(f.contents, string(raw))
	}
	return "", f.exit, nil
}

func TestManagerApplySingleAtomicCall(t *testing.T) {
	st := state.NewStore(state.Initial("test"))
	fr := &fakeRunner{}
	m := NewManagerWithRunner(st, slog.New(slog.DiscardHandler), fr,
		func(_ context.Context, host string) (string, error) { return "10.0.0.9", nil })

	cfg := config.Uplink{Mode: "socks5", Socks5: config.Socks5{Host: "proxy.local", Port: 1086, UDP: "auto"}}
	if err := m.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("nft calls=%d want 1 (atomic)", len(fr.calls))
	}
	if len(fr.contents) != 1 || !strings.Contains(fr.contents[0], "ip daddr 10.0.0.9 accept") {
		t.Fatalf("ruleset content: %v", fr.contents)
	}
	if !strings.Contains(fr.contents[0], "flush table inet detour") {
		t.Fatal("missing atomic flush")
	}
}

func TestManagerApplyFailure(t *testing.T) {
	st := state.NewStore(state.Initial("test"))
	fr := &fakeRunner{exit: 1} // и nft, и iptables-фолбэк падают
	m := NewManagerWithRunner(st, slog.New(slog.DiscardHandler), fr, nil)
	if err := m.Apply(context.Background(), config.Uplink{Mode: "host"}); err == nil {
		t.Fatal("want error when both backends fail")
	}
}

// nftFailsRunner: nft недоступен (старое ядро NAS), iptables-legacy работает.
type nftFailsRunner struct {
	calls    [][]string
	restored []string
	jumpOK   bool
}

func (f *nftFailsRunner) Run(_ context.Context, name string, args ...string) (string, int, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	switch name {
	case "nft":
		return "netlink: Error: cache initialization failed: Invalid argument", 1, nil
	case "iptables-legacy-restore":
		raw, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			return "cannot read", 1, nil
		}
		f.restored = append(f.restored, string(raw))
		return "", 0, nil
	case "iptables-legacy":
		if args[0] == "-C" {
			if f.jumpOK {
				return "", 0, nil
			}
			return "", 1, nil // jump ещё нет
		}
		if args[0] == "-I" {
			f.jumpOK = true
			return "", 0, nil
		}
		if args[0] == "-L" {
			return "  pkts bytes target prot opt in out source destination\n" +
				"     7  420 DROP  all  --  *  *   0.0.0.0/0  0.0.0.0/0  mark match 0x5050\n" +
				"     5  300 DROP  all  --  *  eth0 0.0.0.0/0 0.0.0.0/0\n", 0, nil
		}
	}
	return "", 0, nil
}

func TestManagerFallsBackToIptables(t *testing.T) {
	st := state.NewStore(state.Initial("test"))
	fr := &nftFailsRunner{}
	m := NewManagerWithRunner(st, slog.New(slog.DiscardHandler), fr,
		func(_ context.Context, host string) (string, error) { return "198.51.100.7", nil })

	cfg := config.Uplink{Mode: "socks5", Socks5: config.Socks5{Host: "198.51.100.7", Port: 10808, UDP: "auto"}}
	if err := m.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if m.backend != "iptables" {
		t.Fatalf("backend=%q", m.backend)
	}
	if len(fr.restored) != 1 {
		t.Fatalf("restores=%d", len(fr.restored))
	}
	r := fr.restored[0]
	for _, want := range []string{
		"-F DETOUR_OUT",
		"-A DETOUR_OUT -m mark --mark 0x5050 -o tun0 -j ACCEPT",
		"-A DETOUR_OUT -m mark --mark 0x5050 -j DROP",
		"-A DETOUR_OUT -o eth0 -d 198.51.100.7 -j ACCEPT",
		"-A DETOUR_OUT -o eth0 -j DROP",
		"COMMIT",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("ruleset missing %q:\n%s", want, r)
		}
	}
	if !fr.jumpOK {
		t.Fatal("OUTPUT jump was not inserted")
	}

	// Счётчик дропов из iptables -L.
	n, err := m.Dropped(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 12 {
		t.Fatalf("dropped=%d want 12", n)
	}

	// Повторный Apply: jump уже есть — не дублируется (нет второго -I).
	if err := m.Apply(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	inserts := 0
	for _, c := range fr.calls {
		if c[0] == "iptables-legacy" && c[1] == "-I" {
			inserts++
		}
	}
	if inserts != 1 {
		t.Fatalf("OUTPUT jump inserted %d times", inserts)
	}
}

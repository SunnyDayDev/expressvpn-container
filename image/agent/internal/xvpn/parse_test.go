package xvpn

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"detour/agent/internal/state"
)

var rcLine = regexp.MustCompile(`(?m)^rc=(\d+)\n?$`)

// readGolden возвращает вывод CLI из testdata и код выхода (строка rc=N
// добавлена скриптом снятия; -1 — если её нет).
func readGolden(t *testing.T, name string) (string, int) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	exit := -1
	if m := rcLine.FindStringSubmatch(s); m != nil {
		exit, _ = strconv.Atoi(m[1])
		s = rcLine.ReplaceAllString(s, "")
	}
	return s, exit
}

func TestParseConnectionStateGolden(t *testing.T) {
	out, _ := readGolden(t, "get_connectionstate_disconnected.txt")
	if got := ParseConnectionState(out); got != state.ConnDisconnected {
		t.Fatalf("got %v", got)
	}
}

func TestParseConnectionStateAll(t *testing.T) {
	cases := map[string]state.Connection{
		"Disconnected":             state.ConnDisconnected,
		"Connecting":               state.ConnConnecting,
		"Connected":                state.ConnConnected,
		"Interrupted":              state.ConnReconnecting,
		"Reconnecting":             state.ConnReconnecting,
		"DisconnectingToReconnect": state.ConnReconnecting,
		"Disconnecting":            state.ConnDisconnected,
	}
	for in, want := range cases {
		if got := ParseConnectionState(in + "\n"); got != want {
			t.Errorf("%s → %v, want %v", in, got, want)
		}
	}
}

func TestParseStatusLoggedOutGolden(t *testing.T) {
	out, _ := readGolden(t, "status_logged_out.txt")
	st := ParseStatus(out)
	if st.LoggedIn {
		t.Fatal("want logged out")
	}
	if st.NetworkLock != "enabled when connected" {
		t.Fatalf("networklock=%q", st.NetworkLock)
	}
	if st.SplitTunnel != "disabled" {
		t.Fatalf("splittunnel=%q", st.SplitTunnel)
	}
}

func TestParseRegionsLoggedOutGolden(t *testing.T) {
	out, _ := readGolden(t, "get_regions_logged_out.txt")
	regions := ParseRegions(out)
	if len(regions) != 1 || regions[0] != "smart" {
		t.Fatalf("regions=%v", regions)
	}
}

func TestParseSmartGolden(t *testing.T) {
	out, _ := readGolden(t, "get_smart_logged_out.txt")
	if got := ParseSmart(out); got != "" {
		t.Fatalf("smart=%q want empty (N/A)", got)
	}
	if got := ParseSmart("germany-frankfurt-1\n"); got != "germany-frankfurt-1" {
		t.Fatalf("smart=%q", got)
	}
}

func TestParseProtocolGolden(t *testing.T) {
	out, _ := readGolden(t, "get_protocol.txt")
	if got := ProtocolToAPI(ParseValue(out)); got != "auto" {
		t.Fatalf("protocol=%q", got)
	}
}

func TestProtocolMappingRoundTrip(t *testing.T) {
	for _, api := range []string{"auto", "lightway_udp", "lightway_tcp", "openvpn_udp", "openvpn_tcp", "wireguard"} {
		if got := ProtocolToAPI(ProtocolToCLI(api)); got != api {
			t.Errorf("round trip %s → %s", api, got)
		}
	}
	if got := ProtocolToCLI("lightway_tcp"); got != "lightwaytcp" {
		t.Fatalf("cli name=%q", got)
	}
}

func TestParseValueUnknown(t *testing.T) {
	out, _ := readGolden(t, "get_pubip.txt")
	if got := ParseValue(out); got != "" {
		t.Fatalf("pubip=%q want empty (Unknown)", got)
	}
}

func TestClassifyLoginInvalidCodeGolden(t *testing.T) {
	out, exit := readGolden(t, "login_invalid_code.txt")
	if exit != 127 {
		t.Fatalf("golden exit=%d", exit)
	}
	err := classifyError("login", out, exit)
	ce, ok := err.(*CLIError)
	if !ok || ce.Code != "invalid_activation_code" {
		t.Fatalf("err=%v", err)
	}
}

func TestClassifyUnknownRegionGolden(t *testing.T) {
	out, exit := readGolden(t, "connect_unknown_region_logged_out.txt")
	err := classifyError("connect", out, exit)
	ce, ok := err.(*CLIError)
	if !ok || ce.Code != "unknown_location" {
		t.Fatalf("err=%v", err)
	}

	out, exit = readGolden(t, "set_region_unknown.txt")
	err = classifyError("set", out, exit)
	ce, ok = err.(*CLIError)
	if !ok || ce.Code != "unknown_location" {
		t.Fatalf("err=%v", err)
	}
}

func TestClassifyRequiresLoginGolden(t *testing.T) {
	out, exit := readGolden(t, "connect_logged_out.txt")
	err := classifyError("connect", out, exit)
	ce, ok := err.(*CLIError)
	if !ok || ce.Code != "not_logged_in" {
		t.Fatalf("err=%v", err)
	}
}

func TestClassifyTimeout(t *testing.T) {
	err := classifyError("get", "Timed out after 15.001 sec\n", 2)
	ce, ok := err.(*CLIError)
	if !ok || ce.Code != "daemon_not_ready" {
		t.Fatalf("err=%v", err)
	}
}

func TestClassifySuccess(t *testing.T) {
	if err := classifyError("set", "", 0); err != nil {
		t.Fatal(err)
	}
}

// TestLoginCodeNeverInArgs: код активации передаётся только через временный
// файл 0600 — в argv expressvpnctl попадает путь файла, не сам код; файл
// удаляется после вызова.
func TestLoginCodeNeverInArgs(t *testing.T) {
	const code = "SECRET-ACTIVATION-CODE"
	var loginFile string
	cli := NewCLIWithRunner(runnerFunc(func(args []string) (string, int) {
		for i, a := range args {
			if strings.Contains(a, code) {
				t.Fatalf("activation code leaked into argv: %v", args)
			}
			if a == "login" && i+1 < len(args) {
				loginFile = args[i+1]
			}
		}
		if loginFile == "" {
			t.Fatalf("no login file in argv: %v", args)
		}
		raw, err := os.ReadFile(loginFile)
		if err != nil {
			t.Fatalf("login file unreadable during call: %v", err)
		}
		if strings.TrimSpace(string(raw)) != code {
			t.Fatalf("login file content=%q", raw)
		}
		fi, err := os.Stat(loginFile)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("login file perm=%v want 0600", fi.Mode().Perm())
		}
		return "", 0
	}))
	if err := cli.Login(t.Context(), code); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(loginFile); !os.IsNotExist(err) {
		t.Fatal("login file not removed after call")
	}
}

type runnerFunc func(args []string) (string, int)

func (f runnerFunc) Run(_ context.Context, _ string, args ...string) (string, int, error) {
	out, exit := f(args)
	return out, exit, nil
}

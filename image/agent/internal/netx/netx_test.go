package netx

import (
	"os"
	"path/filepath"
	"testing"
)

// tunDNS — адрес перехвата sing-box (uplink.TunPeer), которым uplink
// подменяет nameserver в socks5-режиме.
const tunDNS = "172.29.0.2"

const redirected = "# detour: socks5 uplink mode — DNS via sing-box DoH over proxy\nnameserver " + tunDNS + "\n"

func TestPickNameserver(t *testing.T) {
	cases := []struct {
		name, conf string
		want       string
	}{
		{"docker embedded", "nameserver 127.0.0.11\nsearch lan\noptions ndots:0\n", "127.0.0.11"},
		{"first of several", "# generated\nnameserver 192.168.1.1\nnameserver 1.1.1.1\n", "192.168.1.1"},
		{"indented", "  nameserver\t10.0.0.53  \n", "10.0.0.53"},
		{"already redirected", redirected, defaultContainerDNS},
		{"redirected, original kept below", redirected + "nameserver 10.0.0.53\n", "10.0.0.53"},
		{"no nameserver", "search lan\n", defaultContainerDNS},
		{"empty", "", defaultContainerDNS},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickNameserver([]byte(c.conf), []string{tunDNS}); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// withResolvConf подменяет путь resolv.conf и сбрасывает пин на время теста.
func withResolvConf(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "resolv.conf")
	writeFile(t, path, content)
	prevPath, prevDNS := resolvConfPath, containerDNS
	resolvConfPath, containerDNS = path, ""
	t.Cleanup(func() { resolvConfPath, containerDNS = prevPath, prevDNS })
	return path
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Регрессия: пин берётся на старте и не меняется, когда uplink позже
// переписывает resolv.conf под перехват sing-box.
func TestPinSurvivesResolvConfRedirect(t *testing.T) {
	path := withResolvConf(t, "nameserver 127.0.0.11\noptions ndots:0\n")

	if got := PinContainerDNS(tunDNS); got != "127.0.0.11" {
		t.Fatalf("pin: got %q", got)
	}
	writeFile(t, path, redirected)

	if got := ContainerDNS(); got != "127.0.0.11" {
		t.Fatalf("after redirect: ContainerDNS()=%q, want pinned 127.0.0.11", got)
	}
	if got := PinContainerDNS(tunDNS); got != "127.0.0.11" {
		t.Fatalf("repeated pin must be a no-op: got %q", got)
	}
}

// ContainerDNS до пина не читает файл: иначе первый резолв имени мог бы
// застать уже перенаправленный resolv.conf.
func TestContainerDNSDoesNotReadFileLazily(t *testing.T) {
	withResolvConf(t, "nameserver 10.1.2.3\n")

	if got := ContainerDNS(); got != defaultContainerDNS {
		t.Fatalf("before pin: got %q, want %q", got, defaultContainerDNS)
	}
	if got := PinContainerDNS(tunDNS); got != "10.1.2.3" {
		t.Fatalf("pin: got %q", got)
	}
	if got := ContainerDNS(); got != "10.1.2.3" {
		t.Fatalf("after pin: got %q", got)
	}
}

// Агент стартует, когда resolv.conf уже указывает на sing-box (авария без
// восстановления файла): адрес перехвата не пинуется.
func TestPinIgnoresTunDNS(t *testing.T) {
	withResolvConf(t, redirected)

	if got := PinContainerDNS(tunDNS); got != defaultContainerDNS {
		t.Fatalf("got %q, want %q", got, defaultContainerDNS)
	}
}

func TestPinMissingResolvConf(t *testing.T) {
	path := withResolvConf(t, "")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := PinContainerDNS(tunDNS); got != defaultContainerDNS {
		t.Fatalf("got %q, want %q", got, defaultContainerDNS)
	}
}

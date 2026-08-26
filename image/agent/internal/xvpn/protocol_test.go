package xvpn

import (
	"testing"

	"detour/agent/internal/state"
)

// Матрица задачи 5.5: host/socks5 × udp on/off/unknown × protocol.
func TestEffectiveProtocolMatrix(t *testing.T) {
	cases := []struct {
		requested  string
		mode       string
		udp        state.TriState
		wantEff    string
		wantReason bool
	}{
		// host: uplink не ограничивает ничего, udpSupported не важен.
		{"auto", "host", state.TriUnknown, "auto", false},
		{"lightway_udp", "host", state.TriFalse, "lightway_udp", false},
		{"wireguard", "host", state.TriUnknown, "wireguard", false},
		{"openvpn_tcp", "host", state.TriTrue, "openvpn_tcp", false},

		// socks5 + UDP поддерживается: как запрошено.
		{"auto", "socks5", state.TriTrue, "auto", false},
		{"lightway_udp", "socks5", state.TriTrue, "lightway_udp", false},
		{"openvpn_udp", "socks5", state.TriTrue, "openvpn_udp", false},
		{"wireguard", "socks5", state.TriTrue, "wireguard", false},
		{"lightway_tcp", "socks5", state.TriTrue, "lightway_tcp", false},

		// socks5 без UDP: UDP-протоколы и auto падают в lightway_tcp.
		{"auto", "socks5", state.TriFalse, "lightway_tcp", true},
		{"lightway_udp", "socks5", state.TriFalse, "lightway_tcp", true},
		{"openvpn_udp", "socks5", state.TriFalse, "lightway_tcp", true},
		{"wireguard", "socks5", state.TriFalse, "lightway_tcp", true},
		{"lightway_tcp", "socks5", state.TriFalse, "lightway_tcp", false},
		{"openvpn_tcp", "socks5", state.TriFalse, "openvpn_tcp", false},

		// socks5, UDP неизвестен (probe ещё не прошёл): консервативный TCP.
		{"auto", "socks5", state.TriUnknown, "lightway_tcp", true},
		{"lightway_udp", "socks5", state.TriUnknown, "lightway_tcp", true},
		{"openvpn_tcp", "socks5", state.TriUnknown, "openvpn_tcp", false},

		// Пустой requested трактуется как auto.
		{"", "host", state.TriUnknown, "auto", false},
		{"", "socks5", state.TriFalse, "lightway_tcp", true},
	}
	for _, c := range cases {
		eff, reason := EffectiveProtocol(c.requested, c.mode, c.udp)
		if eff != c.wantEff {
			t.Errorf("(%q,%s,%s): effective=%q want %q", c.requested, c.mode, c.udp, eff, c.wantEff)
		}
		if (reason != "") != c.wantReason {
			t.Errorf("(%q,%s,%s): reason=%q wantReason=%v", c.requested, c.mode, c.udp, reason, c.wantReason)
		}
	}
}

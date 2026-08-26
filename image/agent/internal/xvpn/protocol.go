package xvpn

import "detour/agent/internal/state"

// udpProtocols — протоколы, требующие UDP у uplink'а.
var udpProtocols = map[string]bool{
	"auto":         true, // auto предпочитает lightway_udp
	"lightway_udp": true,
	"openvpn_udp":  true,
	"wireguard":    true,
}

// EffectiveProtocol вычисляет протокол, который реально передаётся демону
// (спека expressvpn-control, Protocol selection with effective fallback):
// пользовательский выбор сохраняется в requested, effective отражает
// ограничения uplink'а, reason объясняет замену (пусто, если замены нет).
func EffectiveProtocol(requested, uplinkMode string, udpSupported state.TriState) (effective, reason string) {
	if requested == "" {
		requested = "auto"
	}
	if uplinkMode != "socks5" {
		return requested, ""
	}
	if !udpProtocols[requested] {
		return requested, ""
	}
	switch udpSupported {
	case state.TriTrue:
		return requested, ""
	case state.TriFalse:
		return "lightway_tcp", "uplink has no UDP"
	default:
		return "lightway_tcp", "uplink UDP support unknown"
	}
}

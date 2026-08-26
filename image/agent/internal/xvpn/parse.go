package xvpn

import (
	"strings"

	"detour/agent/internal/state"
)

// Форматы выводов expressvpnctl 14.2 — plain text, JSON-режима нет
// (golden-данные: testdata/, собраны спайком S3).

// ParseConnectionState отображает вывод `get connectionstate` в состояние API.
func ParseConnectionState(out string) state.Connection {
	switch strings.TrimSpace(out) {
	case "Disconnected", "Disconnecting":
		return state.ConnDisconnected
	case "Connecting":
		return state.ConnConnecting
	case "Connected":
		return state.ConnConnected
	case "Interrupted", "Reconnecting", "DisconnectingToReconnect":
		return state.ConnReconnecting
	default:
		return state.ConnDisconnected
	}
}

// Status — разбор многострочного `expressvpnctl status`.
type Status struct {
	LoggedIn    bool
	NetworkLock string
	SplitTunnel string
	// Lines — прочие строки статуса как есть (для диагностики).
	Lines []string
}

func ParseStatus(out string) Status {
	st := Status{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case line == "Not logged in.":
			st.LoggedIn = false
		case strings.HasPrefix(line, "Network Lock:"):
			st.NetworkLock = strings.TrimSpace(strings.TrimPrefix(line, "Network Lock:"))
		case strings.HasPrefix(line, "Split Tunnel:"):
			st.SplitTunnel = strings.TrimSpace(strings.TrimPrefix(line, "Split Tunnel:"))
		default:
			// До входа первая строка — "Not logged in."; после входа её нет.
			st.LoggedIn = true
			st.Lines = append(st.Lines, line)
		}
	}
	return st
}

// ParseRegions — `get regions`: одна локация на строку. До входа в аккаунт
// список состоит из единственного "smart".
func ParseRegions(out string) []string {
	var regions []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			regions = append(regions, line)
		}
	}
	return regions
}

// ParseSmart — `get smart`: id региона или "N/A", когда демону он неизвестен.
func ParseSmart(out string) string {
	s := strings.TrimSpace(out)
	if s == "N/A" {
		return ""
	}
	return s
}

// ParseValue — однострочные `get <type>` (protocol, pubip, vpnip, region, bool-поля).
// "Unknown" нормализуется в пустую строку.
func ParseValue(out string) string {
	s := strings.TrimSpace(out)
	if s == "Unknown" {
		return ""
	}
	return s
}

// Протоколы: имена CLI (`lightwayudp`) ↔ имена API (`lightway_udp`).
var cliToAPIProtocol = map[string]string{
	"auto":        "auto",
	"lightwayudp": "lightway_udp",
	"lightwaytcp": "lightway_tcp",
	"openvpnudp":  "openvpn_udp",
	"openvpntcp":  "openvpn_tcp",
	"wireguard":   "wireguard",
}

var apiToCLIProtocol = func() map[string]string {
	m := make(map[string]string, len(cliToAPIProtocol))
	for cli, api := range cliToAPIProtocol {
		m[api] = cli
	}
	return m
}()

func ProtocolToAPI(cli string) string {
	if api, ok := cliToAPIProtocol[strings.TrimSpace(cli)]; ok {
		return api
	}
	return strings.TrimSpace(cli)
}

func ProtocolToCLI(api string) string {
	if cli, ok := apiToCLIProtocol[api]; ok {
		return cli
	}
	return api
}

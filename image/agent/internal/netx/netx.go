// Package netx — общие сетевые утилиты агента.
package netx

import (
	"context"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
)

// resolvConfPath — переменная ради тестов.
var resolvConfPath = "/etc/resolv.conf"

// defaultContainerDNS — embedded DNS пользовательских Docker-сетей (compose).
const defaultContainerDNS = "127.0.0.11"

var (
	pinMu        sync.Mutex
	containerDNS string
)

// PinContainerDNS фиксирует резолвер контейнера по текущему /etc/resolv.conf
// и возвращает его. main вызывает её на старте агента — до того, как uplink
// в socks5-режиме перепишет resolv.conf под перехват sing-box: собственные
// резолвы агента (имя прокси-эндпоинта — единственный разрешённый прямой
// резолв, D5 контур 3) должны ходить в резолвер Docker, а не в DoH через
// прокси.
//
// ignore — адреса, которые не могут быть резолвером контейнера (адрес
// перехвата uplink.TunPeer). Если resolv.conf на старте уже перенаправлен
// (агент поднялся после аварии, не успев вернуть файл), такие nameserver'ы
// пропускаются, а без других берётся embedded DNS Docker.
//
// Пинуется один раз: повторные вызовы возвращают уже зафиксированный адрес.
func PinContainerDNS(ignore ...string) string {
	pinMu.Lock()
	defer pinMu.Unlock()
	if containerDNS == "" {
		raw, _ := os.ReadFile(resolvConfPath)
		containerDNS = pickNameserver(raw, ignore)
	}
	return containerDNS
}

// ContainerDNS возвращает резолвер, зафиксированный PinContainerDNS. Файл
// здесь не читается: ленивое чтение при первом резолве имени могло застать
// resolv.conf, уже переписанный uplink'ом. До пина — embedded DNS Docker.
func ContainerDNS() string {
	pinMu.Lock()
	defer pinMu.Unlock()
	if containerDNS == "" {
		return defaultContainerDNS
	}
	return containerDNS
}

// pickNameserver возвращает первый nameserver из содержимого resolv.conf,
// не входящий в ignore, иначе — embedded DNS Docker.
func pickNameserver(raw []byte, ignore []string) string {
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" && !slices.Contains(ignore, f[1]) {
			return f[1]
		}
	}
	return defaultContainerDNS
}

// ResolveIPv4 резолвит имя в IPv4 через резолвер контейнера (пинованный
// адрес, не зависящий от текущего содержимого /etc/resolv.conf).
func ResolveIPv4(ctx context.Context, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	res := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(ContainerDNS(), "53"))
		},
	}
	ips, err := res.LookupIP(ctx, "ip4", host)
	if err != nil {
		return "", err
	}
	if len(ips) == 0 {
		return "", fmt.Errorf("no IPv4 addresses for %s", host)
	}
	return ips[0].String(), nil
}

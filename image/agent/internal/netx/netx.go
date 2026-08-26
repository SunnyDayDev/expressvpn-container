// Package netx — общие сетевые утилиты агента.
package netx

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
)

var (
	once         sync.Once
	containerDNS string
)

// ContainerDNS возвращает адрес резолвера контейнера, каким он был на старте
// агента (до любых подмен /etc/resolv.conf). В socks5-режиме resolv.conf
// переписывается под перехват sing-box'ом, но собственные резолвы агента
// (имя прокси-эндпоинта — единственный разрешённый прямой резолв, D5) должны
// продолжать ходить в резолвер Docker.
func ContainerDNS() string {
	once.Do(func() {
		containerDNS = "127.0.0.11" // embedded DNS Docker-сетей compose
		if raw, err := os.ReadFile("/etc/resolv.conf"); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				f := strings.Fields(strings.TrimSpace(line))
				if len(f) >= 2 && f[0] == "nameserver" {
					containerDNS = f[1]
					break
				}
			}
		}
	})
	return containerDNS
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

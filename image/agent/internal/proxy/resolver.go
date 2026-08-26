package proxy

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// TunnelResolver — remote DNS для клиентов прокси: домены резолвятся через
// резолвер, который настроил демон ExpressVPN (адрес — из /etc/resolv.conf,
// его переписывает демон при подключении), сокетами с SO_MARK — то есть
// только через туннель. Резолв на хосте не происходит (спека proxy-ingress).
type TunnelResolver struct {
	dialer Dialer

	mu       sync.Mutex
	cachedNS string
	readAt   time.Time
}

func NewTunnelResolver(d Dialer) *TunnelResolver {
	return &TunnelResolver{dialer: d}
}

func (r *TunnelResolver) Resolve(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip, nil
	}
	ns, err := r.nameserver()
	if err != nil {
		return nil, err
	}
	res := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return r.dialer.DialContext(ctx, network, net.JoinHostPort(ns, "53"))
		},
	}
	ips, err := res.LookupIP(ctx, "ip4", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no A records for %s", host)
	}
	return ips[0], nil
}

// Nameserver — текущий адрес резолвера туннеля (для отчёта self-check).
func (r *TunnelResolver) Nameserver() string {
	ns, err := r.nameserver()
	if err != nil {
		return ""
	}
	return ns
}

// nameserver читает первый nameserver из /etc/resolv.conf (кеш 5 секунд —
// демон переписывает файл при connect/disconnect).
func (r *TunnelResolver) nameserver() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cachedNS != "" && time.Since(r.readAt) < 5*time.Second {
		return r.cachedNS, nil
	}
	raw, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 && fields[0] == "nameserver" {
			r.cachedNS = fields[1]
			r.readAt = time.Now()
			return r.cachedNS, nil
		}
	}
	return "", fmt.Errorf("no nameserver in /etc/resolv.conf")
}

// Package selfcheck — действие selfcheck (спека agent-api): IP/страна через
// входящий прокси, IP uplink'а, DNS через прокси и вердикт.
package selfcheck

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"strconv"

	"detour/agent/internal/config"
	"detour/agent/internal/netx"
	"detour/agent/internal/state"
)

// ipEndpoints — сервисы «какой у меня IP»: сначала JSON (дают страну), затем
// plain-text запасные (некоторые сервисы блокируются на отдельных маршрутах
// или лимитируют VPN-адреса). Первый ответивший побеждает; 2 круга.
var ipEndpoints = []struct {
	url  string
	json bool
}{
	{"https://api.country.is", true},
	{"https://ipinfo.io/json", true},
	{"https://ifconfig.me/ip", false},
	{"https://icanhazip.com", false},
}

type Checker struct {
	st  *state.Store
	cfg *config.Store
	// proxyAddr — адрес собственного SOCKS5 изнутри контейнера.
	proxyAddr string
	logger    *slog.Logger
	// resolveViaTunnel — remote DNS (тот же путь, что у клиентов прокси).
	resolveViaTunnel func(ctx context.Context, host string) (net.IP, error)
	// tunnelNS — текущий адрес резолвера туннеля (для отчёта).
	tunnelNS func() string
}

func New(st *state.Store, cfg *config.Store, proxyAddr string, logger *slog.Logger,
	resolve func(ctx context.Context, host string) (net.IP, error), tunnelNS func() string) *Checker {
	return &Checker{
		st: st, cfg: cfg, proxyAddr: proxyAddr,
		logger:           logger.With("component", "agent"),
		resolveViaTunnel: resolve,
		tunnelNS:         tunnelNS,
	}
}

// Run выполняет проверку и публикует результат в state.
func (c *Checker) Run(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()

	res := &state.Selfcheck{At: time.Now().UTC()}
	var reasons []string

	// VPN отключён намеренно — сразу честный вердикт без ожиданий.
	if c.st.Get().Desired.Connection != state.DesiredConnected {
		res.Verdict = "fail"
		res.Reasons = []string{"tunnel_down"}
		c.st.Update(func(st *state.State) { st.Selfcheck = res })
		return errors.New("selfcheck failed: tunnel_down")
	}

	// Сначала дожидаемся устойчивого состояния: сразу после переключения
	// uplink'а или реконнекта замеры бессмысленны (прокси fail-closed).
	if !c.waitSettled(ctx, 45*time.Second) {
		s := c.st.Get()
		if s.Uplink.Status != state.UplinkUp {
			reasons = append(reasons, "uplink_down")
		}
		if s.ExpressVPN.Connection != state.ConnConnected {
			reasons = append(reasons, "tunnel_down")
		}
		res.Verdict = "fail"
		res.Reasons = reasons
		c.st.Update(func(st *state.State) { st.Selfcheck = res })
		return fmt.Errorf("selfcheck failed: %s", strings.Join(reasons, ", "))
	}

	// (a) IP/страна через входящий прокси — полный путь клиента.
	proxyIP, proxyCountry, proxyErr := c.ipViaProxy(ctx)
	if proxyErr != nil {
		// Замер мог попасть в переходное окно (переключение uplink'а ещё не
		// успело отразиться в state) — дождаться стабилизации и повторить.
		if c.waitSettled(ctx, 45*time.Second) {
			proxyIP, proxyCountry, proxyErr = c.ipViaProxy(ctx)
		}
	}
	if proxyErr != nil {
		if c.st.Get().ExpressVPN.Connection != state.ConnConnected {
			reasons = append(reasons, "tunnel_down")
		} else {
			reasons = append(reasons, "proxy_check_failed")
		}
	} else {
		res.ProxyIP, res.ProxyCountry = proxyIP, proxyCountry
	}

	// (b) IP uplink'а — путь ДО туннеля: сокеты привязываются к eth0 (мимо
	// маршрутов 0.0.0.0/1 подключённого VPN), в режиме socks5 — через
	// пользовательский прокси. В host-режиме при подключённом VPN замер
	// невозможен: демон ExpressVPN сам режет не-туннельный egress
	// (blockAll «enabled when connected», S2) — пропускаем без предупреждения.
	if c.cfg.Get().Uplink.Mode == "socks5" {
		uplinkIP, _, uplinkErr := fetchIP(ctx, c.uplinkTransport())
		if uplinkErr != nil {
			reasons = append(reasons, "uplink_ip_unknown")
		} else {
			res.UplinkIP = uplinkIP
		}
	}

	// (c) DNS через прокси.
	if c.resolveViaTunnel != nil {
		if _, err := c.resolveViaTunnel(ctx, "example.com"); err != nil {
			if proxyErr == nil {
				reasons = append(reasons, "dns_check_failed")
			}
		} else if c.tunnelNS != nil {
			res.DNS = []string{c.tunnelNS()}
		}
	}

	// Вердикт.
	switch {
	case proxyErr != nil:
		res.Verdict = "fail"
	case res.UplinkIP != "" && res.ProxyIP == res.UplinkIP:
		res.Verdict = "fail"
		reasons = append(reasons, "same_ip")
	case len(reasons) > 0:
		res.Verdict = "warning"
	default:
		res.Verdict = "ok"
	}
	res.Reasons = reasons

	c.st.Update(func(s *state.State) { s.Selfcheck = res })
	c.logger.Info("selfcheck finished", "verdict", res.Verdict, "reasons", strings.Join(reasons, ","))
	if res.Verdict == "fail" {
		return fmt.Errorf("selfcheck failed: %s", strings.Join(reasons, ", "))
	}
	return nil
}

// waitSettled ждёт устойчивого «connected + uplink up» (3 замера подряд с
// шагом в секунду), не дольше timeout. false — состояние так и не устоялось.
func (c *Checker) waitSettled(ctx context.Context, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	stable := 0
	for {
		s := c.st.Get()
		if s.ExpressVPN.Connection == state.ConnConnected && s.Uplink.Status == state.UplinkUp {
			stable++
			if stable >= 3 {
				return true
			}
		} else {
			stable = 0
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
	}
}

// ipViaProxy ходит наружу через собственный SOCKS5 (127.0.0.1:1080).
func (c *Checker) ipViaProxy(ctx context.Context) (ip, country string, err error) {
	auth := c.cfg.Get().Proxy.Auth
	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return socksDialVia(ctx, &net.Dialer{Timeout: 5 * time.Second}, c.proxyAddr, auth, addr)
		},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	return fetchIP(ctx, tr)
}

// uplinkTransport — транспорт по uplink-пути (мимо туннеля).
func (c *Checker) uplinkTransport() http.RoundTripper {
	u := c.cfg.Get().Uplink
	if u.Mode == "socks5" && u.Socks5.Host != "" {
		return &http.Transport{
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				pip, err := netx.ResolveIPv4(ctx, u.Socks5.Host)
				if err != nil {
					return nil, err
				}
				var auth *config.ProxyAuth
				if u.Socks5.Username != "" {
					auth = &config.ProxyAuth{Username: u.Socks5.Username, Password: u.Socks5.Password}
				}
				d := eth0Dialer()
				d.Timeout = 5 * time.Second
				return socksDialVia(ctx, d, net.JoinHostPort(pip, strconv.Itoa(u.Socks5.Port)), auth, addr)
			},
			DisableKeepAlives: true,
		}
	}
	return &http.Transport{DialContext: dialViaEth0, DisableKeepAlives: true}
}

// fetchIP спрашивает публичный IP у первого ответившего сервиса (2 круга).
func fetchIP(ctx context.Context, tr http.RoundTripper) (ip, country string, err error) {
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	var lastErr error
	for round := 0; round < 2; round++ {
		for _, ep := range ipEndpoints {
			if ctx.Err() != nil {
				return "", "", ctx.Err()
			}
			req, rerr := http.NewRequestWithContext(ctx, "GET", ep.url, nil)
			if rerr != nil {
				lastErr = rerr
				continue
			}
			resp, rerr := client.Do(req)
			if rerr != nil {
				lastErr = rerr
				continue
			}
			raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if rerr != nil || resp.StatusCode != http.StatusOK {
				lastErr = fmt.Errorf("%s: status %d", ep.url, resp.StatusCode)
				continue
			}
			if ep.json {
				var body struct {
					IP      string `json:"ip"`
					Country string `json:"country"`
				}
				if err := json.Unmarshal(raw, &body); err == nil && body.IP != "" {
					return body.IP, body.Country, nil
				}
				lastErr = fmt.Errorf("%s: unparsable body", ep.url)
				continue
			}
			if cand := strings.TrimSpace(string(raw)); net.ParseIP(cand) != nil {
				return cand, "", nil
			}
			lastErr = fmt.Errorf("%s: not an IP", ep.url)
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no ip endpoints configured")
	}
	return "", "", lastErr
}

// socksDialVia — минимальный SOCKS5 CONNECT-клиент (NO AUTH / RFC 1929)
// поверх произвольного базового диалера.
func socksDialVia(ctx context.Context, d *net.Dialer, proxyAddr string, auth *config.ProxyAuth, target string) (net.Conn, error) {
	conn, err := d.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	conn.SetDeadline(time.Now().Add(15 * time.Second))

	method := byte(0x00)
	if auth != nil {
		method = 0x02
	}
	if _, err := conn.Write([]byte{0x05, 0x01, method}); err != nil {
		return nil, err
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return nil, err
	}
	if resp[1] != method {
		return nil, fmt.Errorf("proxy rejected auth method (0x%02x)", resp[1])
	}
	if auth != nil {
		req := []byte{0x01, byte(len(auth.Username))}
		req = append(req, auth.Username...)
		req = append(req, byte(len(auth.Password)))
		req = append(req, auth.Password...)
		if _, err := conn.Write(req); err != nil {
			return nil, err
		}
		st := make([]byte, 2)
		if _, err := io.ReadFull(conn, st); err != nil {
			return nil, err
		}
		if st[1] != 0x00 {
			return nil, errors.New("proxy auth failed")
		}
	}

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		req = append(req, 0x01)
		req = append(req, ip.To4()...)
	} else {
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := conn.Write(req); err != nil {
		return nil, err
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return nil, err
	}
	if reply[1] != 0x00 {
		return nil, fmt.Errorf("proxy CONNECT failed (rep=0x%02x)", reply[1])
	}
	// Дочитываем BND.ADDR.
	switch reply[3] {
	case 0x01:
		_, err = io.ReadFull(conn, make([]byte, 4+2))
	case 0x04:
		_, err = io.ReadFull(conn, make([]byte, 16+2))
	case 0x03:
		l := make([]byte, 1)
		if _, err = io.ReadFull(conn, l); err == nil {
			_, err = io.ReadFull(conn, make([]byte, int(l[0])+2))
		}
	}
	if err != nil {
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	ok = true
	return conn, nil
}

package uplink

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"strconv"
	"time"

	"detour/agent/internal/config"
)

// probeTimeout — спека uplink-routing: датаграмма должна пройти за 3 секунды.
const probeTimeout = 3 * time.Second

// ProbeUDP проверяет поддержку UDP у SOCKS5-прокси: UDP ASSOCIATE + реальная
// DNS-датаграмма к 1.1.1.1 через relay (D6). true — датаграмма прошла и
// ответ вернулся.
func ProbeUDP(ctx context.Context, proxyIP string, cfg config.Socks5, logger *slog.Logger) bool {
	deadline := time.Now().Add(probeTimeout)
	d := net.Dialer{Deadline: deadline}
	ctrl, err := d.DialContext(ctx, "tcp", net.JoinHostPort(proxyIP, strconv.Itoa(cfg.Port)))
	if err != nil {
		logger.Debug("probe: tcp connect failed", "error", err)
		return false
	}
	defer ctrl.Close()
	ctrl.SetDeadline(deadline)

	// Хендшейк.
	method := byte(0x00)
	if cfg.Username != "" {
		method = 0x02
	}
	if _, err := ctrl.Write([]byte{0x05, 0x01, method}); err != nil {
		return false
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(ctrl, resp); err != nil || resp[1] != method {
		logger.Debug("probe: method rejected")
		return false
	}
	if method == 0x02 {
		req := []byte{0x01, byte(len(cfg.Username))}
		req = append(req, cfg.Username...)
		req = append(req, byte(len(cfg.Password)))
		req = append(req, cfg.Password...)
		if _, err := ctrl.Write(req); err != nil {
			return false
		}
		st := make([]byte, 2)
		if _, err := io.ReadFull(ctrl, st); err != nil || st[1] != 0x00 {
			return false
		}
	}

	// UDP ASSOCIATE.
	if _, err := ctrl.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return false
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(ctrl, reply); err != nil || reply[1] != 0x00 {
		logger.Debug("probe: UDP ASSOCIATE rejected")
		return false
	}
	relayIP := net.IP(reply[4:8])
	relayPort := int(binary.BigEndian.Uint16(reply[8:10]))
	// 0.0.0.0/127.x в BND.ADDR — обычное дело (xray, dante): relay доступен
	// по адресу самого прокси.
	if relayIP.IsUnspecified() || relayIP.IsLoopback() {
		relayIP = net.ParseIP(proxyIP)
	}

	// Реальная датаграмма: DNS-запрос A example.com к 1.1.1.1 через relay.
	udp, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: relayIP, Port: relayPort})
	if err != nil {
		return false
	}
	defer udp.Close()
	udp.SetDeadline(deadline)

	pkt := []byte{0, 0, 0, 0x01, 1, 1, 1, 1, 0, 53}
	pkt = append(pkt, dnsQuery("example.com")...)
	if _, err := udp.Write(pkt); err != nil {
		return false
	}
	buf := make([]byte, 1500)
	n, err := udp.Read(buf)
	if err != nil || n <= 10 {
		logger.Debug("probe: no datagram response", "error", err)
		return false
	}
	return true
}

// dnsQuery — минимальный DNS-запрос A <name> (RD=1).
func dnsQuery(name string) []byte {
	q := []byte{0xd7, 0x12, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			q = append(q, byte(i-start))
			q = append(q, name[start:i]...)
			start = i + 1
		}
	}
	q = append(q, 0x00)       // конец имени
	q = append(q, 0x00, 0x01) // QTYPE A
	q = append(q, 0x00, 0x01) // QCLASS IN
	return q
}

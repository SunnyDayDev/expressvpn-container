package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"
)

// UDP ASSOCIATE (RFC 1928 §7).
//
// Relay слушает ФИКСИРОВАННЫЙ порт (равный TCP-порту прокси): динамические
// порты Docker не публикует, и клиент с хоста не смог бы прислать датаграмму.
// Один общий сокет обслуживает все ассоциации. Клиенты за NAT Docker приходят
// с непредсказуемого адреса, поэтому ассоциация создаётся по первой датаграмме
// с валидным SOCKS5-заголовком и живёт, пока идёт обмен (TTL 5 минут).
type udpRelay struct {
	srv  *Server
	conn net.PacketConn

	mu    sync.Mutex
	flows map[string]*udpFlow // адрес клиента → поток
}

type udpFlow struct {
	client  *net.UDPAddr
	targets map[string][]byte // адрес цели → SOCKS5-заголовок для обратной упаковки
	last    time.Time
}

const udpFlowIdle = 5 * time.Minute

func newUDPRelayOn(s *Server, addr string) (*udpRelay, error) {
	conn, err := s.dialer.ListenUDP(addr)
	if err != nil {
		return nil, err
	}
	return &udpRelay{srv: s, conn: conn, flows: map[string]*udpFlow{}}, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func (r *udpRelay) port() int { return r.conn.LocalAddr().(*net.UDPAddr).Port }

// run — общий цикл relay.
func (r *udpRelay) run(ctx context.Context) {
	go func() {
		<-ctx.Done()
		r.conn.Close()
	}()
	buf := make([]byte, 64*1024)
	lastCleanup := time.Now()
	for {
		r.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, from, err := r.conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if time.Since(lastCleanup) > time.Minute {
				r.expire()
				lastCleanup = time.Now()
			}
			continue
		}
		fromUDP := from.(*net.UDPAddr)
		key := fromUDP.String()

		r.mu.Lock()
		flow, isClient := r.flows[key]
		if !isClient {
			// Ответ цели? Ищем поток, знающий этот адрес.
			var hdr []byte
			for _, cand := range r.flows {
				if h, ok := cand.targets[key]; ok {
					flow, hdr = cand, h
					break
				}
			}
			r.mu.Unlock()
			if flow != nil {
				out := append(append(make([]byte, 0, len(hdr)+n), hdr...), buf[:n]...)
				r.conn.WriteTo(out, flow.client)
				r.srv.bytesOut.Add(int64(n))
				r.srv.dirty.Store(true)
				continue
			}
			// Неизвестный отправитель: возможно, новый клиент.
			if !r.srv.gate() {
				continue // fail closed
			}
			hdrLen, target, ok := r.srv.parseUDPHeader(ctx, buf[:n])
			if !ok {
				r.srv.logger.Debug("udp relay: unparsable datagram from unknown sender", "from", key, "len", n)
				continue
			}
			flow = &udpFlow{client: fromUDP, targets: map[string][]byte{}, last: time.Now()}
			r.mu.Lock()
			r.flows[key] = flow
			flow.targets[target.String()] = packUDPHeader(target)
			r.mu.Unlock()
			if _, werr := r.conn.WriteTo(buf[hdrLen:n], target); werr != nil {
				r.srv.logger.Debug("udp relay: send to target failed", "target", target.String(), "error", werr)
			}
			r.srv.logger.Debug("udp relay: new flow", "client", key, "target", target.String())
			r.srv.bytesIn.Add(int64(n - hdrLen))
			r.srv.dirty.Store(true)
			continue
		}
		r.mu.Unlock()

		// Датаграмма известного клиента.
		if !r.srv.gate() {
			continue // fail closed
		}
		hdrLen, target, ok := r.srv.parseUDPHeader(ctx, buf[:n])
		if !ok {
			continue
		}
		r.mu.Lock()
		flow.last = time.Now()
		if _, seen := flow.targets[target.String()]; !seen {
			flow.targets[target.String()] = packUDPHeader(target)
		}
		r.mu.Unlock()
		r.conn.WriteTo(buf[hdrLen:n], target)
		r.srv.bytesIn.Add(int64(n - hdrLen))
		r.srv.dirty.Store(true)
	}
}

func (r *udpRelay) expire() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, f := range r.flows {
		if time.Since(f.last) > udpFlowIdle {
			delete(r.flows, k)
		}
	}
}

// handleUDPAssociate: сообщает клиенту фиксированный relay-порт; сессия живёт,
// пока открыт TCP-контроль (сам relay-поток управляется TTL — за NAT Docker
// адрес клиента заранее неизвестен и к TCP-сессии не привязывается).
func (s *Server) handleUDPAssociate(ctx context.Context, conn net.Conn) {
	if !s.gate() {
		writeReply(conn, repNetworkUnreachable, nil)
		return
	}
	if s.udp == nil {
		writeReply(conn, repGeneralFailure, nil)
		return
	}
	// BND.ADDR = 0.0.0.0: сервер за NAT Docker не знает опубликованного
	// адреса; клиенты (curl и большинство библиотек) в этом случае шлют
	// датаграммы на адрес контрольного соединения. BND.PORT — опубликованный
	// порт хоста (SOCKS_PORT), а не контейнерный: снаружи существует он.
	port := s.publishedPort
	if port == 0 {
		port = s.udp.port()
	}
	if err := writeReply(conn, repSuccess, &net.UDPAddr{IP: net.IPv4zero, Port: port}); err != nil {
		return
	}
	io.Copy(io.Discard, conn)
}

// parseUDPHeader разбирает заголовок RFC 1928 §7; FRAG≠0 не поддерживается.
func (s *Server) parseUDPHeader(ctx context.Context, pkt []byte) (hdrLen int, target *net.UDPAddr, ok bool) {
	if len(pkt) < 4 || pkt[2] != 0x00 {
		return 0, nil, false
	}
	atyp := pkt[3]
	var a addrSpec
	var consumed int
	switch atyp {
	case atypIPv4:
		if len(pkt) < 4+6 {
			return 0, nil, false
		}
		a.IP = net.IP(pkt[4:8])
		a.Port = int(binary.BigEndian.Uint16(pkt[8:10]))
		consumed = 10
	case atypIPv6:
		if len(pkt) < 4+18 {
			return 0, nil, false
		}
		a.IP = net.IP(pkt[4:20])
		a.Port = int(binary.BigEndian.Uint16(pkt[20:22]))
		consumed = 22
	case atypDomain:
		if len(pkt) < 5 {
			return 0, nil, false
		}
		l := int(pkt[4])
		if len(pkt) < 5+l+2 {
			return 0, nil, false
		}
		a.Host = string(pkt[5 : 5+l])
		a.Port = int(binary.BigEndian.Uint16(pkt[5+l : 7+l]))
		consumed = 7 + l
	default:
		return 0, nil, false
	}
	if a.Host != "" {
		ip, err := s.resolver(ctx, a.Host)
		if err != nil {
			return 0, nil, false
		}
		a.IP = ip
	}
	return consumed, &net.UDPAddr{IP: a.IP, Port: a.Port}, true
}

func packUDPHeader(from *net.UDPAddr) []byte {
	ip4 := from.IP.To4()
	if ip4 != nil {
		hdr := make([]byte, 0, 10)
		hdr = append(hdr, 0, 0, 0, atypIPv4)
		hdr = append(hdr, ip4...)
		return binary.BigEndian.AppendUint16(hdr, uint16(from.Port))
	}
	hdr := make([]byte, 0, 22)
	hdr = append(hdr, 0, 0, 0, atypIPv6)
	hdr = append(hdr, from.IP.To16()...)
	return binary.BigEndian.AppendUint16(hdr, uint16(from.Port))
}

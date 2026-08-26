package proxy

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"detour/agent/internal/config"
	"detour/agent/internal/state"
)

// Dialer — исходящие соединения от имени клиентов (в проде — с SO_MARK).
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	// ListenUDP открывает relay-сокет UDP ASSOCIATE на указанном адресе
	// (фиксированный порт — динамические Docker на хост не публикует).
	ListenUDP(addr string) (net.PacketConn, error)
}

// ResolveFunc — remote DNS: резолв доменов из SOCKS5-запросов через резолвер
// туннеля (задача 8.2).
type ResolveFunc func(ctx context.Context, host string) (net.IP, error)

// Server — SOCKS5-сервер на фиксированном порту контейнера.
type Server struct {
	addr     string
	dialer   Dialer
	resolver ResolveFunc
	// gate: true — туннель подключён, можно проксировать; false — fail closed
	// (REP=0x03, спека proxy-ingress).
	gate func() bool
	// authFn возвращает текущие настройки RFC 1929 (nil — NO AUTH).
	authFn func() *config.ProxyAuth
	st            *state.Store
	logger        *slog.Logger
	publishedPort int

	active   atomic.Int64
	bytesIn  atomic.Int64 // от клиента к цели
	bytesOut atomic.Int64 // от цели к клиенту
	dirty    atomic.Bool

	mu  sync.Mutex
	ln  net.Listener
	udp *udpRelay
}

type Options struct {
	Addr     string
	Dialer   Dialer
	Resolver ResolveFunc
	Gate     func() bool
	Auth     func() *config.ProxyAuth
	State    *state.Store
	Logger   *slog.Logger
	// PublishedPort — порт, под которым SOCKS5 опубликован на хосте
	// (SOCKS_PORT из .env). Возвращается клиентам в BND.PORT ответа
	// UDP ASSOCIATE: контейнерный порт снаружи не существует, если
	// публикация его переименовала (например, 1081 → 1080).
	PublishedPort int
}

func New(o Options) *Server {
	return &Server{
		addr:          o.Addr,
		dialer:        o.Dialer,
		resolver:      o.Resolver,
		gate:          o.Gate,
		authFn:        o.Auth,
		st:            o.State,
		logger:        o.Logger.With("component", "proxy"),
		publishedPort: o.PublishedPort,
	}
}

// Run слушает порт и обслуживает клиентов до отмены контекста.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	// UDP-relay на том же порту, что и TCP (фиксированный — для публикации).
	tcpPort := ln.Addr().(*net.TCPAddr).Port
	host, _, _ := net.SplitHostPort(s.addr)
	if host == "" {
		host = "0.0.0.0"
	}
	relay, err := newUDPRelayOn(s, net.JoinHostPort(host, itoa(tcpPort)))
	if err != nil {
		s.logger.Warn("udp relay unavailable", "error", err)
	} else {
		s.udp = relay
		go relay.run(ctx)
	}
	s.logger.Info("socks5 listening", "addr", s.addr, "udpRelay", s.udp != nil)
	s.st.Update(func(st *state.State) {
		st.Proxy.Listen = s.addr
		st.Proxy.Status = "running"
	})

	go s.publishStats(ctx)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				s.st.Update(func(st *state.State) { st.Proxy.Status = "stopped" })
				return nil
			}
			s.logger.Warn("accept failed", "error", err)
			continue
		}
		go s.handle(ctx, conn)
	}
}

// publishStats переносит счётчики в state не чаще раза в секунду и только
// при изменениях (SSE-событие ≤1 c, спека agent-api).
func (s *Server) publishStats(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !s.dirty.Swap(false) {
			continue
		}
		in, out, act := s.bytesIn.Load(), s.bytesOut.Load(), s.active.Load()
		s.st.Update(func(st *state.State) {
			st.Proxy.BytesIn = in
			st.Proxy.BytesOut = out
			st.Proxy.ActiveConns = act
		})
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := s.readHandshake(conn); err != nil {
		s.logger.Debug("handshake rejected", "error", err)
		return
	}
	cmd, addr, err := readRequest(conn)
	if err != nil {
		if err == errUnsupportedAddr {
			writeReply(conn, repAddrUnsupported, nil)
		}
		return
	}
	conn.SetDeadline(time.Time{})

	switch cmd {
	case cmdConnect:
		s.handleConnect(ctx, conn, addr)
	case cmdUDPAssociate:
		s.handleUDPAssociate(ctx, conn)
	default:
		writeReply(conn, repCommandUnsupported, nil)
	}
}

func (s *Server) handleConnect(ctx context.Context, conn net.Conn, addr addrSpec) {
	// Fail closed: туннель не подключён → Network unreachable.
	if !s.gate() {
		writeReply(conn, repNetworkUnreachable, nil)
		return
	}
	target, err := s.resolve(ctx, addr)
	if err != nil {
		writeReply(conn, repHostUnreachable, nil)
		return
	}
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	out, err := s.dialer.DialContext(dctx, "tcp", target)
	cancel()
	if err != nil {
		writeReply(conn, repNetworkUnreachable, nil)
		return
	}
	defer out.Close()
	if err := writeReply(conn, repSuccess, out.LocalAddr()); err != nil {
		return
	}

	s.active.Add(1)
	s.dirty.Store(true)
	defer func() {
		s.active.Add(-1)
		s.dirty.Store(true)
	}()

	done := make(chan struct{}, 2)
	copyCount := func(dst io.Writer, src io.Reader, counter *atomic.Int64) {
		n, _ := io.Copy(dst, src)
		counter.Add(n)
		s.dirty.Store(true)
		if c, ok := dst.(interface{ CloseWrite() error }); ok {
			c.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyCount(out, conn, &s.bytesIn)
	go copyCount(conn, out, &s.bytesOut)
	<-done
	<-done
}

func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.addr
}

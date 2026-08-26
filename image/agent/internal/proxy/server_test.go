package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"detour/agent/internal/config"
	"detour/agent/internal/state"
)

// plainDialer — без SO_MARK, для тестов.
type plainDialer struct{}

func (plainDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, address)
}

func (plainDialer) ListenUDP(addr string) (net.PacketConn, error) {
	return net.ListenPacket("udp4", addr)
}

type testProxy struct {
	srv    *Server
	addr   string
	gate   *atomic.Bool
	auth   *atomic.Pointer[config.ProxyAuth]
	cancel context.CancelFunc
}

func startProxy(t *testing.T) *testProxy {
	t.Helper()
	gate := &atomic.Bool{}
	gate.Store(true)
	auth := &atomic.Pointer[config.ProxyAuth]{}
	st := state.NewStore(state.Initial("test"))
	srv := New(Options{
		Addr:     "127.0.0.1:0",
		Dialer:   plainDialer{},
		Resolver: func(_ context.Context, host string) (net.IP, error) { return net.ParseIP("127.0.0.1"), nil },
		Gate:     gate.Load,
		Auth:     auth.Load,
		State:    st,
		Logger:   slog.New(slog.DiscardHandler),
	})
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Run(ctx)
	t.Cleanup(cancel)
	// Ждём адреса слушателя.
	deadline := time.Now().Add(2 * time.Second)
	for srv.Addr() == "127.0.0.1:0" {
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return &testProxy{srv: srv, addr: srv.Addr(), gate: gate, auth: auth, cancel: cancel}
}

// echoTCP поднимает TCP-эхо и возвращает адрес.
func echoTCP(t *testing.T) *net.TCPAddr {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	return ln.Addr().(*net.TCPAddr)
}

// socksConnect выполняет хендшейк и CONNECT, возвращает REP и соединение.
func socksConnect(t *testing.T, proxyAddr string, target *net.TCPAddr, creds []byte) (byte, net.Conn) {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	method := byte(0x00)
	if creds != nil {
		method = 0x02
	}
	conn.Write([]byte{0x05, 0x01, method})
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("handshake read: %v", err)
	}
	if resp[1] == 0xFF {
		return 0xFF, conn
	}
	if creds != nil {
		conn.Write(creds)
		st := make([]byte, 2)
		if _, err := io.ReadFull(conn, st); err != nil {
			t.Fatalf("auth read: %v", err)
		}
		if st[1] != 0x00 {
			return 0xFE, conn
		}
	}
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, target.IP.To4()...)
	req = binary.BigEndian.AppendUint16(req, uint16(target.Port))
	conn.Write(req)
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("reply read: %v", err)
	}
	return reply[1], conn
}

func TestConnectThroughProxy(t *testing.T) {
	p := startProxy(t)
	echo := echoTCP(t)
	rep, conn := socksConnect(t, p.addr, echo, nil)
	defer conn.Close()
	if rep != 0x00 {
		t.Fatalf("rep=0x%02x", rep)
	}
	conn.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo failed: %q %v", buf, err)
	}
}

func TestFailClosedWhenTunnelDown(t *testing.T) {
	p := startProxy(t)
	p.gate.Store(false)
	echo := echoTCP(t)
	rep, conn := socksConnect(t, p.addr, echo, nil)
	defer conn.Close()
	if rep != repNetworkUnreachable {
		t.Fatalf("rep=0x%02x want 0x03 (network unreachable)", rep)
	}
}

func TestDomainResolvedRemotely(t *testing.T) {
	p := startProxy(t)
	echo := echoTCP(t)
	conn, err := net.Dial("tcp", p.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte{0x05, 0x01, 0x00})
	io.ReadFull(conn, make([]byte, 2))
	// CONNECT example.com:<echo port> — резолвер теста возвращает 127.0.0.1.
	host := "example.com"
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(host))}
	req = append(req, host...)
	req = binary.BigEndian.AppendUint16(req, uint16(echo.Port))
	conn.Write(req)
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("rep=0x%02x", reply[1])
	}
	conn.Write([]byte("hi"))
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "hi" {
		t.Fatalf("echo via domain failed: %q %v", buf, err)
	}
}

func TestAuthRequired(t *testing.T) {
	p := startProxy(t)
	p.auth.Store(&config.ProxyAuth{Username: "u", Password: "p"})
	echo := echoTCP(t)

	// Без учётных данных: no acceptable methods.
	rep, conn := socksConnect(t, p.addr, echo, nil)
	conn.Close()
	if rep != 0xFF {
		t.Fatalf("rep=0x%02x want 0xFF", rep)
	}

	// С неверными: отказ.
	bad := []byte{0x01, 1, 'u', 1, 'x'}
	rep, conn = socksConnect(t, p.addr, echo, bad)
	conn.Close()
	if rep != 0xFE {
		t.Fatalf("rep=0x%02x want auth failure", rep)
	}

	// С верными: работает.
	good := []byte{0x01, 1, 'u', 1, 'p'}
	rep, conn = socksConnect(t, p.addr, echo, good)
	defer conn.Close()
	if rep != 0x00 {
		t.Fatalf("rep=0x%02x want success", rep)
	}
}

func TestUDPAssociate(t *testing.T) {
	p := startProxy(t)

	// UDP-эхо цель.
	targetConn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer targetConn.Close()
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := targetConn.ReadFrom(buf)
			if err != nil {
				return
			}
			targetConn.WriteTo(buf[:n], from)
		}
	}()
	target := targetConn.LocalAddr().(*net.UDPAddr)

	// ASSOCIATE.
	ctrl, err := net.Dial("tcp", p.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	ctrl.Write([]byte{0x05, 0x01, 0x00})
	io.ReadFull(ctrl, make([]byte, 2))
	ctrl.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	reply := make([]byte, 10)
	if _, err := io.ReadFull(ctrl, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("associate rep=0x%02x", reply[1])
	}
	relayPort := int(binary.BigEndian.Uint16(reply[8:10]))
	relayIP := net.IP(reply[4:8])

	// Датаграмма через relay.
	cli, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	pkt := []byte{0, 0, 0, 0x01}
	pkt = append(pkt, target.IP.To4()...)
	pkt = binary.BigEndian.AppendUint16(pkt, uint16(target.Port))
	pkt = append(pkt, []byte("dgram")...)
	if _, err := cli.WriteTo(pkt, &net.UDPAddr{IP: relayIP, Port: relayPort}); err != nil {
		t.Fatal(err)
	}

	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1500)
	n, _, err := cli.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	// Ответ обёрнут SOCKS5-заголовком (10 байт для IPv4).
	if n < 10 || string(buf[10:n]) != "dgram" {
		t.Fatalf("udp echo failed: %q", buf[:n])
	}
}

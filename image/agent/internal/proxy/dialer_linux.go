//go:build linux

package proxy

import (
	"context"
	"net"
	"syscall"
)

// MarkedDialer — исходящие сокеты с SO_MARK: kill switch выпускает такой
// трафик только через туннель ExpressVPN (D4).
type MarkedDialer struct {
	Mark int
}

func (d *MarkedDialer) control(_, _ string, c syscall.RawConn) error {
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, d.Mark)
	}); err != nil {
		return err
	}
	return serr
}

func (d *MarkedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	nd := &net.Dialer{Control: d.control}
	return nd.DialContext(ctx, network, address)
}

func (d *MarkedDialer) ListenUDP(addr string) (net.PacketConn, error) {
	lc := &net.ListenConfig{Control: d.control}
	return lc.ListenPacket(context.Background(), "udp4", addr)
}

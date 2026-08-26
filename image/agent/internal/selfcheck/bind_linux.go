//go:build linux

package selfcheck

import (
	"context"
	"net"
	"syscall"
)

// eth0Dialer — сокеты, привязанные к eth0 (SO_BINDTODEVICE): трафик уходит
// мимо туннельных маршрутов 0.0.0.0/1 — так измеряется настоящий uplink-путь.
func eth0Dialer() *net.Dialer {
	return &net.Dialer{
		Control: func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				serr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, "eth0")
			}); err != nil {
				return err
			}
			return serr
		},
	}
}

func dialViaEth0(ctx context.Context, network, address string) (net.Conn, error) {
	return eth0Dialer().DialContext(ctx, network, address)
}

// Package proxy — входящий SOCKS5-сервер (RFC 1928): единственная «дверь»
// для приложений пользователя в туннель ExpressVPN.
package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
)

// Коды ответов SOCKS5.
const (
	repSuccess            = 0x00
	repGeneralFailure     = 0x01
	repNetworkUnreachable = 0x03
	repHostUnreachable    = 0x04
	repCommandUnsupported = 0x07
	repAddrUnsupported    = 0x08
)

const (
	cmdConnect      = 0x01
	cmdUDPAssociate = 0x03

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04

	methodNoAuth       = 0x00
	methodUserPass     = 0x02
	methodUnacceptable = 0xFF
)

// addrSpec — разобранный адрес запроса.
type addrSpec struct {
	Host string // домен, если клиент прислал ATYP=domain
	IP   net.IP
	Port int
}

func (a addrSpec) String() string {
	host := a.Host
	if host == "" {
		host = a.IP.String()
	}
	return net.JoinHostPort(host, strconv.Itoa(a.Port))
}

// readHandshake выполняет выбор метода аутентификации.
// Возвращает ошибку, если клиент не поддерживает требуемый метод.
func (s *Server) readHandshake(conn net.Conn) error {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return err
	}
	if hdr[0] != 0x05 {
		return fmt.Errorf("not a SOCKS5 client (ver=%d)", hdr[0])
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}

	auth := s.authFn()
	want := byte(methodNoAuth)
	if auth != nil {
		want = methodUserPass
	}
	for _, m := range methods {
		if m == want {
			if _, err := conn.Write([]byte{0x05, want}); err != nil {
				return err
			}
			if auth != nil {
				return s.verifyUserPass(conn, auth.Username, auth.Password)
			}
			return nil
		}
	}
	conn.Write([]byte{0x05, methodUnacceptable})
	return errors.New("no acceptable auth methods")
}

// verifyUserPass — RFC 1929.
func (s *Server) verifyUserPass(conn net.Conn, username, password string) error {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return err
	}
	if hdr[0] != 0x01 {
		return fmt.Errorf("bad userpass version %d", hdr[0])
	}
	uname := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(conn, uname); err != nil {
		return err
	}
	plenBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, plenBuf); err != nil {
		return err
	}
	passwd := make([]byte, int(plenBuf[0]))
	if _, err := io.ReadFull(conn, passwd); err != nil {
		return err
	}
	if string(uname) != username || string(passwd) != password {
		conn.Write([]byte{0x01, 0x01})
		return errors.New("bad credentials")
	}
	_, err := conn.Write([]byte{0x01, 0x00})
	return err
}

// readRequest читает запрос после хендшейка.
func readRequest(conn net.Conn) (cmd byte, addr addrSpec, err error) {
	hdr := make([]byte, 4)
	if _, err = io.ReadFull(conn, hdr); err != nil {
		return
	}
	if hdr[0] != 0x05 {
		err = fmt.Errorf("bad request version %d", hdr[0])
		return
	}
	cmd = hdr[1]
	addr, err = readAddr(conn, hdr[3])
	return
}

func readAddr(r io.Reader, atyp byte) (addrSpec, error) {
	var a addrSpec
	switch atyp {
	case atypIPv4:
		buf := make([]byte, 4+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return a, err
		}
		a.IP = net.IP(buf[:4])
		a.Port = int(binary.BigEndian.Uint16(buf[4:]))
	case atypIPv6:
		buf := make([]byte, 16+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return a, err
		}
		a.IP = net.IP(buf[:16])
		a.Port = int(binary.BigEndian.Uint16(buf[16:]))
	case atypDomain:
		l := make([]byte, 1)
		if _, err := io.ReadFull(r, l); err != nil {
			return a, err
		}
		buf := make([]byte, int(l[0])+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return a, err
		}
		a.Host = string(buf[:l[0]])
		a.Port = int(binary.BigEndian.Uint16(buf[l[0]:]))
	default:
		return a, errUnsupportedAddr
	}
	return a, nil
}

var errUnsupportedAddr = errors.New("unsupported address type")

// writeReply отправляет ответ с указанным кодом и bound-адресом.
func writeReply(conn net.Conn, rep byte, bnd net.Addr) error {
	ip := net.IPv4zero
	port := 0
	if bnd != nil {
		switch v := bnd.(type) {
		case *net.TCPAddr:
			ip, port = v.IP, v.Port
		case *net.UDPAddr:
			ip, port = v.IP, v.Port
		}
	}
	atyp := byte(atypIPv4)
	raw := ip.To4()
	if raw == nil {
		atyp = atypIPv6
		raw = ip.To16()
	}
	buf := make([]byte, 0, 6+len(raw))
	buf = append(buf, 0x05, rep, 0x00, atyp)
	buf = append(buf, raw...)
	buf = binary.BigEndian.AppendUint16(buf, uint16(port))
	_, err := conn.Write(buf)
	return err
}

// resolve превращает addrSpec в ip:port, используя remote DNS для доменов.
func (s *Server) resolve(ctx context.Context, a addrSpec) (string, error) {
	if a.Host == "" {
		return a.String(), nil
	}
	ip, err := s.resolver(ctx, a.Host)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(a.Port)), nil
}

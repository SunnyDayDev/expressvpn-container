package config

import (
	"fmt"
	"slices"
)

// Config — декларативная конфигурация агента (/data/config.json).
// Все ключи применяются на лету; параметров с requiresRecreate в этом API нет.
type Config struct {
	ExpressVPN ExpressVPN `json:"expressvpn"`
	Uplink     Uplink     `json:"uplink"`
	Proxy      Proxy      `json:"proxy"`
}

type ExpressVPN struct {
	// Location — id локации; пустая строка означает smart.
	Location    string      `json:"location"`
	Protocol    string      `json:"protocol"`
	Autoconnect bool        `json:"autoconnect"`
	Protections Protections `json:"protections"`
}

type Protections struct {
	Ads       bool `json:"ads"`
	Trackers  bool `json:"trackers"`
	Malicious bool `json:"malicious"`
	Adult     bool `json:"adult"`
}

type Uplink struct {
	Mode   string `json:"mode"`
	Socks5 Socks5 `json:"socks5"`
}

type Socks5 struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	// UDP: auto — по результату probe; on/off — принудительно.
	UDP string `json:"udp"`
}

type Proxy struct {
	// Auth — аутентификация входящего SOCKS5 (RFC 1929); nil — выключена.
	Auth *ProxyAuth `json:"auth"`
}

type ProxyAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Default — конфигурация свежего тома.
func Default() Config {
	return Config{
		ExpressVPN: ExpressVPN{Protocol: "auto"},
		Uplink:     Uplink{Mode: "host", Socks5: Socks5{UDP: "auto"}},
	}
}

// KnownProtocols — протоколы, которые принимает валидация. Список согласован
// с установленным демоном (get protocol values, S3) и отдаётся в /v1/version.
var KnownProtocols = []string{"auto", "lightway_udp", "lightway_tcp", "openvpn_udp", "openvpn_tcp", "wireguard"}

// Validate проверяет конфигурацию целиком; ошибки адресуются полям.
func Validate(c Config) error {
	if !slices.Contains(KnownProtocols, c.ExpressVPN.Protocol) {
		return &FieldError{Field: "expressvpn.protocol", Msg: fmt.Sprintf("unknown protocol %q", c.ExpressVPN.Protocol)}
	}
	switch c.Uplink.Mode {
	case "host", "socks5":
	default:
		return &FieldError{Field: "uplink.mode", Msg: fmt.Sprintf("unknown mode %q (host|socks5)", c.Uplink.Mode)}
	}
	if c.Uplink.Mode == "socks5" {
		if c.Uplink.Socks5.Host == "" {
			return &FieldError{Field: "uplink.socks5.host", Msg: "host is required in socks5 mode"}
		}
		if c.Uplink.Socks5.Port < 1 || c.Uplink.Socks5.Port > 65535 {
			return &FieldError{Field: "uplink.socks5.port", Msg: "port must be 1..65535"}
		}
	}
	switch c.Uplink.Socks5.UDP {
	case "auto", "on", "off":
	default:
		return &FieldError{Field: "uplink.socks5.udp", Msg: fmt.Sprintf("unknown value %q (auto|on|off)", c.Uplink.Socks5.UDP)}
	}
	if a := c.Proxy.Auth; a != nil {
		if a.Username == "" || a.Password == "" {
			return &FieldError{Field: "proxy.auth", Msg: "username and password are required when auth is set"}
		}
	}
	return nil
}

// FieldError — ошибка валидации, привязанная к ключу конфигурации.
type FieldError struct {
	Field string
	Msg   string
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Msg }

const mask = "***"

// Redacted возвращает копию для выдачи наружу: пароли заменены на "***"
// (если были заданы), пустые остаются пустыми.
func Redacted(c Config) Config {
	if c.Uplink.Socks5.Password != "" {
		c.Uplink.Socks5.Password = mask
	}
	if c.Proxy.Auth != nil {
		a := *c.Proxy.Auth
		if a.Password != "" {
			a.Password = mask
		}
		c.Proxy.Auth = &a
	}
	return c
}

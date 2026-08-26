package uplink

import (
	"fmt"
	"os"
)

const resolvConfPath = "/etc/resolv.conf"

// tunPeerDNS — адрес «резолвера» в socks5-режиме: любой UDP/53-пакет,
// ушедший в xup0, перехватывается правилом hijack-dns sing-box'а и
// обслуживается DoH через прокси (D5, контур 1). Совпадает с TunPeer —
// nexthop'ом default-маршрута.
const tunPeerDNS = TunPeer

// redirectResolvConf переключает резолвер контейнера на перехват sing-box.
// Возвращает прежнее содержимое для восстановления.
//
// ВАЖНО: /etc/resolv.conf в Docker — bind-mount, его нельзя заменить через
// rename; пишем содержимое на месте.
func redirectResolvConf() (saved []byte, err error) {
	saved, err = os.ReadFile(resolvConfPath)
	if err != nil {
		return nil, err
	}
	content := fmt.Sprintf("# detour: socks5 uplink mode — DNS via sing-box DoH over proxy\nnameserver %s\n", tunPeerDNS)
	if err := os.WriteFile(resolvConfPath, []byte(content), 0o644); err != nil {
		return nil, err
	}
	return saved, nil
}

func restoreResolvConf(saved []byte) error {
	if saved == nil {
		return nil
	}
	return os.WriteFile(resolvConfPath, saved, 0o644)
}

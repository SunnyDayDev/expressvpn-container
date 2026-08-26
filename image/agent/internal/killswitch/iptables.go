package killswitch

import (
	"fmt"
	"strings"
)

// Фолбэк для хостов без работающего nf_tables (старые ядра NAS: nft отвечает
// «cache initialization failed»): те же правила через iptables-legacy.
// Загрузка через iptables-restore --noflush атомарна в пределах таблицы;
// свои правила держим в отдельной цепочке DETOUR_OUT (жёстко очищается
// в том же restore), в OUTPUT — единственный jump.

const legacyChain = "DETOUR_OUT"

// IptablesRuleset — содержимое для `iptables-restore --noflush`.
func IptablesRuleset(p Params) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w("*filter")
	w(":%s - [0:0]", legacyChain)
	w("-F %s", legacyChain)
	w("-A %s -o lo -j ACCEPT", legacyChain)
	// Трафик клиентов прокси: ответы в docker-подсеть разрешены, наружу —
	// только туннель, всё прочее помеченное — drop (счётчик = правило DROP).
	if p.DockerSubnet != "" {
		w("-A %s -m mark --mark 0x%04x -o %s -d %s -j ACCEPT", legacyChain, Mark, p.EthIface, p.DockerSubnet)
	}
	w("-A %s -m mark --mark 0x%04x -o %s -j ACCEPT", legacyChain, Mark, p.TunIface)
	w("-A %s -m mark --mark 0x%04x -j DROP", legacyChain, Mark)
	if p.Mode == "socks5" {
		if p.ProxyIP != "" {
			w("-A %s -o %s -d %s -j ACCEPT", legacyChain, p.EthIface, p.ProxyIP)
		}
		if p.DockerSubnet != "" {
			w("-A %s -o %s -d %s -j ACCEPT", legacyChain, p.EthIface, p.DockerSubnet)
		}
		w("-A %s -o %s -j DROP", legacyChain, p.EthIface)
	}
	w("COMMIT")
	return b.String()
}

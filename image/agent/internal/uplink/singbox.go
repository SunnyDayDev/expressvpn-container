package uplink

import (
	"encoding/json"

	"detour/agent/internal/config"
)

// TunIface — имя uplink-TUN (D3).
const TunIface = "xup0"

// tunAddr — адрес интерфейса xup0 (частная /30, не пересекается с docker).
const tunAddr = "172.29.0.1/30"

// TunPeer — peer-адрес xup0: nexthop для default-маршрута (Lightway требует
// именно gateway-IP, device-route без via он не принимает — S2) и адрес
// «резолвера» для перехвата DNS.
const TunPeer = "172.29.0.2"

// SingboxConfig генерирует конфигурацию sing-box для режима socks5 (D3):
//   - TUN-inbound xup0 с auto_route: false — маршруты ведёт агент, чтобы демон
//     ExpressVPN видел default в main-таблице через xup0;
//   - socks-outbound к пользовательскому прокси с bind_interface eth0
//     (иначе цикл: пакеты к прокси сами ушли бы в xup0);
//   - DNS DoH через тот же прокси + перехват порта 53 (контур 1 из D5).
//
// proxyIP — уже отрезолвленный агентом адрес прокси (sing-box не должен
// резолвить ничего напрямую).
func SingboxConfig(cfg config.Socks5, proxyIP string) ([]byte, error) {
	socksOut := map[string]any{
		"type":           "socks",
		"tag":            "proxy-out",
		"server":         proxyIP,
		"server_port":    cfg.Port,
		"version":        "5",
		"bind_interface": "eth0",
	}
	if cfg.Username != "" {
		socksOut["username"] = cfg.Username
		socksOut["password"] = cfg.Password
	}

	conf := map[string]any{
		"log": map[string]any{"level": "warn", "timestamp": true},
		"dns": map[string]any{
			"servers": []any{
				map[string]any{
					"tag":    "doh-via-proxy",
					"type":   "https",
					"server": "1.1.1.1",
					"detour": "proxy-out",
				},
			},
			"final": "doh-via-proxy",
			// Никаких fallback-резолвов мимо прокси.
			"independent_cache": true,
		},
		"inbounds": []any{
			map[string]any{
				"type":           "tun",
				"tag":            "tun-in",
				"interface_name": TunIface,
				"address":        []string{tunAddr},
				"mtu":            1500,
				"auto_route":     false,
			},
		},
		"outbounds": []any{socksOut},
		"route": map[string]any{
			"rules": []any{
				// Sniff — теперь route-action (sing-box ≥1.11).
				map[string]any{"inbound": []string{"tun-in"}, "action": "sniff"},
				// Перехват DNS: запросы к любому порту 53 обслуживает
				// DNS-слой sing-box (DoH через прокси).
				map[string]any{"protocol": "dns", "action": "hijack-dns"},
			},
			"final":                 "proxy-out",
			"auto_detect_interface": false,
			"default_domain_resolver": map[string]any{
				"server": "doh-via-proxy",
			},
		},
	}
	return json.MarshalIndent(conf, "", "  ")
}

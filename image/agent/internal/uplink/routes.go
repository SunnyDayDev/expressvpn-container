package uplink

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Runner выполняет ip-команды; абстракция для тестов.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (out string, exit int, err error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, int, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	exit := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()
		err = nil
	}
	return string(out), exit, err
}

var viaRe = regexp.MustCompile(`default via (\S+)`)

// detectGateway возвращает шлюз Docker из default-маршрута eth0.
func detectGateway(ctx context.Context, r Runner) (string, error) {
	out, exit, err := r.Run(ctx, "ip", "-4", "route", "show", "default", "dev", "eth0")
	if err != nil || exit != 0 {
		return "", fmt.Errorf("ip route show default: exit %d, %v", exit, err)
	}
	if m := viaRe.FindStringSubmatch(out); m != nil {
		return m[1], nil
	}
	// default-маршрут мог уже быть заменён на xup0 — ищем сохранённый
	// маршрут с метрикой 1000.
	out, exit, err = r.Run(ctx, "ip", "-4", "route", "show", "default")
	if err != nil || exit != 0 {
		return "", fmt.Errorf("ip route show default: exit %d, %v", exit, err)
	}
	if m := viaRe.FindStringSubmatch(out); m != nil {
		return m[1], nil
	}
	return "", fmt.Errorf("no default gateway found in: %s", strings.TrimSpace(out))
}

func ipRoute(ctx context.Context, r Runner, args ...string) error {
	out, exit, err := r.Run(ctx, "ip", append([]string{"route"}, args...)...)
	if err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("ip route %s: %s", strings.Join(args, " "), strings.TrimSpace(out))
	}
	return nil
}

// applyRoutes ставит маршруты режима socks5 (схема из design.md, D3):
//   - <proxyIP>/32 через шлюз Docker (исключение для самого прокси);
//   - запасной default через шлюз с метрикой 1000 (для отката в host);
//   - default через xup0.
func applyRoutes(ctx context.Context, r Runner, proxyIP, gw string) error {
	if err := ipRoute(ctx, r, "replace", proxyIP+"/32", "via", gw, "dev", "eth0"); err != nil {
		return err
	}
	// Запасной default: может уже существовать — replace идемпотентен.
	if err := ipRoute(ctx, r, "replace", "default", "via", gw, "dev", "eth0", "metric", "1000"); err != nil {
		return err
	}
	// Обязательно via <peer>: демон ExpressVPN (Lightway) ищет default
	// gateway-IP и отвергает device-route без nexthop («Failed to find
	// default gateway», docs/spikes/S2.md).
	return ipRoute(ctx, r, "replace", "default", "via", TunPeer, "dev", TunIface)
}

// restoreHostRoutes возвращает маршруты host-режима.
func restoreHostRoutes(ctx context.Context, r Runner, proxyIP, gw string) error {
	if err := ipRoute(ctx, r, "replace", "default", "via", gw, "dev", "eth0"); err != nil {
		return err
	}
	// Служебные маршруты убираем; ошибок «нет маршрута» не боимся.
	_ = ipRoute(ctx, r, "del", "default", "via", gw, "dev", "eth0", "metric", "1000")
	if proxyIP != "" {
		_ = ipRoute(ctx, r, "del", proxyIP+"/32", "via", gw, "dev", "eth0")
	}
	return nil
}

// linkExists проверяет наличие интерфейса.
func linkExists(ctx context.Context, r Runner, iface string) bool {
	_, exit, err := r.Run(ctx, "ip", "link", "show", iface)
	return err == nil && exit == 0
}

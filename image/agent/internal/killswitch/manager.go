package killswitch

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"detour/agent/internal/config"
	"detour/agent/internal/netx"
	"detour/agent/internal/state"
)

// Runner выполняет nft; абстракция для тестов.
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

// Manager применяет правила kill switch и читает счётчики дропов.
// Реализует reconciler.KillswitchDriver.
type Manager struct {
	runner Runner
	st     *state.Store
	logger *slog.Logger
	// resolve — резолв имени прокси (подменяется в тестах).
	resolve func(ctx context.Context, host string) (string, error)
	// подсеть/интерфейсы определяются при первом Apply.
	params Params
	// backend: "nft" | "iptables" (фолбэк для старых ядер без nf_tables).
	backend string
	// legacyLogged — фолбэк уже объявлен в логе (не шуметь на каждом apply).
	legacyLogged bool
}

func NewManager(st *state.Store, logger *slog.Logger) *Manager {
	return &Manager{
		runner:  execRunner{},
		st:      st,
		logger:  logger.With("component", "killswitch"),
		resolve: resolveIPv4,
		params:  Defaults(),
	}
}

func NewManagerWithRunner(st *state.Store, logger *slog.Logger, r Runner, resolve func(context.Context, string) (string, error)) *Manager {
	m := NewManager(st, logger)
	m.runner = r
	if resolve != nil {
		m.resolve = resolve
	}
	return m
}

// Apply атомарно заменяет таблицу inet detour под режим uplink'а.
func (m *Manager) Apply(ctx context.Context, cfg config.Uplink) error {
	p := m.params
	p.Mode = cfg.Mode
	if subnet, err := dockerSubnet(p.EthIface); err == nil {
		p.DockerSubnet = subnet
	}
	if cfg.Mode == "socks5" {
		ip, err := m.resolve(ctx, cfg.Socks5.Host)
		if err != nil {
			return fmt.Errorf("resolve proxy host %q: %w", cfg.Socks5.Host, err)
		}
		p.ProxyIP = ip
		p.ProxyPort = cfg.Socks5.Port
	}

	if err := m.applyNFT(ctx, p); err != nil {
		// Старые ядра (NAS) часто без работающего nf_tables — фолбэк на
		// iptables-legacy с теми же правилами.
		if lerr := m.applyIptables(ctx, p); lerr != nil {
			return fmt.Errorf("nft failed (%v); iptables fallback failed: %w", err, lerr)
		}
		if !m.legacyLogged {
			m.logger.Warn("nf_tables unavailable on this kernel, using iptables-legacy fallback", "nftError", err)
			m.legacyLogged = true
		}
		m.backend = "iptables"
		m.params = p
		m.logger.Info("kill switch applied", "backend", "iptables", "mode", p.Mode, "proxyIP", p.ProxyIP)
		return nil
	}
	m.backend = "nft"
	m.params = p
	m.logger.Info("kill switch applied", "backend", "nft", "mode", p.Mode, "proxyIP", p.ProxyIP)
	return nil
}

func (m *Manager) applyNFT(ctx context.Context, p Params) error {
	f, err := os.CreateTemp("", "detour-nft-*.conf")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(Ruleset(p)); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	out, exit, err := m.runner.Run(ctx, "nft", "-f", f.Name())
	if err != nil {
		return fmt.Errorf("run nft: %w", err)
	}
	if exit != 0 {
		return fmt.Errorf("nft -f failed (exit %d): %s", exit, strings.TrimSpace(out))
	}
	return nil
}

// applyIptables загружает правила через iptables-legacy-restore (атомарно в
// пределах таблицы; --noflush не трогает чужие цепочки, наша чистится внутри).
func (m *Manager) applyIptables(ctx context.Context, p Params) error {
	f, err := os.CreateTemp("", "detour-ipt-*.rules")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(IptablesRuleset(p)); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	out, exit, err := m.runner.Run(ctx, "iptables-legacy-restore", "--noflush", f.Name())
	if err != nil {
		return fmt.Errorf("run iptables-legacy-restore: %w", err)
	}
	if exit != 0 {
		return fmt.Errorf("iptables-legacy-restore failed (exit %d): %s", exit, strings.TrimSpace(out))
	}
	// Единственный jump из OUTPUT в нашу цепочку (первым правилом).
	_, exit, err = m.runner.Run(ctx, "iptables-legacy", "-C", "OUTPUT", "-j", legacyChain)
	if err != nil {
		return err
	}
	if exit != 0 {
		out, exit, err = m.runner.Run(ctx, "iptables-legacy", "-I", "OUTPUT", "1", "-j", legacyChain)
		if err != nil || exit != 0 {
			return fmt.Errorf("insert OUTPUT jump failed (exit %d): %s, %v", exit, strings.TrimSpace(out), err)
		}
	}
	return nil
}

var counterRe = regexp.MustCompile(`packets\s+(\d+)`)

// Dropped суммирует счётчики отброшенных пакетов (backend-зависимо).
func (m *Manager) Dropped(ctx context.Context) (int64, error) {
	if m.backend == "iptables" {
		return m.droppedIptables(ctx)
	}
	out, exit, err := m.runner.Run(ctx, "nft", "list", "counters", "table", "inet", "detour")
	if err != nil || exit != 0 {
		return 0, fmt.Errorf("nft list counters: exit %d, %v", exit, err)
	}
	var total int64
	for _, match := range counterRe.FindAllStringSubmatch(out, -1) {
		n, _ := strconv.ParseInt(match[1], 10, 64)
		total += n
	}
	return total, nil
}

// droppedIptables — сумма pkts у DROP-правил нашей цепочки
// (`iptables-legacy -L DETOUR_OUT -v -x`: колонки pkts bytes target …).
func (m *Manager) droppedIptables(ctx context.Context) (int64, error) {
	out, exit, err := m.runner.Run(ctx, "iptables-legacy", "-L", legacyChain, "-v", "-x", "-n")
	if err != nil || exit != 0 {
		return 0, fmt.Errorf("iptables -L: exit %d, %v", exit, err)
	}
	var total int64
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[2] == "DROP" {
			if n, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
				total += n
			}
		}
	}
	return total, nil
}

// PollCounters периодически публикует счётчик дропов в state.
func (m *Manager) PollCounters(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !m.st.Get().Killswitch.Active {
			continue
		}
		if n, err := m.Dropped(ctx); err == nil {
			m.st.Update(func(s *state.State) { s.Killswitch.Dropped = n })
		}
	}
}

// dockerSubnet возвращает IPv4-подсеть интерфейса контейнера (CIDR сети).
func dockerSubnet(iface string) (string, error) {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return "", err
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return "", err
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			network := ipnet.IP.Mask(ipnet.Mask)
			ones, _ := ipnet.Mask.Size()
			return fmt.Sprintf("%s/%d", network, ones), nil
		}
	}
	return "", fmt.Errorf("no IPv4 address on %s", iface)
}

// resolveIPv4 резолвит имя в IPv4 пинованным резолвером контейнера —
// единственный разрешённый «прямой» резолв (D5, контур 3).
func resolveIPv4(ctx context.Context, host string) (string, error) {
	return netx.ResolveIPv4(ctx, host)
}

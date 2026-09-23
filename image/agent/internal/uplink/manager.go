// Package uplink — выход контейнера наружу: host (шлюз Docker) или socks5
// (sing-box: TUN xup0 → пользовательский SOCKS5-прокси; маршруты ведёт агент).
package uplink

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"syscall"
	"time"

	"detour/agent/internal/config"
	"detour/agent/internal/netx"
	"detour/agent/internal/state"
)

const (
	// startTimeout — ожидание появления xup0 после запуска sing-box.
	startTimeout = 10 * time.Second
	// healthInterval — период health-мониторинга uplink'а (спека: ≤10 c).
	healthInterval = 10 * time.Second
	tcpCheckTimeout = 3 * time.Second
)

// Manager реализует reconciler.UplinkDriver.
type Manager struct {
	logger  *slog.Logger
	runner  Runner
	resolve func(ctx context.Context, host string) (string, error)
	st      *state.Store
	// onRecovered — уведомление слоя ExpressVPN о восстановлении uplink'а
	// (reconciler.ResetRetries).
	onRecovered func()
	cfgPath     string
	// probe — UDP-проба (ProbeUDP; подменяется в тестах).
	probe func(ctx context.Context, proxyIP string, cfg config.Socks5, logger *slog.Logger) bool

	mu          sync.Mutex
	proc        *exec.Cmd
	procDone    chan struct{}
	applied     *config.Uplink
	probeRun    *probeRun // проба, запущенная последним Apply (udp=auto)
	proxyIP     string
	gw          string
	savedResolv []byte
}

func NewManager(logger *slog.Logger, st *state.Store) *Manager {
	return &Manager{
		logger:  logger.With("component", "uplink"),
		runner:  execRunner{},
		resolve: resolveIPv4,
		st:      st,
		cfgPath: filepath.Join(os.TempDir(), "detour-singbox.json"),
		probe:   ProbeUDP,
	}
}

// probeRun — одна UDP-проба; done закрывается после публикации результата.
type probeRun struct {
	done chan struct{}
}

// OnRecovered задаёт колбэк восстановления (вызывается из health-монитора).
func (m *Manager) OnRecovered(f func()) { m.onRecovered = f }

// Apply идемпотентно приводит uplink к конфигурации.
func (m *Manager) Apply(ctx context.Context, cfg config.Uplink) (state.Uplink, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch cfg.Mode {
	case "host":
		if m.applied != nil && m.applied.Mode == "socks5" {
			m.teardownLocked(ctx)
		}
		m.applied = &cfg
		m.probeRun = nil
		return state.Uplink{Mode: "host", Status: state.UplinkUp, UDPSupported: state.TriTrue}, nil

	case "socks5":
		endpoint := net.JoinHostPort(cfg.Socks5.Host, strconv.Itoa(cfg.Socks5.Port))
		if m.applied != nil && reflect.DeepEqual(*m.applied, cfg) && m.procAlive() {
			return m.statusLocked(ctx, cfg, endpoint), nil
		}
		if m.applied != nil && m.applied.Mode == "socks5" {
			m.teardownLocked(ctx)
		}

		ip, err := m.resolve(ctx, cfg.Socks5.Host)
		if err != nil {
			return downState(cfg, endpoint, "proxy_unreachable"),
				fmt.Errorf("resolve proxy host: %w", err)
		}
		gw, err := detectGateway(ctx, m.runner)
		if err != nil {
			return downState(cfg, endpoint, "no_gateway"), err
		}
		m.proxyIP, m.gw = ip, gw

		// Исключение-маршрут к прокси — ДО любых проверок: если VPN ещё
		// подключён, его 0.0.0.0/1-маршруты иначе утащат проверку внутрь
		// туннеля, где LAN-адрес прокси недостижим (ложный down).
		if err := ipRoute(ctx, m.runner, "replace", ip+"/32", "via", gw, "dev", "eth0"); err != nil {
			return downState(cfg, endpoint, "routes_failed"), err
		}

		// Быстрая проверка достижимости прокси до перестройки остальных маршрутов.
		if err := tcpCheck(ctx, ip, cfg.Socks5.Port); err != nil {
			return downState(cfg, endpoint, "proxy_unreachable"),
				fmt.Errorf("proxy %s: %w", endpoint, err)
		}

		raw, err := SingboxConfig(cfg.Socks5, ip)
		if err != nil {
			return downState(cfg, endpoint, "config_failed"), err
		}
		if err := os.WriteFile(m.cfgPath, raw, 0o600); err != nil {
			return downState(cfg, endpoint, "config_failed"), err
		}
		if err := m.startLocked(ctx); err != nil {
			return downState(cfg, endpoint, "engine_failed"), err
		}
		if err := applyRoutes(ctx, m.runner, ip, gw); err != nil {
			m.teardownLocked(ctx)
			return downState(cfg, endpoint, "routes_failed"), err
		}
		// DNS контейнера — на перехват sing-box (иначе демон резолвил бы
		// через Docker-резолвер хоста в обход прокси; D5, контур 1).
		if saved, err := redirectResolvConf(); err != nil {
			m.logger.Error("redirect resolv.conf", "error", err)
		} else if m.savedResolv == nil {
			m.savedResolv = saved
		}
		m.applied = &cfg
		m.logger.Info("socks5 uplink up", "endpoint", endpoint, "proxyIP", ip, "gw", gw)

		us := state.Uplink{Mode: "socks5", Status: state.UplinkUp, Endpoint: endpoint}
		switch cfg.Socks5.UDP {
		case "on":
			us.UDPSupported = state.TriTrue
		case "off":
			us.UDPSupported = state.TriFalse
		default:
			us.UDPSupported = state.TriUnknown
			// Probe — асинхронно, чтобы не держать реконсайлер (задача 7.3);
			// кому нужен результат до подключения, ждёт его через WaitUDP.
			m.startProbeLocked(cfg, ip)
		}
		return us, nil

	default:
		return state.Uplink{Mode: cfg.Mode, Status: state.UplinkUnknown, UDPSupported: state.TriUnknown},
			fmt.Errorf("unknown uplink mode %q", cfg.Mode)
	}
}

func downState(cfg config.Uplink, endpoint, reason string) state.Uplink {
	return state.Uplink{
		Mode:         cfg.Mode,
		Status:       state.UplinkDown,
		UDPSupported: state.TriUnknown,
		Endpoint:     endpoint,
		Reason:       reason,
	}
}

func (m *Manager) statusLocked(ctx context.Context, cfg config.Uplink, endpoint string) state.Uplink {
	us := state.Uplink{Mode: "socks5", Endpoint: endpoint, UDPSupported: m.st.Get().Uplink.UDPSupported}
	if tcpCheck(ctx, m.proxyIP, cfg.Socks5.Port) == nil && linkExists(ctx, m.runner, TunIface) {
		us.Status = state.UplinkUp
	} else {
		us.Status = state.UplinkDown
		us.Reason = "proxy_unreachable"
	}
	return us
}

// startLocked запускает sing-box и ждёт появления xup0.
func (m *Manager) startLocked(ctx context.Context) error {
	cmd := exec.Command("sing-box", "run", "-c", m.cfgPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start sing-box: %w", err)
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			m.logger.Debug(sc.Text())
		}
	}()
	go func() { cmd.Wait(); close(done) }()
	m.proc, m.procDone = cmd, done

	deadline := time.Now().Add(startTimeout)
	for !linkExists(ctx, m.runner, TunIface) {
		select {
		case <-done:
			m.proc, m.procDone = nil, nil
			return errors.New("sing-box exited during startup")
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			m.stopProcLocked()
			return fmt.Errorf("interface %s did not appear in %s", TunIface, startTimeout)
		}
	}
	m.logger.Info("sing-box started", "pid", cmd.Process.Pid, "iface", TunIface)
	return nil
}

func (m *Manager) procAlive() bool {
	if m.proc == nil {
		return false
	}
	select {
	case <-m.procDone:
		return false
	default:
		return true
	}
}

func (m *Manager) stopProcLocked() {
	if m.proc == nil {
		return
	}
	_ = m.proc.Process.Signal(syscall.SIGTERM)
	select {
	case <-m.procDone:
	case <-time.After(3 * time.Second):
		_ = m.proc.Process.Kill()
		<-m.procDone
	}
	m.proc, m.procDone = nil, nil
}

// teardownLocked останавливает sing-box и возвращает маршруты host-режима.
func (m *Manager) teardownLocked(ctx context.Context) {
	m.stopProcLocked()
	if m.savedResolv != nil {
		if err := restoreResolvConf(m.savedResolv); err != nil {
			m.logger.Error("restore resolv.conf", "error", err)
		}
		m.savedResolv = nil
	}
	if m.gw != "" {
		if err := restoreHostRoutes(ctx, m.runner, m.proxyIP, m.gw); err != nil {
			m.logger.Error("restore host routes", "error", err)
		}
	}
	m.proxyIP = ""
	m.applied = nil
	m.probeRun = nil
	m.logger.Info("socks5 uplink torn down")
}

// Shutdown — остановка при завершении агента.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.applied != nil && m.applied.Mode == "socks5" {
		m.teardownLocked(ctx)
	}
}

// startProbeLocked запускает UDP-пробу для только что применённого uplink'а.
// Сброс udpSupported и публикация результата идут под m.mu, поэтому проба
// прежнего uplink'а не может записать свой результат поверх нового.
func (m *Manager) startProbeLocked(cfg config.Uplink, ip string) {
	run := &probeRun{done: make(chan struct{})}
	m.probeRun = run
	m.st.Update(func(s *state.State) { s.Uplink.UDPSupported = state.TriUnknown })
	go m.probeAndPublish(run, cfg, ip)
}

// probeAndPublish выполняет UDP-probe и публикует результат (udp=auto), если
// за это время uplink не перестроили.
func (m *Manager) probeAndPublish(run *probeRun, cfg config.Uplink, ip string) {
	defer close(run.done)
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout+2*time.Second)
	defer cancel()
	supported := m.probe(ctx, ip, cfg.Socks5, m.logger)
	v := state.TriFalse
	if supported {
		v = state.TriTrue
	}
	m.mu.Lock()
	current := m.probeRun == run
	if current {
		m.st.Update(func(s *state.State) {
			if s.Uplink.Mode == "socks5" {
				s.Uplink.UDPSupported = v
			}
		})
	}
	m.mu.Unlock()
	m.logger.Info("udp probe finished", "supported", supported, "current", current)
}

// WaitUDP ждёт результата UDP-пробы, запущенной последним Apply, и возвращает
// uplink.udpSupported; для host и udp=on|off — сразу. По отмене ctx — unknown:
// сколько ждать, решает вызывающий (реконсайлер перед выбором протокола).
func (m *Manager) WaitUDP(ctx context.Context) state.TriState {
	m.mu.Lock()
	cfg, run := m.applied, m.probeRun
	m.mu.Unlock()
	switch {
	case cfg == nil:
		return state.TriUnknown
	case cfg.Mode == "host", cfg.Socks5.UDP == "on":
		return state.TriTrue
	case cfg.Socks5.UDP == "off":
		return state.TriFalse
	case run == nil:
		return m.st.Get().Uplink.UDPSupported
	}
	select {
	case <-run.done:
		return m.st.Get().Uplink.UDPSupported
	case <-ctx.Done():
		return state.TriUnknown
	}
}

// ProbeAction — действие probe-uplink: перезапускает probe вручную.
func (m *Manager) ProbeAction(ctx context.Context) error {
	m.mu.Lock()
	cfg := m.applied
	ip := m.proxyIP
	m.mu.Unlock()
	if cfg == nil || cfg.Mode != "socks5" {
		return errors.New("probe-uplink is only meaningful in socks5 mode")
	}
	switch cfg.Socks5.UDP {
	case "on", "off":
		return nil // переопределено вручную
	}
	supported := ProbeUDP(ctx, ip, cfg.Socks5, m.logger)
	v := state.TriFalse
	if supported {
		v = state.TriTrue
	}
	m.st.Update(func(s *state.State) {
		if s.Uplink.Mode == "socks5" {
			s.Uplink.UDPSupported = v
		}
	})
	return nil
}

// Monitor — health-мониторинг uplink'а (задача 7.4): каждые 10 с проверяет
// прокси и TUN; переходы публикует в state; down→up дополнительно дёргает
// onRecovered (переподключение VPN).
func (m *Manager) Monitor(ctx context.Context) {
	t := time.NewTicker(healthInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		m.mu.Lock()
		cfg := m.applied
		ip := m.proxyIP
		m.mu.Unlock()
		if cfg == nil || cfg.Mode != "socks5" {
			continue
		}

		proxyOK := tcpCheck(ctx, ip, cfg.Socks5.Port) == nil
		tunOK := m.procAlive() && linkExists(ctx, m.runner, TunIface)
		var status state.UplinkStatus
		var reason string
		switch {
		case proxyOK && tunOK:
			status = state.UplinkUp
		case tunOK && !proxyOK:
			status = state.UplinkDown
			reason = "proxy_unreachable"
		default:
			status = state.UplinkDegraded
			reason = "engine_not_running"
		}

		prev := m.st.Get().Uplink.Status
		if prev == status {
			continue
		}
		m.logger.Info("uplink status changed", "from", string(prev), "to", string(status), "reason", reason)
		m.st.Update(func(s *state.State) {
			s.Uplink.Status = status
			s.Uplink.Reason = reason
		})
		if status == state.UplinkUp && m.onRecovered != nil {
			m.onRecovered()
		}
	}
}

// tcpCheck — TCP-доступность прокси.
func tcpCheck(ctx context.Context, ip string, port int) error {
	d := net.Dialer{Timeout: tcpCheckTimeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return conn.Close()
}

// resolveIPv4 — резолв имени прокси пинованным резолвером контейнера
// (единственный разрешённый прямой резолв, D5 контур 3); не зависит от
// подменённого /etc/resolv.conf.
func resolveIPv4(ctx context.Context, host string) (string, error) {
	return netx.ResolveIPv4(ctx, host)
}

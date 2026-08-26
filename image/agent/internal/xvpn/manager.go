package xvpn

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"detour/agent/internal/config"
	"detour/agent/internal/state"
)

const (
	// connectTimeout/connectRetries — спека expressvpn-control: 45 с на
	// попытку, до 3 повторов с экспоненциальной задержкой.
	connectTimeout = 45 * time.Second
	connectRetries = 3
	// pollInterval — надзор за состоянием демона (спека: не реже раза в 5 с).
	pollInterval = 3 * time.Second
)

// Manager — высокоуровневые операции над демоном: подключение с ретраями,
// настройки, вход/выход, надзор. Реализует reconciler.XVPNDriver.
type Manager struct {
	cli    *CLI
	st     *state.Store
	logger *slog.Logger
}

func NewManager(cli *CLI, st *state.Store, logger *slog.Logger) *Manager {
	return &Manager{cli: cli, st: st, logger: logger.With("component", "expressvpn")}
}

// Connect подключает к локации указанным API-протоколом (уже effective).
func (m *Manager) Connect(ctx context.Context, location, protocol string) error {
	if protocol != "" {
		if err := m.cli.Set(ctx, "protocol", ProtocolToCLI(protocol)); err != nil {
			return err
		}
	}
	var lastErr error
	delay := 2 * time.Second
	for attempt := 1; attempt <= connectRetries; attempt++ {
		err := m.cli.Connect(ctx, location, connectTimeout)
		if err == nil {
			// CLI `connect` возвращает rc=0 до фактического установления
			// туннеля — дожидаемся Connected сами (S2: иначе ложный успех).
			if err = m.waitConnected(ctx, connectTimeout); err == nil {
				m.afterConnect(ctx)
				return nil
			}
		}
		lastErr = err
		var ce *CLIError
		if errors.As(err, &ce) && (ce.Code == "unknown_location" || ce.Code == "not_logged_in") {
			return err // ретраи бессмысленны
		}
		m.logger.Warn("connect attempt failed", "attempt", attempt, "error", err)
		if attempt < connectRetries {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
		}
	}
	return &CLIError{Code: "connect_failed", Message: lastErr.Error()}
}

// waitConnected опрашивает демон, пока подключение не установится фактически.
func (m *Manager) waitConnected(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := m.cli.ConnectionState(cctx)
		cancel()
		if err == nil && conn == state.ConnConnected {
			return nil
		}
		if time.Now().After(deadline) {
			return &CLIError{Code: "connect_failed", Message: "tunnel did not reach Connected in " + timeout.String()}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// afterConnect обновляет фактические поля состояния после подключения.
func (m *Manager) afterConnect(ctx context.Context) {
	region, _ := m.cli.Get(ctx, "region")
	pubip, _ := m.cli.Get(ctx, "pubip")
	now := time.Now().UTC()
	m.st.Update(func(s *state.State) {
		if region != "" && region != "smart" {
			s.ExpressVPN.Location = region
		}
		s.ExpressVPN.PublicIP = pubip
		s.ExpressVPN.ConnectedAt = &now
	})
}

func (m *Manager) Disconnect(ctx context.Context) error {
	if err := m.cli.Disconnect(ctx); err != nil {
		return err
	}
	m.st.Update(func(s *state.State) {
		s.ExpressVPN.PublicIP = ""
		s.ExpressVPN.ConnectedAt = nil
	})
	return nil
}

// ApplySettings применяет защиту на лету. Autoconnect — поведение агента
// (восстановление подключения при старте), демону не транслируется.
func (m *Manager) ApplySettings(ctx context.Context, cfg config.ExpressVPN) error {
	toggles := map[string]bool{
		"blockAds":       cfg.Protections.Ads,
		"blockTrackers":  cfg.Protections.Trackers,
		"blockMalicious": cfg.Protections.Malicious,
		"blockAdult":     cfg.Protections.Adult,
	}
	for key, val := range toggles {
		v := "false"
		if val {
			v = "true"
		}
		if err := m.cli.Set(ctx, key, v); err != nil {
			return err
		}
	}
	return nil
}

// Login: успешный вход, если демон уже в аккаунте — тоже успех (спека).
func (m *Manager) Login(ctx context.Context, activationCode string) error {
	if st, err := m.cli.Status(ctx); err == nil && st.LoggedIn {
		m.setAuth(state.AuthLoggedIn)
		return nil
	}
	if err := m.cli.Login(ctx, activationCode); err != nil {
		return err
	}
	m.setAuth(state.AuthLoggedIn)
	return nil
}

func (m *Manager) Logout(ctx context.Context) error {
	if err := m.cli.Logout(ctx); err != nil {
		return err
	}
	m.st.Update(func(s *state.State) {
		s.ExpressVPN.Auth = state.AuthLoggedOut
		s.ExpressVPN.Connection = state.ConnDisconnected
		s.Desired.Connection = state.DesiredDisconnected
	})
	return nil
}

func (m *Manager) setAuth(a state.Auth) {
	m.st.Update(func(s *state.State) { s.ExpressVPN.Auth = a })
}

// ensureNetworkLockOff выключает Network Lock, если он оказался включён
// (например, после обновления демона), с предупреждением в лог.
func (m *Manager) ensureNetworkLockOff(ctx context.Context) {
	v, err := m.cli.Get(ctx, "networklock")
	if err != nil || v == "false" {
		return
	}
	m.logger.Warn("ExpressVPN Network Lock re-enabled externally; disabling (detour kill switch owns leak protection)", "value", v)
	if err := m.cli.Set(ctx, "networklock", "false"); err != nil {
		m.logger.Error("failed to disable Network Lock", "error", err)
	}
}

// Supervise опрашивает демон каждые ≤5 с, отражает фактическое состояние в
// state и будит реконсайлер при неожиданном разрыве (задача 5.7).
func (m *Manager) Supervise(ctx context.Context, wakeReconnect func()) {
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	polls := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		polls++
		// Раз в ~минуту убеждаемся, что Network Lock ExpressVPN выключен —
		// его роль выполняет наш kill switch (спека kill-switch).
		if polls%20 == 0 {
			m.ensureNetworkLockOff(ctx)
		}
		cctx, cancel := context.WithTimeout(ctx, pollInterval)
		conn, err := m.cli.ConnectionState(cctx)
		cancel()
		if err != nil {
			continue // демон перезапускается — этим занимается Daemon.Run
		}
		s := m.st.Get()
		desired := s.Desired.Connection
		prev := s.ExpressVPN.Connection
		switch {
		case desired == state.DesiredConnected && prev == state.ConnConnected && conn != state.ConnConnected:
			// Неожиданный разрыв: помечаем и будим реконсайлер.
			m.logger.Warn("connection dropped", "daemonState", string(conn))
			m.st.Update(func(st *state.State) { st.ExpressVPN.Connection = state.ConnReconnecting })
			wakeReconnect()
		case prev == state.ConnConnected && conn == state.ConnConnected:
			// Живое подключение — освежаем публичный IP изредка? Нет: дорого.
		case prev == state.ConnDisconnected && conn == state.ConnConnected:
			// Подключили извне (не через API) — отражаем.
			m.st.Update(func(st *state.State) { st.ExpressVPN.Connection = state.ConnConnected })
		}
	}
}

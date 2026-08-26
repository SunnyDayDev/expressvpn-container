package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"detour/agent/internal/state"
	"detour/agent/internal/xvpn"
)

// AccountDriver — операции над аккаунтом и локациями (xvpn.Manager).
type AccountDriver interface {
	Login(ctx context.Context, activationCode string) error
	Logout(ctx context.Context) error
}

// LocationSource — кеш локаций (xvpn.LocationCache).
type LocationSource interface {
	List() []xvpn.Location
	Refresh(ctx context.Context) error
}

// Prober — дополнительные проверки (uplink probe, self-check); появляются в
// задачах 7.3/9.1. Nil-поля допустимы до реализации.
type Probers struct {
	ProbeUplink func(ctx context.Context) error
	Selfcheck   func(ctx context.Context) error
}

// Actions — реализация api.Actions поверх реконсайлера: действия меняют
// desired/config и ждут фактического результата через подписку на state.
type Actions struct {
	r       *Reconciler
	acct    AccountDriver
	locs    LocationSource
	probers Probers
	// waitTimeout — максимum ожидания результата connect/disconnect
	// (драйвер сам ретраит: 45 с × 3 + задержки).
	waitTimeout time.Duration
}

func NewActions(r *Reconciler, acct AccountDriver, locs LocationSource, probers Probers) *Actions {
	return &Actions{r: r, acct: acct, locs: locs, probers: probers, waitTimeout: 3 * time.Minute}
}

func (a *Actions) Connect(ctx context.Context, location string) error {
	if location != "" {
		patch, _ := json.Marshal(map[string]any{"expressvpn": map[string]string{"location": location}})
		if _, err := a.r.cfg.ApplyMergePatch(patch); err != nil {
			return err
		}
	}
	a.r.st.Update(func(s *state.State) {
		s.Desired.Connection = state.DesiredConnected
		if s.ExpressVPN.Connection == state.ConnError {
			s.ExpressVPN.Connection = state.ConnDisconnected
		}
		s.LastError = nil
	})
	a.r.ResetRetries()
	return a.waitConnection(ctx, state.ConnConnected)
}

func (a *Actions) Disconnect(ctx context.Context) error {
	a.r.st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredDisconnected })
	a.r.Wake()
	return a.waitConnection(ctx, state.ConnDisconnected)
}

func (a *Actions) Reconnect(ctx context.Context) error {
	s := a.r.st.Get()
	if s.Desired.Connection != state.DesiredConnected {
		return &xvpn.CLIError{Code: "not_connected", Message: "nothing to reconnect: desired state is disconnected"}
	}
	// Сбрасываем appliedConn — цикл выполнит связку disconnect→connect заново.
	a.r.st.Update(func(st *state.State) { st.ExpressVPN.Connection = state.ConnReconnecting })
	a.r.forgetConnection()
	a.r.ResetRetries()
	return a.waitConnection(ctx, state.ConnConnected)
}

func (a *Actions) Login(ctx context.Context, activationCode string) error {
	if activationCode == "" {
		return &xvpn.CLIError{Code: "invalid_activation_code", Message: "activation code is empty"}
	}
	if err := a.acct.Login(ctx, activationCode); err != nil {
		return err
	}
	// Список локаций доступен только после входа — обновляем в фоне.
	go a.locs.Refresh(context.Background())
	return nil
}

func (a *Actions) Logout(ctx context.Context) error {
	return a.acct.Logout(ctx)
}

func (a *Actions) RefreshLocations(ctx context.Context) error {
	return a.locs.Refresh(ctx)
}

func (a *Actions) Selfcheck(ctx context.Context) error {
	if a.probers.Selfcheck == nil {
		return &xvpn.CLIError{Code: "not_implemented", Message: "self-check is not available yet"}
	}
	return a.probers.Selfcheck(ctx)
}

func (a *Actions) ProbeUplink(ctx context.Context) error {
	if a.probers.ProbeUplink == nil {
		return &xvpn.CLIError{Code: "not_implemented", Message: "uplink probe is not available yet"}
	}
	return a.probers.ProbeUplink(ctx)
}

func (a *Actions) Locations(ctx context.Context) (any, error) {
	return map[string]any{"locations": a.locs.List()}, nil
}

// waitConnection ждёт, пока подключение придёт в target или error.
func (a *Actions) waitConnection(ctx context.Context, target state.Connection) error {
	ctx, cancel := context.WithTimeout(ctx, a.waitTimeout)
	defer cancel()
	ch, unsub := a.r.st.Subscribe()
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s", target)
		case s := <-ch:
			if s.ExpressVPN.Connection == target {
				return nil
			}
			if s.ExpressVPN.Connection == state.ConnError {
				if s.LastError != nil {
					return &xvpn.CLIError{Code: s.LastError.Code, Message: s.LastError.Message}
				}
				return errors.New("connection failed")
			}
		}
	}
}

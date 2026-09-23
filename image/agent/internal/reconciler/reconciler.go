// Package reconciler — однопоточный реконсайлер desired → actual.
//
// Все изменения (config, desired) будят цикл; каждый проход идемпотентно
// приводит слои к желаемому состоянию в порядке: kill switch → uplink →
// подключение ExpressVPN. Ошибка слоя не мешает записать состояние и
// повторить на следующем проходе.
package reconciler

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"detour/agent/internal/config"
	"detour/agent/internal/state"
	"detour/agent/internal/xvpn"
)

// XVPNDriver — управление демоном ExpressVPN (пакет xvpn).
type XVPNDriver interface {
	// Connect подключает к локации ("" = smart) по указанному протоколу.
	Connect(ctx context.Context, location, protocol string) error
	Disconnect(ctx context.Context) error
	// ApplySettings применяет защиту/автоподключение и т.п. на лету.
	ApplySettings(ctx context.Context, cfg config.ExpressVPN) error
}

// UplinkDriver — uplink-слой (пакет uplink).
type UplinkDriver interface {
	// Apply идемпотентно приводит uplink к конфигурации и возвращает его
	// состояние (status, udpSupported).
	Apply(ctx context.Context, cfg config.Uplink) (state.Uplink, error)
	// WaitUDP ждёт результата UDP-пробы последнего Apply (udp=auto) и
	// возвращает udpSupported; по отмене ctx — unknown.
	WaitUDP(ctx context.Context) state.TriState
}

// KillswitchDriver — nftables-правила (пакет killswitch).
type KillswitchDriver interface {
	// Apply атомарно заменяет правила под режим uplink'а.
	Apply(ctx context.Context, cfg config.Uplink) error
}

type Drivers struct {
	XVPN       XVPNDriver
	Uplink     UplinkDriver
	Killswitch KillswitchDriver
}

type Reconciler struct {
	st     *state.Store
	cfg    *config.Store
	d      Drivers
	logger *slog.Logger
	wake   chan struct{}

	// applied* — что уже приведено к желаемому (для идемпотентности).
	appliedKS     *config.Uplink
	appliedUplink *config.Uplink
	appliedEVPN   *config.ExpressVPN
	// appliedConn — последняя конфигурация подключения (локация+протокол),
	// с которой выполнялся Connect.
	appliedConn *connSpec

	// Переподключение с backoff (спека Supervision and reconnect):
	// 1,2,4…60 с; после maxReconnectFailures подряд — reconnect_exhausted.
	retryMu         sync.Mutex
	connectFailures int
	nextRetryAt     time.Time
	// forceReason — причина обязательного переподключения на следующем
	// проходе (пусто — не нужно).
	forceReason string

	// udpWait — сколько ждать UDP-пробы перед выбором протокола.
	udpWait time.Duration
}

const maxReconnectFailures = 10

// udpWaitTimeout — проба: 3 с на датаграмму плюс TCP-рукопожатие с прокси.
const udpWaitTimeout = 5 * time.Second

// Причины (пере)подключения — для журнала (спека Supervision and reconnect).
const (
	reasonUplinkChanged   = "uplink_changed"
	reasonUserRequest     = "user_request"
	reasonLocationChanged = "location_changed"
	reasonProtocolChanged = "protocol_changed"
	reasonConnectionLost  = "connection_lost"
	reasonRetry           = "retry"
	reasonConnect         = "connect"
)

type connSpec struct {
	Location string
	Protocol string
}

func New(st *state.Store, cfg *config.Store, d Drivers, logger *slog.Logger) *Reconciler {
	r := &Reconciler{
		st: st, cfg: cfg, d: d,
		logger:  logger.With("component", "agent"),
		wake:    make(chan struct{}, 1),
		udpWait: udpWaitTimeout,
	}
	cfg.OnChange(func(config.Config) { r.Wake() })
	return r
}

// Wake будит цикл; безопасно из любых горутин, не блокирует.
func (r *Reconciler) Wake() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// forgetConnection помечает, что следующий проход цикла должен выполнить
// переподключение, даже если конфигурация не менялась (смена uplink'а,
// действие reconnect).
func (r *Reconciler) forgetConnection(reason string) {
	r.retryMu.Lock()
	r.forceReason = reason
	r.retryMu.Unlock()
}

// ResetRetries сбрасывает счётчик неудач переподключения и будит цикл —
// вызывается при действии пользователя и при восстановлении uplink'а.
func (r *Reconciler) ResetRetries() {
	r.retryMu.Lock()
	r.connectFailures = 0
	r.nextRetryAt = time.Time{}
	r.retryMu.Unlock()
	r.Wake()
}

// Run крутит цикл до отмены контекста.
func (r *Reconciler) Run(ctx context.Context) error {
	// Страховочный тик: даже без явных пробуждений сверяемся раз в 30 с.
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	r.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.wake:
		case <-tick.C:
		}
		r.reconcile(ctx)
	}
}

func (r *Reconciler) reconcile(ctx context.Context) {
	c := r.cfg.Get()
	s := r.st.Get()

	// 1. Kill switch — до любых изменений маршрутов и подключений.
	if r.appliedKS == nil || !reflect.DeepEqual(*r.appliedKS, c.Uplink) {
		if err := r.d.Killswitch.Apply(ctx, c.Uplink); err != nil {
			r.fail("killswitch_failed", err)
			r.st.Update(func(st *state.State) { st.Killswitch.Active = false })
			return // без защиты дальше не идём
		}
		ks := c.Uplink
		r.appliedKS = &ks
		r.st.Update(func(st *state.State) { st.Killswitch.Active = true })
	}

	// 2. Uplink.
	if r.appliedUplink == nil || !reflect.DeepEqual(*r.appliedUplink, c.Uplink) {
		// Немедленно отражаем перестройку в state: UI видит новый режим и
		// «в процессе» сразу, а не после завершения применения. Если VPN
		// подключён — по D7 сразу помечаем reconnecting и планируем
		// принудительный reconnect после пересборки (иначе переподключение
		// происходило бы «случайно», через смерть туннеля под демоном).
		r.st.Update(func(st *state.State) {
			st.Uplink.Mode = c.Uplink.Mode
			st.Uplink.Status = state.UplinkUnknown
			st.Uplink.Reason = "applying"
			st.Uplink.UDPSupported = state.TriUnknown // до пробы нового uplink'а
			if st.Desired.Connection == state.DesiredConnected &&
				st.ExpressVPN.Connection == state.ConnConnected {
				st.ExpressVPN.Connection = state.ConnReconnecting
			}
		})
		if s.Desired.Connection == state.DesiredConnected && r.appliedUplink != nil {
			r.forgetConnection(reasonUplinkChanged)
		}
		us, err := r.d.Uplink.Apply(ctx, c.Uplink)
		if err != nil {
			r.fail("uplink_failed", err)
			// Драйвер возвращает заполненное down-состояние (mode, status,
			// endpoint, reason) — публикуем его, а не полуправду.
			r.st.Update(func(st *state.State) { st.Uplink = us })
			return
		}
		ul := c.Uplink
		r.appliedUplink = &ul
		r.st.Update(func(st *state.State) {
			// Проба могла успеть раньше этой публикации — не затираем её
			// результат: Apply отдаёт unknown, пока проба идёт.
			udp := st.Uplink.UDPSupported
			st.Uplink = us
			if us.UDPSupported == state.TriUnknown {
				st.Uplink.UDPSupported = udp
			}
		})
	}

	// 3. Настройки ExpressVPN (защита, автоподключение) — на лету.
	if r.appliedEVPN == nil || !reflect.DeepEqual(*r.appliedEVPN, c.ExpressVPN) {
		if err := r.d.XVPN.ApplySettings(ctx, c.ExpressVPN); err != nil {
			r.fail("expressvpn_settings_failed", err)
		} else {
			ev := c.ExpressVPN
			r.appliedEVPN = &ev
		}
	}

	// 4. Подключение.
	s = r.st.Get()
	want := connSpec{Location: c.ExpressVPN.Location, Protocol: c.ExpressVPN.Protocol}
	switch s.Desired.Connection {
	case state.DesiredConnected:
		r.retryMu.Lock()
		forceReason := r.forceReason
		r.forceReason = ""
		exhausted := r.connectFailures >= maxReconnectFailures
		retryDue := time.Now().After(r.nextRetryAt)
		r.retryMu.Unlock()
		force := forceReason != ""

		needsConnect := force ||
			s.ExpressVPN.Connection == state.ConnDisconnected ||
			s.ExpressVPN.Connection == state.ConnReconnecting ||
			s.ExpressVPN.Connection == state.ConnError ||
			(r.appliedConn != nil && *r.appliedConn != want && s.ExpressVPN.Connection == state.ConnConnected)
		if !needsConnect {
			return
		}
		// reconnect_exhausted или ещё не время очередной попытки: ждём
		// (действие пользователя и восстановление uplink'а сбрасывают счётчик).
		if !force && (exhausted || !retryDue) {
			return
		}

		why := connectReason(forceReason, s.ExpressVPN.Connection, r.appliedConn, want)
		requested := want.Protocol
		if requested == "" {
			requested = "auto"
		}
		// State — до драйвера: его собственный disconnect не должен выглядеть
		// для Supervise неожиданным разрывом.
		r.st.Update(func(st *state.State) {
			if st.ExpressVPN.Connection == state.ConnConnected {
				st.ExpressVPN.Connection = state.ConnReconnecting
			} else if st.ExpressVPN.Connection != state.ConnReconnecting {
				st.ExpressVPN.Connection = state.ConnConnecting
			}
		})
		// Сразу после смены uplink'а UDP-проба ещё идёт: протокол выбираем по
		// её результату (D7: проба до подключения), а не по unknown.
		udp := r.st.Get().Uplink.UDPSupported
		if c.Uplink.Mode == "socks5" && c.Uplink.Socks5.UDP == "auto" && udp == state.TriUnknown {
			wctx, cancel := context.WithTimeout(ctx, r.udpWait)
			if v := r.d.Uplink.WaitUDP(wctx); v != state.TriUnknown {
				udp = v
				r.st.Update(func(st *state.State) { st.Uplink.UDPSupported = v })
			}
			cancel()
		}
		effective, reason := xvpn.EffectiveProtocol(requested, c.Uplink.Mode, udp)
		r.st.Update(func(st *state.State) {
			st.ExpressVPN.Protocol = state.Protocol{Requested: requested, Effective: effective, Reason: reason}
		})
		msg := "reconnecting expressvpn"
		if s.ExpressVPN.Connection == state.ConnDisconnected {
			msg = "connecting expressvpn"
		}
		r.logger.Info(msg, "reason", why, "from", string(s.ExpressVPN.Connection),
			"location", locationOrSmart(want.Location), "protocol", effective)
		start := time.Now()
		if err := r.d.XVPN.Connect(ctx, want.Location, effective); err != nil {
			r.onConnectFailure(err)
			return
		}
		r.retryMu.Lock()
		r.connectFailures = 0
		r.nextRetryAt = time.Time{}
		r.retryMu.Unlock()
		r.appliedConn = &want
		r.st.Update(func(st *state.State) {
			st.ExpressVPN.Connection = state.ConnConnected
			if want.Location != "" {
				st.ExpressVPN.Location = want.Location
			}
			st.LastError = nil
		})
		r.logger.Info("expressvpn connected", "location", locationOrSmart(r.st.Get().ExpressVPN.Location),
			"protocol", effective, "took", time.Since(start).Round(100*time.Millisecond).String())
	case state.DesiredDisconnected:
		if s.ExpressVPN.Connection == state.ConnConnected ||
			s.ExpressVPN.Connection == state.ConnConnecting ||
			s.ExpressVPN.Connection == state.ConnReconnecting {
			if err := r.d.XVPN.Disconnect(ctx); err != nil {
				r.fail("disconnect_failed", err)
				return
			}
			r.appliedConn = nil
			r.st.Update(func(st *state.State) { st.ExpressVPN.Connection = state.ConnDisconnected })
		}
	}
}

// connectReason — причина (пере)подключения для журнала: явная (смена
// uplink'а, действие пользователя) или выведенная из состояния.
func connectReason(force string, conn state.Connection, applied *connSpec, want connSpec) string {
	switch {
	case force != "":
		return force
	case conn == state.ConnConnected && applied != nil && applied.Location != want.Location:
		return reasonLocationChanged
	case conn == state.ConnConnected:
		return reasonProtocolChanged
	case conn == state.ConnReconnecting:
		return reasonConnectionLost
	case conn == state.ConnError:
		return reasonRetry
	default:
		return reasonConnect
	}
}

func locationOrSmart(loc string) string {
	if loc == "" {
		return "smart"
	}
	return loc
}

// onConnectFailure: каждая неудача (драйвер уже сделал свои 45 с × 3)
// увеличивает счётчик; повтор — с backoff 1,2,4…60 с; после 10 подряд —
// reconnect_exhausted до действия пользователя или восстановления uplink'а.
func (r *Reconciler) onConnectFailure(err error) {
	r.retryMu.Lock()
	r.connectFailures++
	n := r.connectFailures
	var backoff time.Duration
	if n < maxReconnectFailures {
		backoff = time.Second << (n - 1)
		if backoff > 60*time.Second {
			backoff = 60 * time.Second
		}
		r.nextRetryAt = time.Now().Add(backoff)
	}
	r.retryMu.Unlock()

	// Останавливаем собственный реконнект-цикл демона: между нашими
	// пейсированными попытками он не должен молотить uplink (и, в режиме
	// socks5, пользовательский прокси/роутер) своими ретраями.
	dctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	_ = r.d.XVPN.Disconnect(dctx)
	cancel()

	if n >= maxReconnectFailures {
		r.fail("reconnect_exhausted", err)
		r.st.Update(func(st *state.State) { st.ExpressVPN.Connection = state.ConnError })
		return
	}
	r.fail(errCode(err, "connect_failed"), err)
	r.st.Update(func(st *state.State) { st.ExpressVPN.Connection = state.ConnError })
	r.logger.Info("scheduling reconnect", "attempt", n, "backoff", backoff.String())
	time.AfterFunc(backoff, r.Wake)
}

func errCode(err error, def string) string {
	var ce *xvpn.CLIError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return def
}

func (r *Reconciler) fail(code string, err error) {
	r.logger.Error("reconcile step failed", "code", code, "error", err)
	r.st.Update(func(st *state.State) {
		st.LastError = &state.Error{Code: code, Message: err.Error(), At: time.Now().UTC()}
	})
}

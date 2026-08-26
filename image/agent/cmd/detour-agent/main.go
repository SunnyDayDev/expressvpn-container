// detour-agent — управляющий процесс (PID 1) контейнера Detour.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"detour/agent/internal/api"
	"detour/agent/internal/config"
	"detour/agent/internal/killswitch"
	"detour/agent/internal/logs"
	"detour/agent/internal/proxy"
	"detour/agent/internal/reconciler"
	"detour/agent/internal/selfcheck"
	"detour/agent/internal/state"
	"detour/agent/internal/uplink"
	"detour/agent/internal/webui"
	"detour/agent/internal/xvpn"
)

// version проставляется при сборке через -ldflags -X main.version=…
var version = "dev"

// shutdownBudget — предел на корректное завершение по SIGTERM; спека
// container-image требует уложиться в 10 секунд до SIGKILL.
const shutdownBudget = 9 * time.Second

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "-v", "--version":
			fmt.Println(version)
			return
		case "healthcheck":
			os.Exit(healthcheck())
		case "reset-password":
			// Сброс пароля администратора с хоста:
			//   docker compose exec detour detour-agent reset-password
			authDir := envOr("DETOUR_DATA_DIR", "/data") + "/auth"
			if err := api.ResetPassword(authDir); err != nil {
				fmt.Fprintln(os.Stderr, "reset-password:", err)
				os.Exit(1)
			}
			fmt.Println("Admin password cleared. All sessions are now invalid;")
			fmt.Println("the next visit to the web UI will ask to create a new password.")
			return
		}
	}

	red := logs.NewRedactor()
	logBuf := logs.NewBuffer(5000, red)
	logger := slog.New(logs.NewHandler(logBuf))
	slog.SetDefault(logger)

	if err := run(logger, logBuf); err != nil {
		var pe *prereqError
		if errors.As(err, &pe) {
			logger.Error(pe.Error())
			os.Exit(exitPrereqFailed)
		}
		logger.Error("agent failed", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, logBuf *logs.Buffer) error {
	logger.Info("detour-agent starting", "version", version)

	if err := checkPrereqs(); err != nil {
		return err
	}

	dataDir := envOr("DETOUR_DATA_DIR", "/data")
	if err := ensureDataDir(dataDir); err != nil {
		return fmt.Errorf("prepare data dir: %w", err)
	}
	if err := xvpn.EnsureStateDir(dataDir); err != nil {
		return fmt.Errorf("prepare expressvpn state dir: %w", err)
	}

	cfgStore, err := config.NewStore(dataDir + "/config.json")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if pw := cfgStore.Get().Uplink.Socks5.Password; pw != "" {
		logBuf.Redactor().Add(pw)
	}
	tokens, err := api.NewTokenStore(dataDir + "/auth")
	if err != nil {
		return fmt.Errorf("init api token: %w", err)
	}
	logBuf.Redactor().Add(tokens.Current())

	initial := state.Initial(version)
	initial.Container.PublishedSocksPort = envOr("DETOUR_PUBLISHED_SOCKS_PORT", "1080")
	initial.Container.PublishedHTTPPort = envOr("DETOUR_PUBLISHED_HTTP_PORT", "48100")
	initial.Container.PublishedBindAddr = envOr("DETOUR_PUBLISHED_BIND_ADDR", "127.0.0.1")
	stateStore := state.NewStore(initial)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Демон ExpressVPN: прямой дочерний процесс с рестартами.
	cli := xvpn.NewCLI()
	daemon := xvpn.NewDaemon(logger, func() {
		stateStore.Update(func(s *state.State) {
			if s.Desired.Connection == state.DesiredConnected {
				s.ExpressVPN.Connection = state.ConnReconnecting
			}
		})
	})
	daemonDone := make(chan struct{})
	go func() { defer close(daemonDone); _ = daemon.Run(ctx) }()

	// Слои: kill switch → uplink → ExpressVPN, связанные реконсайлером.
	xvpnMgr := xvpn.NewManager(cli, stateStore, logger)
	ksMgr := killswitch.NewManager(stateStore, logger)
	upMgr := uplink.NewManager(logger, stateStore)
	rec := reconciler.New(stateStore, cfgStore, reconciler.Drivers{
		XVPN:       xvpnMgr,
		Uplink:     upMgr,
		Killswitch: ksMgr,
	}, logger)
	upMgr.OnRecovered(rec.ResetRetries)
	locCache := xvpn.NewLocationCache(dataDir, cli)

	markedDialer := &proxy.MarkedDialer{Mark: killswitch.Mark}
	tunnelResolver := proxy.NewTunnelResolver(markedDialer)
	checker := selfcheck.New(stateStore, cfgStore, "127.0.0.1:1080", logger,
		tunnelResolver.Resolve, tunnelResolver.Nameserver)
	actions := reconciler.NewActions(rec, xvpnMgr, locCache, reconciler.Probers{
		ProbeUplink: upMgr.ProbeAction,
		Selfcheck:   checker.Run,
	})

	go initDaemon(ctx, logger, daemon, cli, stateStore, cfgStore, rec)
	go func() { _ = rec.Run(ctx) }()
	go xvpnMgr.Supervise(ctx, rec.Wake)
	go ksMgr.PollCounters(ctx, 10*time.Second)
	go upMgr.Monitor(ctx)

	// Входящий SOCKS5: исходящие сокеты с SO_MARK, remote DNS через резолвер
	// туннеля, fail closed при отключённом VPN. Стартует только после
	// успешного применения kill switch (спека kill-switch).
	publishedSocks, _ := strconv.Atoi(envOr("DETOUR_PUBLISHED_SOCKS_PORT", "1080"))
	proxySrv := proxy.New(proxy.Options{
		Addr:          envOr("DETOUR_SOCKS_ADDR", ":1080"),
		PublishedPort: publishedSocks,
		Dialer:        markedDialer,
		Resolver:      tunnelResolver.Resolve,
		Gate: func() bool {
			return stateStore.Get().ExpressVPN.Connection == state.ConnConnected
		},
		Auth:   func() *config.ProxyAuth { return cfgStore.Get().Proxy.Auth },
		State:  stateStore,
		Logger: logger,
	})
	go func() {
		ch, cancel := stateStore.Subscribe()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case s := <-ch:
				if s.Killswitch.Active {
					cancel()
					if err := proxySrv.Run(ctx); err != nil {
						logger.Error("socks5 server failed", "error", err)
					}
					return
				}
			}
		}
	}()

	srv := api.New(envOr("DETOUR_HTTP_ADDR", ":8080"), api.Deps{
		State:    stateStore,
		Config:   cfgStore,
		Tokens:   tokens,
		Logs:     logBuf,
		Ops:      api.NewOpManager(),
		Actions:  actions,
		Sessions: api.NewSessionManager(dataDir + "/auth"),
		WebFS:    webui.FS(),
		Logger:   logger,
		Version: api.VersionInfo{
			Agent:      version,
			Image:      envOr("DETOUR_EXPRESSVPN_VERSION", "?") + "-" + version,
			ExpressVPN: envOr("DETOUR_EXPRESSVPN_VERSION", "unknown"),
			Uplink:     "sing-box " + envOr("DETOUR_SINGBOX_VERSION", "unknown"),
			Protocols:  config.KnownProtocols,
		},
	})

	errc := make(chan error, 1)
	go func() { errc <- srv.Run(ctx) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	// SIGTERM: останавливаем компоненты в порядке прокси → VPN → uplink → HTTP.
	// Жёсткий предохранитель на случай зависшего шага.
	logger.Info("shutting down")
	deadline := time.AfterFunc(shutdownBudget+time.Second, func() {
		logger.Error("shutdown budget exceeded, forcing exit")
		os.Exit(1)
	})
	defer deadline.Stop()

	shCtx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	// Порядок: прокси и демон гасятся отменой ctx (SIGINT демону);
	// затем sing-box и HTTP-сервер.
	select {
	case <-daemonDone:
	case <-shCtx.Done():
		logger.Warn("daemon did not stop within budget")
	}
	upMgr.Shutdown(shCtx)
	if err := srv.Shutdown(shCtx); err != nil {
		logger.Warn("http shutdown", "error", err)
	}
	<-errc
	logger.Info("bye")
	return nil
}

// initDaemon дожидается готовности демона, выполняет bootstrap и отражает
// сохранённую сессию в state (спека expressvpn-control, Daemon bootstrap).
func initDaemon(ctx context.Context, logger *slog.Logger, daemon *xvpn.Daemon, cli *xvpn.CLI, st *state.Store, cfg *config.Store, rec *reconciler.Reconciler) {
	if err := daemon.WaitReady(ctx, cli, xvpn.ReadyTimeout); err != nil {
		logger.Error("expressvpn daemon not ready", "error", err)
		st.Update(func(s *state.State) {
			s.ExpressVPN.Connection = state.ConnError
			s.LastError = &state.Error{Code: "daemon_not_ready", Message: err.Error(), At: time.Now().UTC()}
		})
		return
	}
	if err := daemon.Bootstrap(ctx, cli); err != nil {
		logger.Error("daemon bootstrap failed", "error", err)
	}
	status, err := cli.Status(ctx)
	if err != nil {
		logger.Error("read daemon status", "error", err)
		return
	}
	conn, err := cli.ConnectionState(ctx)
	if err != nil {
		conn = state.ConnDisconnected
	}
	st.Update(func(s *state.State) {
		if status.LoggedIn {
			s.ExpressVPN.Auth = state.AuthLoggedIn
		} else {
			s.ExpressVPN.Auth = state.AuthLoggedOut
		}
		s.ExpressVPN.Connection = conn
	})
	logger.Info("expressvpn daemon ready", "loggedIn", status.LoggedIn, "connection", string(conn))

	// Autoconnect (спека expressvpn-control): подключаем к последней локации
	// при старте контейнера, если включено и аккаунт активен.
	if cfg.Get().ExpressVPN.Autoconnect && status.LoggedIn && conn != state.ConnConnected {
		logger.Info("autoconnect: restoring connection")
		st.Update(func(s *state.State) { s.Desired.Connection = state.DesiredConnected })
		rec.ResetRetries()
	}
}

// ensureDataDir создаёт структуру тома /data.
func ensureDataDir(dataDir string) error {
	for _, sub := range []string{"", "auth", "logs"} {
		if err := os.MkdirAll(dataDir+"/"+sub, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

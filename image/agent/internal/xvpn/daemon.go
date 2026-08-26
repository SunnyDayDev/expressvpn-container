package xvpn

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const (
	daemonPath = "/opt/expressvpn/bin/expressvpn-daemon"
	// readyTimeout — спека expressvpn-control: демон готов за 60 с, иначе
	// connection=error с кодом daemon_not_ready.
	ReadyTimeout = 60 * time.Second
)

// Daemon супервизирует процесс expressvpn-daemon: прямой дочерний процесс
// (без init-систем, docs/spikes/S1.md), рестарт с backoff при падении,
// SIGINT при остановке (стоп-сигнал из sysvinit-скрипта ExpressVPN).
type Daemon struct {
	logger  *slog.Logger
	onCrash func()
}

func NewDaemon(logger *slog.Logger, onCrash func()) *Daemon {
	return &Daemon{logger: logger.With("component", "expressvpn"), onCrash: onCrash}
}

// Run запускает и перезапускает демон до отмены контекста.
func (d *Daemon) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		start := time.Now()
		err := d.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		d.logger.Warn("daemon exited, restarting", "error", err, "backoff", backoff.String())
		if d.onCrash != nil {
			d.onCrash()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		// Пожил дольше минуты — считаем запуск успешным, сбрасываем backoff.
		if time.Since(start) > time.Minute {
			backoff = time.Second
		} else if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (d *Daemon) runOnce(ctx context.Context) error {
	cmd := exec.Command(daemonPath)
	cmd.Env = append(os.Environ(), libraryPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	d.logger.Info("daemon started", "pid", cmd.Process.Pid)

	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 256*1024), 256*1024)
		for sc.Scan() {
			d.logger.Debug(sc.Text())
		}
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// Корректная остановка: SIGINT, затем SIGKILL по истечении бюджета.
		_ = cmd.Process.Signal(syscall.SIGINT)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		return nil
	}
}

// WaitReady опрашивает демон, пока тот не начнёт отвечать.
func (d *Daemon) WaitReady(ctx context.Context, cli *CLI, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, err := cli.ConnectionState(cctx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return &CLIError{Code: "daemon_not_ready", Message: "daemon did not become ready in " + timeout.String()}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// Bootstrap приводит демон к базовой конфигурации Detour: фоновый режим
// включён, Network Lock и Split Tunnel выключены (их роль выполняет наш
// kill switch; спеки expressvpn-control и kill-switch).
func (d *Daemon) Bootstrap(ctx context.Context, cli *CLI) error {
	if err := cli.BackgroundEnable(ctx); err != nil {
		return fmt.Errorf("background enable: %w", err)
	}
	if err := cli.Set(ctx, "networklock", "false"); err != nil {
		return fmt.Errorf("disable network lock: %w", err)
	}
	if err := cli.Set(ctx, "splittunnel", "false"); err != nil {
		return fmt.Errorf("disable split tunnel: %w", err)
	}
	d.logger.Info("daemon bootstrapped: background on, network lock off, split tunnel off")
	return nil
}

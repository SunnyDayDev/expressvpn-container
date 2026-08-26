package xvpn

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"

	"detour/agent/internal/state"
)

const (
	ctlPath     = "/opt/expressvpn/bin/expressvpnctl"
	libraryPath = "LD_LIBRARY_PATH=/opt/expressvpn/lib"
	// ctlTimeout — таймаут одноразовых команд (-t) по умолчанию.
	ctlTimeout = 15 * time.Second
)

// Runner выполняет команду и возвращает объединённый вывод и код выхода.
// Абстракция для подмены в тестах.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (out string, exit int, err error)
}

// execRunner — реальный запуск процессов.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), libraryPath)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	exit := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()
		err = nil
	}
	return buf.String(), exit, err
}

// CLI — обёртка над expressvpnctl.
type CLI struct {
	runner Runner
}

func NewCLI() *CLI                  { return &CLI{runner: execRunner{}} }
func NewCLIWithRunner(r Runner) *CLI { return &CLI{runner: r} }

func (c *CLI) run(ctx context.Context, action string, timeout time.Duration, args ...string) (string, error) {
	full := append([]string{"-t", strconv.Itoa(int(timeout.Seconds()))}, args...)
	out, exit, err := c.runner.Run(ctx, ctlPath, full...)
	if err != nil {
		return "", fmt.Errorf("run expressvpnctl %s: %w", action, err)
	}
	if cerr := classifyError(action, out, exit); cerr != nil {
		return "", cerr
	}
	return out, nil
}

func (c *CLI) ConnectionState(ctx context.Context) (state.Connection, error) {
	out, err := c.run(ctx, "get", ctlTimeout, "get", "connectionstate")
	if err != nil {
		return state.ConnDisconnected, err
	}
	return ParseConnectionState(out), nil
}

func (c *CLI) Status(ctx context.Context) (Status, error) {
	out, err := c.run(ctx, "status", ctlTimeout, "status")
	if err != nil {
		return Status{}, err
	}
	return ParseStatus(out), nil
}

func (c *CLI) Get(ctx context.Context, what string) (string, error) {
	out, err := c.run(ctx, "get", ctlTimeout, "get", what)
	if err != nil {
		return "", err
	}
	return ParseValue(out), nil
}

func (c *CLI) Regions(ctx context.Context) ([]string, error) {
	out, err := c.run(ctx, "get", ctlTimeout, "get", "regions")
	if err != nil {
		return nil, err
	}
	return ParseRegions(out), nil
}

func (c *CLI) Smart(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "get", ctlTimeout, "get", "smart")
	if err != nil {
		return "", err
	}
	return ParseSmart(out), nil
}

func (c *CLI) Set(ctx context.Context, what, value string) error {
	_, err := c.run(ctx, "set", ctlTimeout, "set", what, value)
	return err
}

func (c *CLI) BackgroundEnable(ctx context.Context) error {
	_, err := c.run(ctx, "background", ctlTimeout, "background", "enable")
	return err
}

// Connect: локация "" — подключение к региону по умолчанию (smart).
func (c *CLI) Connect(ctx context.Context, location string, timeout time.Duration) error {
	args := []string{"connect"}
	if location != "" {
		args = append(args, location)
	}
	_, err := c.run(ctx, "connect", timeout, args...)
	return err
}

func (c *CLI) Disconnect(ctx context.Context) error {
	_, err := c.run(ctx, "disconnect", ctlTimeout, "disconnect")
	return err
}

// Login записывает код активации во временный файл 0600 и удаляет его сразу
// после вызова; код не попадает ни в аргументы процесса, ни в логи.
func (c *CLI) Login(ctx context.Context, activationCode string) error {
	f, err := os.CreateTemp("", "detour-login-*")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(activationCode + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = c.run(ctx, "login", 60*time.Second, "login", path)
	return err
}

func (c *CLI) Logout(ctx context.Context) error {
	_, err := c.run(ctx, "logout", 30*time.Second, "logout")
	return err
}

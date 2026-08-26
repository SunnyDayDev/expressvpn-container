package xvpn

import (
	"fmt"
	"strings"
)

// CLIError — ошибка вызова expressvpnctl с машинным кодом для API.
type CLIError struct {
	Code    string // invalid_activation_code | unknown_location | not_logged_in | daemon_not_ready | cli_failed
	Message string
	Exit    int
}

func (e *CLIError) Error() string { return fmt.Sprintf("%s: %s (exit %d)", e.Code, e.Message, e.Exit) }

// Наблюдённые коды выхода expressvpnctl (testdata/NOTES.md):
// 1 — ошибка аргументов (неизвестный регион, нет файла), 2 — таймаут демона,
// 5 — требуется вход, 127 — отказ демона ("Request failed").
func classifyError(action, out string, exit int) error {
	if exit == 0 {
		return nil
	}
	msg := strings.TrimSpace(out)
	switch {
	case strings.Contains(out, "Timed out after"):
		return &CLIError{Code: "daemon_not_ready", Message: msg, Exit: exit}
	case strings.Contains(out, "requires a logged in account"):
		return &CLIError{Code: "not_logged_in", Message: msg, Exit: exit}
	case strings.Contains(out, "Cannot find region") || strings.Contains(out, "Unknown region:"):
		return &CLIError{Code: "unknown_location", Message: msg, Exit: exit}
	case action == "login" && (strings.Contains(out, "Unable to log in") || strings.Contains(out, "Request failed")):
		return &CLIError{Code: "invalid_activation_code", Message: msg, Exit: exit}
	default:
		return &CLIError{Code: "cli_failed", Message: msg, Exit: exit}
	}
}

package logs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// Handler — slog.Handler, который пишет записи в Buffer и дублирует их в
// stdout (уже с отредактированными секретами). Компонент берётся из атрибута
// "component" (logger.With("component", "uplink")); по умолчанию — "agent".
type Handler struct {
	buf       *Buffer
	component string
	attrs     []slog.Attr
}

func NewHandler(buf *Buffer) *Handler {
	return &Handler{buf: buf, component: "agent"}
}

func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelDebug
}

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	component := h.component
	var b strings.Builder
	b.WriteString(r.Message)
	appendAttr := func(a slog.Attr) {
		if a.Key == "component" {
			component = a.Value.String()
			return
		}
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
	}
	for _, a := range h.attrs {
		appendAttr(a)
	}
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(a)
		return true
	})

	msg := b.String()
	h.buf.Append(component, strings.ToLower(r.Level.String()), msg)
	fmt.Fprintf(os.Stdout, "%s %-5s [%s] %s\n",
		r.Time.UTC().Format("2006-01-02T15:04:05.000Z"), r.Level, component, h.buf.red.Redact(msg))
	return nil
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	nh := *h
	nh.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	for _, a := range attrs {
		if a.Key == "component" {
			nh.component = a.Value.String()
		}
	}
	return &nh
}

func (h *Handler) WithGroup(string) slog.Handler { return h }

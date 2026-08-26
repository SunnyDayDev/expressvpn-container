package logs

import (
	"strings"
	"sync"
	"time"
)

// Component — источники записей журнала (фильтр /v1/logs?component=).
var Components = []string{"agent", "expressvpn", "uplink", "proxy", "killswitch"}

type Entry struct {
	Time      time.Time `json:"time"`
	Level     string    `json:"level"`
	Component string    `json:"component"`
	Message   string    `json:"message"`
}

// Buffer — кольцевой журнал с подпиской. Секреты редактируются на входе:
// в буфере и у подписчиков они не появляются никогда.
type Buffer struct {
	mu      sync.RWMutex
	entries []Entry
	next    int
	full    bool
	subs    map[chan Entry]struct{}
	red     *Redactor
}

func NewBuffer(capacity int, red *Redactor) *Buffer {
	return &Buffer{
		entries: make([]Entry, capacity),
		subs:    make(map[chan Entry]struct{}),
		red:     red,
	}
}

func (b *Buffer) Redactor() *Redactor { return b.red }

func (b *Buffer) Append(component, level, message string) {
	e := Entry{
		Time:      time.Now().UTC(),
		Level:     level,
		Component: component,
		Message:   b.red.Redact(message),
	}
	b.mu.Lock()
	b.entries[b.next] = e
	b.next = (b.next + 1) % len(b.entries)
	if b.next == 0 {
		b.full = true
	}
	subs := make([]chan Entry, 0, len(b.subs))
	for ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- e:
		default: // отстающий подписчик пропускает запись
		}
	}
}

// Query возвращает до limit последних записей (старые → новые) с фильтрами.
func (b *Buffer) Query(component string, since time.Time, limit int) []Entry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := b.next
	total := n
	if b.full {
		total = len(b.entries)
	}
	out := make([]Entry, 0, min(limit, total))
	// Идём от новых к старым, потом разворачиваем.
	for i := 0; i < total && len(out) < limit; i++ {
		idx := (n - 1 - i + len(b.entries)) % len(b.entries)
		e := b.entries[idx]
		if component != "" && e.Component != component {
			continue
		}
		if !since.IsZero() && e.Time.Before(since) {
			continue
		}
		out = append(out, e)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (b *Buffer) Subscribe() (<-chan Entry, func()) {
	ch := make(chan Entry, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Redactor заменяет зарегистрированные секреты (коды активации, пароли,
// токены) на "***" в любых строках журнала.
type Redactor struct {
	mu      sync.RWMutex
	secrets []string
}

func NewRedactor() *Redactor { return &Redactor{} }

// Add регистрирует секрет; пустые и короткие строки игнорируются, чтобы
// не редактировать случайные совпадения.
func (r *Redactor) Add(secret string) {
	if len(secret) < 4 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.secrets {
		if s == secret {
			return
		}
	}
	r.secrets = append(r.secrets, secret)
}

func (r *Redactor) Redact(s string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, sec := range r.secrets {
		s = strings.ReplaceAll(s, sec, "***")
	}
	return s
}

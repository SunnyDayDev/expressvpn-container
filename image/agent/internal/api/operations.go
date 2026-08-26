package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type OpStatus string

const (
	OpRunning   OpStatus = "running"
	OpSucceeded OpStatus = "succeeded"
	OpFailed    OpStatus = "failed"
)

type Operation struct {
	ID         string     `json:"id"`
	Action     string     `json:"action"`
	Status     OpStatus   `json:"status"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	ErrorCode  string     `json:"errorCode,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// ConflictError — над ресурсом уже выполняется операция (ответ 409).
type ConflictError struct{ RunningID string }

func (e *ConflictError) Error() string { return "conflicting operation " + e.RunningID + " is running" }

// ActionError — ошибка действия с машинным кодом (invalid_activation_code и т.п.).
type ActionError struct {
	Code    string
	Message string
}

func (e *ActionError) Error() string { return e.Message }

// OpManager выполняет действия асинхронно и не допускает параллельных
// операций над одним ресурсом.
type OpManager struct {
	mu         sync.Mutex
	ops        map[string]*Operation
	byResource map[string]string // resource → id выполняющейся операции
}

func NewOpManager() *OpManager {
	return &OpManager{ops: map[string]*Operation{}, byResource: map[string]string{}}
}

// Start запускает действие в фоне. Возвращает ConflictError, если над тем же
// ресурсом уже идёт операция.
func (m *OpManager) Start(action, resource string, run func(ctx context.Context) error) (*Operation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, busy := m.byResource[resource]; busy {
		return nil, &ConflictError{RunningID: id}
	}
	op := &Operation{
		ID:        newOpID(),
		Action:    action,
		Status:    OpRunning,
		StartedAt: time.Now().UTC(),
	}
	m.ops[op.ID] = op
	m.byResource[resource] = op.ID

	go func() {
		err := run(context.Background())
		now := time.Now().UTC()
		m.mu.Lock()
		defer m.mu.Unlock()
		op.FinishedAt = &now
		if err != nil {
			op.Status = OpFailed
			if ae, ok := err.(*ActionError); ok {
				op.ErrorCode = ae.Code
				op.Error = ae.Message
			} else {
				op.ErrorCode = "internal"
				op.Error = err.Error()
			}
		} else {
			op.Status = OpSucceeded
		}
		delete(m.byResource, resource)
	}()
	return op, nil
}

func (m *OpManager) Get(id string) (Operation, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	op, ok := m.ops[id]
	if !ok {
		return Operation{}, false
	}
	return *op, true
}

func newOpID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "op_" + hex.EncodeToString(b)
}

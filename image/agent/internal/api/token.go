package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// TokenStore — Bearer-токен для скриптов: генерируется при первом старте,
// хранится в <dataDir>/auth/token (0600), ротация инвалидирует старый.
type TokenStore struct {
	mu    sync.RWMutex
	path  string
	token string
}

func NewTokenStore(authDir string) (*TokenStore, error) {
	s := &TokenStore{path: filepath.Join(authDir, "token")}
	raw, err := os.ReadFile(s.path)
	switch {
	case err == nil && len(strings.TrimSpace(string(raw))) >= 32:
		s.token = strings.TrimSpace(string(raw))
	case err != nil && !os.IsNotExist(err):
		return nil, err
	default:
		if err := s.generate(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *TokenStore) generate() error {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	token := "dtr_" + hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(s.path, []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	s.token = token
	return nil
}

// Current возвращает действующий токен (для показа в Access).
func (s *TokenStore) Current() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.token
}

// Verify сравнивает предъявленный токен с действующим за константное время.
func (s *TokenStore) Verify(presented string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.token == "" || presented == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(s.token), []byte(presented)) == 1
}

// Rotate генерирует и сохраняет новый токен; старый перестаёт действовать.
func (s *TokenStore) Rotate() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.generate(); err != nil {
		return "", err
	}
	return s.token, nil
}

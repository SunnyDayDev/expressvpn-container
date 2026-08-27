package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

// Параметры argon2id (рекомендации RFC 9106 для интерактивного входа).
const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32

	sessionCookie = "detour_session"
	csrfCookie    = "detour_csrf"
	csrfHeader    = "X-CSRF-Token"

	sessionTTL = 30 * 24 * time.Hour

	// Троттлинг входа: 5 неверных попыток подряд → пауза 30 с (спека).
	throttleAfter = 5
	throttleFor   = 30 * time.Second
)

// SessionManager — пароль администратора (argon2id-хэш в томе) и cookie-сессии.
type SessionManager struct {
	path         string // <dataDir>/auth/password
	disabledPath string // <dataDir>/auth/disabled — защита сознательно выключена

	mu       sync.Mutex
	sessions map[string]sessionInfo
	// pwVersion — отпечаток файла пароля на момент создания сессии: смена или
	// сброс пароля (в т.ч. из другого процесса) инвалидирует прочие сессии.
	failures  int
	blockedTo time.Time
}

type sessionInfo struct {
	expires   time.Time
	pwVersion string
}

func NewSessionManager(authDir string) *SessionManager {
	return &SessionManager{
		path:         filepath.Join(authDir, "password"),
		disabledPath: filepath.Join(authDir, "disabled"),
		sessions:     map[string]sessionInfo{},
	}
}

// HasPassword — задан ли пароль администратора.
func (m *SessionManager) HasPassword() bool {
	_, err := os.Stat(m.path)
	return err == nil
}

// AuthDisabled — защита паролем сознательно выключена (маркер в томе).
// Конфликт «есть и пароль, и маркер» разрешается в пользу пароля.
func (m *SessionManager) AuthDisabled() bool {
	if m.HasPassword() {
		os.Remove(m.disabledPath)
		return false
	}
	_, err := os.Stat(m.disabledPath)
	return err == nil
}

// ErrPasswordSet — операция допустима только пока пароль не задан.
var ErrPasswordSet = fmt.Errorf("password already set")

// Skip выключает защиту на свежем томе (пароль ещё не задавался).
func (m *SessionManager) Skip() error {
	if m.HasPassword() {
		return ErrPasswordSet
	}
	if err := os.MkdirAll(filepath.Dir(m.disabledPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(m.disabledPath, nil, 0o600)
}

// Disable снимает установленный пароль (подтверждение текущим — под общим
// с login троттлингом) и выключает защиту; все сессии сбрасываются.
// Маркер пишется до удаления хэша: при сбое между шагами пароль побеждает.
func (m *SessionManager) Disable(password string) (bool, error) {
	ok, err := m.VerifyPassword(password)
	if err != nil || !ok {
		return ok, err
	}
	if err := os.WriteFile(m.disabledPath, nil, 0o600); err != nil {
		return false, err
	}
	if err := os.Remove(m.path); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	m.DropAllSessions()
	return true, nil
}

func (m *SessionManager) pwVersion() string {
	fi, err := os.Stat(m.path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", fi.ModTime().UnixNano(), fi.Size())
}

// SetPassword сохраняет argon2id-хэш (PHC-строка) с правами 0600.
func (m *SessionManager) SetPassword(password string) error {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	phc := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(m.path, []byte(phc+"\n"), 0o600); err != nil {
		return err
	}
	// Установка пароля включает защиту обратно.
	if err := os.Remove(m.disabledPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// VerifyPassword сверяет пароль с хэшем; учитывает троттлинг.
func (m *SessionManager) VerifyPassword(password string) (bool, error) {
	m.mu.Lock()
	if time.Now().Before(m.blockedTo) {
		m.mu.Unlock()
		return false, ErrThrottled
	}
	m.mu.Unlock()

	raw, err := os.ReadFile(m.path)
	if err != nil {
		return false, err
	}
	ok := verifyPHC(strings.TrimSpace(string(raw)), password)

	m.mu.Lock()
	defer m.mu.Unlock()
	if ok {
		m.failures = 0
		return true, nil
	}
	m.failures++
	if m.failures >= throttleAfter {
		m.blockedTo = time.Now().Add(throttleFor)
		m.failures = 0
	}
	return false, nil
}

// ErrThrottled — слишком много неверных попыток.
var ErrThrottled = fmt.Errorf("too many attempts")

func verifyPHC(phc, password string) bool {
	parts := strings.Split(phc, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var mem, tm uint32
	var par uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &tm, &par); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, tm, mem, par, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// CreateSession возвращает токены сессии и CSRF.
func (m *SessionManager) CreateSession() (session, csrf string) {
	b := make([]byte, 32)
	rand.Read(b)
	session = hex.EncodeToString(b)
	rand.Read(b)
	csrf = hex.EncodeToString(b)

	m.mu.Lock()
	m.sessions[session] = sessionInfo{
		expires:   time.Now().Add(sessionTTL),
		pwVersion: m.pwVersion(),
	}
	m.mu.Unlock()
	return session, csrf
}

func (m *SessionManager) DropSession(session string) {
	m.mu.Lock()
	delete(m.sessions, session)
	m.mu.Unlock()
}

// DropAllSessions инвалидирует все сессии (logout-all, смена пароля).
func (m *SessionManager) DropAllSessions() {
	m.mu.Lock()
	m.sessions = map[string]sessionInfo{}
	m.mu.Unlock()
}

// VerifyRequest — реализация api.SessionVerifier: валидная cookie-сессия и,
// для мутирующих методов, совпадающий CSRF-токен (double submit).
func (m *SessionManager) VerifyRequest(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	m.mu.Lock()
	info, ok := m.sessions[c.Value]
	m.mu.Unlock()
	if !ok || time.Now().After(info.expires) {
		return false
	}
	// Сброс/смена пароля (в т.ч. reset-password из другого процесса)
	// инвалидирует сессии, созданные до неё.
	if info.pwVersion != m.pwVersion() {
		m.mu.Lock()
		delete(m.sessions, c.Value)
		m.mu.Unlock()
		return false
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	csrf, err := r.Cookie(csrfCookie)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(csrf.Value), []byte(r.Header.Get(csrfHeader))) == 1
}

// setSessionCookies выставляет cookie сессии (HTTP-only) и CSRF (читаемую JS).
func setSessionCookies(w http.ResponseWriter, session, csrf string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: session, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: csrf, Path: "/",
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func clearSessionCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: "", Path: "/", MaxAge: -1})
}

// ResetPassword — команда `detour-agent reset-password` (запускается с хоста
// отдельным процессом): удаляет хэш и маркер выключенной защиты — состояние
// возвращается в «свежий том»; работающий агент заметит смену версии файла
// и отбросит все сессии.
func ResetPassword(authDir string) error {
	if err := os.Remove(filepath.Join(authDir, "password")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(filepath.Join(authDir, "disabled")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

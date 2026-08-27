package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const minPasswordLen = 8

func readJSONBody(w http.ResponseWriter, r *http.Request, v any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body"})
		return false
	}
	if len(raw) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty body"})
		return false
	}
	if err := json.Unmarshal(raw, v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return false
	}
	return true
}

// GET /v1/auth/status — публичный: UI решает, показывать setup, login или app.
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	disabled := s.deps.Sessions.AuthDisabled()
	writeJSON(w, http.StatusOK, map[string]bool{
		"passwordSet":   s.deps.Sessions.HasPassword(),
		"authDisabled":  disabled,
		"authenticated": disabled || s.authorized(r),
	})
}

// POST /v1/auth/setup — создание пароля администратора (пока не задан:
// свежий том или выключенная защита; во втором случае включает защиту).
func (s *Server) handleAuthSetup(w http.ResponseWriter, r *http.Request) {
	if s.deps.Sessions.AuthDisabled() && crossSiteBlocked(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross_site_blocked"})
		return
	}
	if s.deps.Sessions.HasPassword() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "password already set"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readJSONBody(w, r, &body) {
		return
	}
	if len(body.Password) < minPasswordLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "password_too_short", "message": "minimum 8 characters",
		})
		return
	}
	if err := s.deps.Sessions.SetPassword(body.Password); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	session, csrf := s.deps.Sessions.CreateSession()
	setSessionCookies(w, session, csrf)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /v1/auth/skip — выключает защиту на свежем томе (только пока пароль
// ещё не задавался и защита не выключена ранее).
func (s *Server) handleAuthSkip(w http.ResponseWriter, r *http.Request) {
	if s.deps.Sessions.HasPassword() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "password already set"})
		return
	}
	if s.deps.Sessions.AuthDisabled() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already disabled"})
		return
	}
	if err := s.deps.Sessions.Skip(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /v1/auth/disable — снимает установленный пароль (подтверждение текущим,
// общий с login троттлинг) и выключает защиту; все сессии сбрасываются.
func (s *Server) handleAuthDisable(w http.ResponseWriter, r *http.Request) {
	if !s.deps.Sessions.HasPassword() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no password set"})
		return
	}
	var body struct {
		Current string `json:"current"`
	}
	if !readJSONBody(w, r, &body) {
		return
	}
	ok, err := s.deps.Sessions.Disable(body.Current)
	if errors.Is(err, ErrThrottled) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_attempts"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong_password"})
		return
	}
	clearSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /v1/auth/login — пароль → HTTP-only cookie-сессия.
func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if !s.deps.Sessions.HasPassword() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "setup_required"})
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readJSONBody(w, r, &body) {
		return
	}
	ok, err := s.deps.Sessions.VerifyPassword(body.Password)
	if errors.Is(err, ErrThrottled) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_attempts"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong_password"})
		return
	}
	session, csrf := s.deps.Sessions.CreateSession()
	setSessionCookies(w, session, csrf)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /v1/auth/logout — завершает текущую сессию.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.deps.Sessions.DropSession(c.Value)
	}
	clearSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /v1/auth/logout-all — инвалидирует все сессии, включая текущую.
func (s *Server) handleAuthLogoutAll(w http.ResponseWriter, r *http.Request) {
	s.deps.Sessions.DropAllSessions()
	clearSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /v1/auth/password — смена пароля; прочие сессии инвалидируются
// (версия файла пароля меняется), текущий клиент получает новую сессию.
func (s *Server) handleAuthPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSONBody(w, r, &body) {
		return
	}
	ok, err := s.deps.Sessions.VerifyPassword(body.Current)
	if errors.Is(err, ErrThrottled) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too_many_attempts"})
		return
	}
	if err != nil || !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "wrong_password"})
		return
	}
	if len(body.New) < minPasswordLen {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "password_too_short", "message": "minimum 8 characters",
		})
		return
	}
	if err := s.deps.Sessions.SetPassword(body.New); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	session, csrf := s.deps.Sessions.CreateSession()
	setSessionCookies(w, session, csrf)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

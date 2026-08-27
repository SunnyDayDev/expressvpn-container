package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"detour/agent/internal/config"
	"detour/agent/internal/logs"
	"detour/agent/internal/state"
)

// Actions — действия /v1/actions/*; реализуются реконсайлером и драйверами.
type Actions interface {
	Connect(ctx context.Context, location string) error
	Disconnect(ctx context.Context) error
	Reconnect(ctx context.Context) error
	Login(ctx context.Context, activationCode string) error
	Logout(ctx context.Context) error
	RefreshLocations(ctx context.Context) error
	Selfcheck(ctx context.Context) error
	ProbeUplink(ctx context.Context) error
	Locations(ctx context.Context) (any, error)
}

type VersionInfo struct {
	Agent      string   `json:"agent"`
	Image      string   `json:"image"`
	ExpressVPN string   `json:"expressvpn"`
	Uplink     string   `json:"uplinkEngine"`
	Protocols  []string `json:"protocols"`
}

type Deps struct {
	State    *state.Store
	Config   *config.Store
	Tokens   *TokenStore
	Logs     *logs.Buffer
	Ops      *OpManager
	Actions  Actions
	Sessions *SessionManager
	Version  VersionInfo
	Logger   *slog.Logger
	// WebFS — встроенная статика веб-интерфейса (nil — только API).
	WebFS fs.FS
}

// Server — HTTP-сервер агента: веб-интерфейс, /healthz и API /v1 на одном порту.
type Server struct {
	http *http.Server
	deps Deps
}

const (
	heartbeatInterval = 15 * time.Second
	defaultLogLimit   = 500
)

func New(addr string, deps Deps) *Server {
	s := &Server{deps: deps}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)

	authed := func(h http.HandlerFunc) http.Handler { return s.requireAuth(h) }
	mux.Handle("GET /v1/state", authed(s.handleState))
	mux.Handle("GET /v1/events", authed(s.handleEvents))
	mux.Handle("GET /v1/config", authed(s.handleConfigGet))
	mux.Handle("PATCH /v1/config", authed(s.handleConfigPatch))
	mux.Handle("GET /v1/version", authed(s.handleVersion))
	mux.Handle("POST /v1/actions/{name}", authed(s.handleAction))
	mux.Handle("GET /v1/operations/{id}", authed(s.handleOperation))
	mux.Handle("GET /v1/locations", authed(s.handleLocations))
	mux.Handle("GET /v1/logs", authed(s.handleLogs))
	mux.Handle("GET /v1/logs/stream", authed(s.handleLogsStream))
	mux.Handle("GET /v1/auth/token", authed(s.handleTokenGet))
	mux.Handle("POST /v1/auth/token/rotate", authed(s.handleTokenRotate))
	mux.Handle("GET /v1/diagnostics/archive", authed(s.handleDiagnosticsArchive))

	if deps.Sessions != nil {
		mux.HandleFunc("GET /v1/auth/status", s.handleAuthStatus)
		mux.HandleFunc("POST /v1/auth/setup", s.handleAuthSetup)
		mux.HandleFunc("POST /v1/auth/skip", s.handleAuthSkip)
		mux.Handle("POST /v1/auth/disable", authed(s.handleAuthDisable))
		mux.HandleFunc("POST /v1/auth/login", s.handleAuthLogin)
		mux.Handle("POST /v1/auth/logout", authed(s.handleAuthLogout))
		mux.Handle("POST /v1/auth/logout-all", authed(s.handleAuthLogoutAll))
		mux.Handle("POST /v1/auth/password", authed(s.handleAuthPassword))
	}

	// SPA: статика с фолбэком на index.html (клиентский роутинг).
	if deps.WebFS != nil {
		mux.Handle("/", spaHandler(deps.WebFS))
	}

	s.http = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// Run обслуживает запросы, пока не вызван Shutdown (или сервер не упал сам).
func (s *Server) Run(ctx context.Context) error {
	s.deps.Logger.Info("http server listening", "addr", s.http.Addr)
	err := s.http.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

// --- аутентификация ---

func (s *Server) requireAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Sessions != nil && s.deps.Sessions.AuthDisabled() {
			if crossSiteBlocked(r) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross_site_blocked"})
				return
			}
			next(w, r)
			return
		}
		if s.authorized(r) {
			next(w, r)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	})
}

// crossSiteBlocked — щит для мутирующих запросов при выключенной защите:
// без cookie+CSRF любой сайт в браузере пользователя может слать «слепые»
// POST/PATCH на адрес агента (fetch no-cors). Браузерные запросы распознаются
// по Sec-Fetch-Site/Origin; клиенты без этих заголовков (curl, скрипты)
// проходят свободно.
func crossSiteBlocked(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	// Sec-Fetch-Site надёжнее Origin за reverse-proxy (Host мог быть переписан).
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "cross-site"
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err != nil || !strings.EqualFold(u.Host, r.Host)
	}
	return false
}

func (s *Server) authorized(r *http.Request) bool {
	if h := r.Header.Get("Authorization"); h != "" {
		if token, ok := strings.CutPrefix(h, "Bearer "); ok && s.deps.Tokens.Verify(strings.TrimSpace(token)) {
			return true
		}
		return false
	}
	if s.deps.Sessions != nil {
		return s.deps.Sessions.VerifyRequest(r)
	}
	return false
}

// --- обработчики ---

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.State.Get())
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.Version)
}

func (s *Server) handleConfigGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, config.Redacted(s.deps.Config.Get()))
}

func (s *Server) handleConfigPatch(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read body: " + err.Error()})
		return
	}
	applied, err := s.deps.Config.ApplyMergePatch(body)
	if err != nil {
		var fe *config.FieldError
		if errors.As(err, &fe) {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "validation", "field": fe.Field, "message": fe.Msg,
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"applied":          applied,
		"requiresRecreate": []string{},
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	ch, cancel := s.deps.State.Subscribe()
	defer cancel()
	hb := time.NewTicker(heartbeatInterval)
	defer hb.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case st := <-ch:
			raw, err := json.Marshal(st)
			if err != nil {
				return
			}
			fmt.Fprintf(w, "event: state\ndata: %s\n\n", raw)
			fl.Flush()
		case <-hb.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			fl.Flush()
		}
	}
}

// actionResources: конфликтующие действия делят один ресурс (409 при гонке).
var actionResources = map[string]string{
	"connect":           "connection",
	"disconnect":        "connection",
	"reconnect":         "connection",
	"login":             "account",
	"logout":            "account",
	"refresh-locations": "locations",
	"selfcheck":         "selfcheck",
	"probe-uplink":      "uplink",
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	resource, known := actionResources[name]
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown action " + name})
		return
	}
	var body struct {
		Location       string `json:"location"`
		ActivationCode string `json:"activationCode"`
	}
	if r.Body != nil {
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err == nil && len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
				return
			}
		}
	}
	if body.ActivationCode != "" {
		// Код активации не должен появляться ни в логах, ни в диагностике.
		s.deps.Logs.Redactor().Add(body.ActivationCode)
	}

	a := s.deps.Actions
	if a == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "actions unavailable"})
		return
	}
	var run func(ctx context.Context) error
	switch name {
	case "connect":
		run = func(ctx context.Context) error { return a.Connect(ctx, body.Location) }
	case "disconnect":
		run = a.Disconnect
	case "reconnect":
		run = a.Reconnect
	case "login":
		run = func(ctx context.Context) error { return a.Login(ctx, body.ActivationCode) }
	case "logout":
		run = a.Logout
	case "refresh-locations":
		run = a.RefreshLocations
	case "selfcheck":
		run = a.Selfcheck
	case "probe-uplink":
		run = a.ProbeUplink
	}

	op, err := s.deps.Ops.Start(name, resource, run)
	if err != nil {
		var ce *ConflictError
		if errors.As(err, &ce) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "conflict", "operation": ce.RunningID,
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation": op.ID})
}

func (s *Server) handleOperation(w http.ResponseWriter, r *http.Request) {
	op, ok := s.deps.Ops.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown operation"})
		return
	}
	writeJSON(w, http.StatusOK, op)
}

func (s *Server) handleLocations(w http.ResponseWriter, r *http.Request) {
	if s.deps.Actions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "actions unavailable"})
		return
	}
	locs, err := s.deps.Actions.Locations(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, locs)
}

func parseLogsQuery(r *http.Request) (component string, since time.Time, limit int, err error) {
	q := r.URL.Query()
	component = q.Get("component")
	if component != "" {
		ok := false
		for _, c := range logs.Components {
			if c == component {
				ok = true
				break
			}
		}
		if !ok {
			return "", time.Time{}, 0, fmt.Errorf("unknown component %q", component)
		}
	}
	if v := q.Get("since"); v != "" {
		since, err = time.Parse(time.RFC3339, v)
		if err != nil {
			return "", time.Time{}, 0, fmt.Errorf("since must be RFC3339")
		}
	}
	limit = defaultLogLimit
	if v := q.Get("limit"); v != "" {
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > 5000 {
			return "", time.Time{}, 0, fmt.Errorf("limit must be 1..5000")
		}
	}
	return component, since, limit, nil
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	component, since, limit, err := parseLogsQuery(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": s.deps.Logs.Query(component, since, limit),
	})
}

func (s *Server) handleLogsStream(w http.ResponseWriter, r *http.Request) {
	component, _, _, err := parseLogsQuery(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	ch, cancel := s.deps.Logs.Subscribe()
	defer cancel()
	hb := time.NewTicker(heartbeatInterval)
	defer hb.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			if component != "" && e.Component != component {
				continue
			}
			raw, _ := json.Marshal(e)
			fmt.Fprintf(w, "event: log\ndata: %s\n\n", raw)
			fl.Flush()
		case <-hb.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			fl.Flush()
		}
	}
}

func (s *Server) handleTokenGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"token": s.deps.Tokens.Current()})
}

func (s *Server) handleTokenRotate(w http.ResponseWriter, _ *http.Request) {
	token, err := s.deps.Tokens.Rotate()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.Encode(v)
}

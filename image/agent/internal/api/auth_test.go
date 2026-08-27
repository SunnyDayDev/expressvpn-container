package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"detour/agent/internal/config"
	"detour/agent/internal/logs"
	"detour/agent/internal/state"
)

type authEnv struct {
	srv      *httptest.Server
	sessions *SessionManager
	authDir  string
}

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.NewStore(dir + "/config.json")
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := NewTokenStore(dir + "/auth")
	if err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionManager(dir + "/auth")
	s := New("127.0.0.1:0", Deps{
		State:    state.NewStore(state.Initial("test")),
		Config:   cfg,
		Tokens:   tokens,
		Logs:     logs.NewBuffer(10, logs.NewRedactor()),
		Ops:      NewOpManager(),
		Sessions: sessions,
		Logger:   slog.New(slog.DiscardHandler),
	})
	ts := httptest.NewServer(s.http.Handler)
	t.Cleanup(ts.Close)
	return &authEnv{srv: ts, sessions: sessions, authDir: dir + "/auth"}
}

// client — HTTP-клиент с cookie jar (браузерная сессия).
func (e *authEnv) client(t *testing.T) *http.Client {
	t.Helper()
	jar := newJar()
	return &http.Client{Jar: jar}
}

type jar struct{ cookies map[string][]*http.Cookie }

func newJar() *jar { return &jar{cookies: map[string][]*http.Cookie{}} }

func (j *jar) SetCookies(u *url.URL, cs []*http.Cookie) {
	existing := j.cookies[u.Host]
	for _, c := range cs {
		found := false
		for i, e := range existing {
			if e.Name == c.Name {
				existing[i] = c
				found = true
			}
		}
		if !found {
			existing = append(existing, c)
		}
	}
	j.cookies[u.Host] = existing
}

func (j *jar) Cookies(u *url.URL) []*http.Cookie {
	var out []*http.Cookie
	for _, c := range j.cookies[u.Host] {
		if c.MaxAge >= 0 && c.Value != "" {
			out = append(out, c)
		}
	}
	return out
}

func (e *authEnv) post(t *testing.T, c *http.Client, path, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+path, strings.NewReader(body))
	// CSRF double submit: заголовок из cookie.
	u, _ := url.Parse(e.srv.URL)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == csrfCookie {
			req.Header.Set(csrfHeader, ck.Value)
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (e *authEnv) get(t *testing.T, c *http.Client, path string) *http.Response {
	t.Helper()
	resp, err := c.Get(e.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestAuthFullFlow(t *testing.T) {
	e := newAuthEnv(t)
	c := e.client(t)

	// Пока пароль не задан: login → 409 setup_required, статус passwordSet=false.
	resp := e.get(t, c, "/v1/auth/status")
	var st map[string]bool
	json.NewDecoder(resp.Body).Decode(&st)
	if st["passwordSet"] || st["authenticated"] {
		t.Fatalf("fresh status=%v", st)
	}
	if resp := e.post(t, c, "/v1/auth/login", `{"password":"whatever1"}`); resp.StatusCode != 409 {
		t.Fatalf("login before setup: %d", resp.StatusCode)
	}

	// Setup создаёт пароль и сразу даёт сессию.
	if resp := e.post(t, c, "/v1/auth/setup", `{"password":"correct-horse"}`); resp.StatusCode != 200 {
		t.Fatalf("setup: %d", resp.StatusCode)
	}
	if resp := e.get(t, c, "/v1/state"); resp.StatusCode != 200 {
		t.Fatalf("state with session: %d", resp.StatusCode)
	}

	// Повторный setup → 409.
	if resp := e.post(t, c, "/v1/auth/setup", `{"password":"another-pass"}`); resp.StatusCode != 409 {
		t.Fatalf("second setup: %d", resp.StatusCode)
	}

	// Logout: сессия недействительна.
	if resp := e.post(t, c, "/v1/auth/logout", `{}`); resp.StatusCode != 200 {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp := e.get(t, c, "/v1/state"); resp.StatusCode != 401 {
		t.Fatalf("state after logout: %d", resp.StatusCode)
	}

	// Login обратно.
	if resp := e.post(t, c, "/v1/auth/login", `{"password":"correct-horse"}`); resp.StatusCode != 200 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	if resp := e.get(t, c, "/v1/state"); resp.StatusCode != 200 {
		t.Fatalf("state after login: %d", resp.StatusCode)
	}
}

func TestLoginThrottleAfterFiveFailures(t *testing.T) {
	e := newAuthEnv(t)
	c := e.client(t)
	e.post(t, c, "/v1/auth/setup", `{"password":"correct-horse"}`)

	for i := 0; i < 5; i++ {
		resp := e.post(t, c, "/v1/auth/login", `{"password":"wrong-pass"}`)
		if resp.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i+1, resp.StatusCode)
		}
	}
	// Шестая попытка (даже с верным паролем) — 429.
	if resp := e.post(t, c, "/v1/auth/login", `{"password":"correct-horse"}`); resp.StatusCode != 429 {
		t.Fatalf("throttled attempt: %d want 429", resp.StatusCode)
	}
}

func TestPasswordChangeInvalidatesOtherSessions(t *testing.T) {
	e := newAuthEnv(t)
	c1 := e.client(t)
	e.post(t, c1, "/v1/auth/setup", `{"password":"correct-horse"}`)

	c2 := e.client(t)
	e.post(t, c2, "/v1/auth/login", `{"password":"correct-horse"}`)
	if resp := e.get(t, c2, "/v1/state"); resp.StatusCode != 200 {
		t.Fatal("second session must work before change")
	}

	// c1 меняет пароль → сессия c2 умирает, c1 живёт (новая сессия).
	if resp := e.post(t, c1, "/v1/auth/password", `{"current":"correct-horse","new":"brand-new-pass"}`); resp.StatusCode != 200 {
		t.Fatalf("password change: %d", resp.StatusCode)
	}
	if resp := e.get(t, c2, "/v1/state"); resp.StatusCode != 401 {
		t.Fatalf("old session after change: %d want 401", resp.StatusCode)
	}
	if resp := e.get(t, c1, "/v1/state"); resp.StatusCode != 200 {
		t.Fatalf("changer session: %d want 200", resp.StatusCode)
	}
}

func TestResetPasswordClearsHashAndSessions(t *testing.T) {
	e := newAuthEnv(t)
	c := e.client(t)
	e.post(t, c, "/v1/auth/setup", `{"password":"correct-horse"}`)
	if resp := e.get(t, c, "/v1/state"); resp.StatusCode != 200 {
		t.Fatal("session must work")
	}

	// Сброс «с хоста» (отдельный процесс — просто вызов функции).
	if err := ResetPassword(e.authDir); err != nil {
		t.Fatal(err)
	}
	if e.sessions.HasPassword() {
		t.Fatal("hash not cleared")
	}
	if resp := e.get(t, c, "/v1/state"); resp.StatusCode != 401 {
		t.Fatalf("session survived reset: %d", resp.StatusCode)
	}
	// UI снова предлагает создать пароль.
	resp := e.get(t, c, "/v1/auth/status")
	var st map[string]bool
	json.NewDecoder(resp.Body).Decode(&st)
	if st["passwordSet"] {
		t.Fatal("passwordSet must be false after reset")
	}
}

func (e *authEnv) status(t *testing.T, c *http.Client) map[string]bool {
	t.Helper()
	resp := e.get(t, c, "/v1/auth/status")
	var st map[string]bool
	json.NewDecoder(resp.Body).Decode(&st)
	return st
}

func TestSkipDisablesAuthOnFreshVolume(t *testing.T) {
	e := newAuthEnv(t)
	c := e.client(t)

	if resp := e.post(t, c, "/v1/auth/skip", `{}`); resp.StatusCode != 200 {
		t.Fatalf("skip: %d", resp.StatusCode)
	}
	st := e.status(t, c)
	if st["passwordSet"] || !st["authDisabled"] || !st["authenticated"] {
		t.Fatalf("status after skip=%v", st)
	}
	// Все запросы проходят без сессии, токена и CSRF.
	if resp := e.get(t, c, "/v1/state"); resp.StatusCode != 200 {
		t.Fatalf("state while disabled: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("PATCH", e.srv.URL+"/v1/config", strings.NewReader(`{"expressvpn":{"protocol":"auto"}}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("mutation while disabled: %d", resp.StatusCode)
	}
	// Повторный skip — состояние уже не unset.
	if resp := e.post(t, c, "/v1/auth/skip", `{}`); resp.StatusCode != 409 {
		t.Fatalf("double skip: %d want 409", resp.StatusCode)
	}
}

func TestSkipUnavailableOncePasswordSet(t *testing.T) {
	e := newAuthEnv(t)
	c := e.client(t)
	e.post(t, c, "/v1/auth/setup", `{"password":"correct-horse"}`)

	if resp := e.post(t, c, "/v1/auth/skip", `{}`); resp.StatusCode != 409 {
		t.Fatalf("skip after setup: %d want 409", resp.StatusCode)
	}
	if e.sessions.AuthDisabled() {
		t.Fatal("protection must stay on")
	}
}

func TestDisableRequiresCurrentPassword(t *testing.T) {
	e := newAuthEnv(t)
	c := e.client(t)
	e.post(t, c, "/v1/auth/setup", `{"password":"correct-horse"}`)

	if resp := e.post(t, c, "/v1/auth/disable", `{"current":"wrong-pass"}`); resp.StatusCode != 401 {
		t.Fatalf("disable with wrong password: %d want 401", resp.StatusCode)
	}
	// Защита на месте: свежий клиент без сессии получает 401.
	if resp := e.get(t, e.client(t), "/v1/state"); resp.StatusCode != 401 {
		t.Fatal("protection must stay on after failed disable")
	}

	if resp := e.post(t, c, "/v1/auth/disable", `{"current":"correct-horse"}`); resp.StatusCode != 200 {
		t.Fatalf("disable: %d", resp.StatusCode)
	}
	// Теперь открыто для всех.
	if resp := e.get(t, e.client(t), "/v1/state"); resp.StatusCode != 200 {
		t.Fatal("state must be open after disable")
	}
	st := e.status(t, e.client(t))
	if st["passwordSet"] || !st["authDisabled"] {
		t.Fatalf("status after disable=%v", st)
	}
}

func TestDisableSharesLoginThrottle(t *testing.T) {
	e := newAuthEnv(t)
	c := e.client(t)
	e.post(t, c, "/v1/auth/setup", `{"password":"correct-horse"}`)

	// 3 неверных login + 2 неверных disable → общий счётчик добит до 5.
	for i := 0; i < 3; i++ {
		e.post(t, c, "/v1/auth/login", `{"password":"wrong-pass"}`)
	}
	for i := 0; i < 2; i++ {
		e.post(t, c, "/v1/auth/disable", `{"current":"wrong-pass"}`)
	}
	if resp := e.post(t, c, "/v1/auth/disable", `{"current":"correct-horse"}`); resp.StatusCode != 429 {
		t.Fatalf("throttled disable: %d want 429", resp.StatusCode)
	}
}

func TestSetupReenablesAfterDisable(t *testing.T) {
	e := newAuthEnv(t)
	c := e.client(t)
	e.post(t, c, "/v1/auth/skip", `{}`)

	if resp := e.post(t, c, "/v1/auth/setup", `{"password":"brand-new-pass"}`); resp.StatusCode != 200 {
		t.Fatalf("setup after skip: %d", resp.StatusCode)
	}
	// Создатель пароля получил сессию, посторонние — 401.
	if resp := e.get(t, c, "/v1/state"); resp.StatusCode != 200 {
		t.Fatalf("creator session: %d", resp.StatusCode)
	}
	if resp := e.get(t, e.client(t), "/v1/state"); resp.StatusCode != 401 {
		t.Fatal("protection must be on after setup")
	}
	st := e.status(t, c)
	if !st["passwordSet"] || st["authDisabled"] {
		t.Fatalf("status after re-enable=%v", st)
	}
}

func TestCrossSiteMutationsBlockedWhileDisabled(t *testing.T) {
	e := newAuthEnv(t)
	c := e.client(t)
	e.post(t, c, "/v1/auth/skip", `{}`)

	patch := func(hdr map[string]string) int {
		req, _ := http.NewRequest("PATCH", e.srv.URL+"/v1/config", strings.NewReader(`{"expressvpn":{"protocol":"auto"}}`))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode
	}
	u, _ := url.Parse(e.srv.URL)

	// Чужой Origin и явный cross-site — блок.
	if code := patch(map[string]string{"Origin": "http://evil.example"}); code != 403 {
		t.Fatalf("cross-origin mutation: %d want 403", code)
	}
	if code := patch(map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://" + u.Host}); code != 403 {
		t.Fatalf("sec-fetch-site cross-site: %d want 403", code)
	}
	// Same-origin браузер и клиенты без заголовков — проходят.
	if code := patch(map[string]string{"Origin": "http://" + u.Host}); code != 200 {
		t.Fatalf("same-origin mutation: %d want 200", code)
	}
	if code := patch(map[string]string{"Sec-Fetch-Site": "same-origin"}); code != 200 {
		t.Fatalf("sec-fetch-site same-origin: %d want 200", code)
	}
	if code := patch(nil); code != 200 {
		t.Fatalf("headerless mutation: %d want 200", code)
	}
	// GET не ограничивается даже с чужим Origin.
	req, _ := http.NewRequest("GET", e.srv.URL+"/v1/state", nil)
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("cross-origin GET: %d want 200", resp.StatusCode)
	}
	// Setup с чужого сайта при выключенной защите — блок (lockout-защита).
	req2, _ := http.NewRequest("POST", e.srv.URL+"/v1/auth/setup", strings.NewReader(`{"password":"evil-password"}`))
	req2.Header.Set("Origin", "http://evil.example")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.StatusCode != 403 {
		t.Fatalf("cross-site setup while disabled: %d want 403", resp2.StatusCode)
	}
}

func TestResetPasswordClearsDisabledMarker(t *testing.T) {
	e := newAuthEnv(t)
	c := e.client(t)
	e.post(t, c, "/v1/auth/skip", `{}`)

	if err := ResetPassword(e.authDir); err != nil {
		t.Fatal(err)
	}
	st := e.status(t, c)
	if st["passwordSet"] || st["authDisabled"] {
		t.Fatalf("status after reset=%v (want fresh volume)", st)
	}
	if resp := e.get(t, c, "/v1/state"); resp.StatusCode != 401 {
		t.Fatal("auth must be required again after reset")
	}
}

func TestCSRFRequiredForMutations(t *testing.T) {
	e := newAuthEnv(t)
	c := e.client(t)
	e.post(t, c, "/v1/auth/setup", `{"password":"correct-horse"}`)

	// PATCH без CSRF-заголовка при cookie-аутентификации → 401.
	req, _ := http.NewRequest("PATCH", e.srv.URL+"/v1/config", strings.NewReader(`{"expressvpn":{"protocol":"auto"}}`))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("mutation without CSRF: %d want 401", resp.StatusCode)
	}

	// С заголовком — проходит.
	u, _ := url.Parse(e.srv.URL)
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == csrfCookie {
			req2, _ := http.NewRequest("PATCH", e.srv.URL+"/v1/config", strings.NewReader(`{"expressvpn":{"protocol":"auto"}}`))
			req2.Header.Set(csrfHeader, ck.Value)
			resp2, err := c.Do(req2)
			if err != nil {
				t.Fatal(err)
			}
			if resp2.StatusCode != 200 {
				t.Fatalf("mutation with CSRF: %d", resp2.StatusCode)
			}
			return
		}
	}
	t.Fatal("csrf cookie not found")
}

package api

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"detour/agent/internal/config"
	"detour/agent/internal/logs"
	"detour/agent/internal/state"
)

type fakeActions struct {
	mu       sync.Mutex
	block    chan struct{} // если задан, Connect ждёт закрытия
	connects int
}

func (f *fakeActions) Connect(ctx context.Context, location string) error {
	f.mu.Lock()
	f.connects++
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return nil
}
func (f *fakeActions) Disconnect(context.Context) error            { return nil }
func (f *fakeActions) Reconnect(context.Context) error             { return nil }
func (f *fakeActions) Login(_ context.Context, code string) error  { return nil }
func (f *fakeActions) Logout(context.Context) error                { return nil }
func (f *fakeActions) RefreshLocations(context.Context) error      { return nil }
func (f *fakeActions) Selfcheck(context.Context) error             { return nil }
func (f *fakeActions) ProbeUplink(context.Context) error           { return nil }
func (f *fakeActions) Locations(context.Context) (any, error)      { return []string{}, nil }

type testEnv struct {
	srv    *httptest.Server
	tokens *TokenStore
	state  *state.Store
	logs   *logs.Buffer
	acts   *fakeActions
}

func newTestEnv(t *testing.T) *testEnv {
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
	red := logs.NewRedactor()
	buf := logs.NewBuffer(100, red)
	st := state.NewStore(state.Initial("test"))
	acts := &fakeActions{}
	s := New("127.0.0.1:0", Deps{
		State: st, Config: cfg, Tokens: tokens, Logs: buf,
		Ops: NewOpManager(), Actions: acts,
		Version: VersionInfo{Agent: "test", Protocols: config.KnownProtocols},
		Logger:  slog.New(slog.DiscardHandler),
	})
	ts := httptest.NewServer(s.http.Handler)
	t.Cleanup(ts.Close)
	return &testEnv{srv: ts, tokens: tokens, state: st, logs: buf, acts: acts}
}

func (e *testEnv) req(t *testing.T, method, path, token, body string) *http.Response {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHealthzPublic(t *testing.T) {
	e := newTestEnv(t)
	resp := e.req(t, "GET", "/healthz", "", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestUnauthorized401(t *testing.T) {
	e := newTestEnv(t)
	for _, path := range []string{"/v1/state", "/v1/config", "/v1/version", "/v1/logs"} {
		resp := e.req(t, "GET", path, "", "")
		if resp.StatusCode != 401 {
			t.Fatalf("%s: status=%d want 401", path, resp.StatusCode)
		}
		var body map[string]string
		json.NewDecoder(resp.Body).Decode(&body)
		if body["error"] != "unauthorized" {
			t.Fatalf("%s: body=%v", path, body)
		}
	}
	if resp := e.req(t, "GET", "/v1/state", "wrong-token", ""); resp.StatusCode != 401 {
		t.Fatalf("wrong token: %d", resp.StatusCode)
	}
}

func TestStateWithBearer(t *testing.T) {
	e := newTestEnv(t)
	resp := e.req(t, "GET", "/v1/state", e.tokens.Current(), "")
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var st state.State
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Uplink.Mode != "host" {
		t.Fatalf("mode=%q", st.Uplink.Mode)
	}
}

func TestConfigPatchInvalidModeAtomic(t *testing.T) {
	e := newTestEnv(t)
	tok := e.tokens.Current()
	resp := e.req(t, "PATCH", "/v1/config", tok,
		`{"expressvpn":{"protocol":"lightway_tcp"},"uplink":{"mode":"wireguard"}}`)
	if resp.StatusCode != 400 {
		t.Fatalf("status=%d want 400", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["field"] != "uplink.mode" {
		t.Fatalf("body=%v", body)
	}
	// Ничего не применилось.
	resp = e.req(t, "GET", "/v1/config", tok, "")
	var cfg config.Config
	json.NewDecoder(resp.Body).Decode(&cfg)
	if cfg.ExpressVPN.Protocol != "auto" {
		t.Fatalf("partial apply: protocol=%q", cfg.ExpressVPN.Protocol)
	}
}

func TestConfigPatchAppliedKeys(t *testing.T) {
	e := newTestEnv(t)
	resp := e.req(t, "PATCH", "/v1/config", e.tokens.Current(),
		`{"expressvpn":{"location":"de-frankfurt-1"}}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		Applied          []string `json:"applied"`
		RequiresRecreate []string `json:"requiresRecreate"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Applied) != 1 || body.Applied[0] != "expressvpn.location" {
		t.Fatalf("applied=%v", body.Applied)
	}
	if body.RequiresRecreate == nil || len(body.RequiresRecreate) != 0 {
		t.Fatalf("requiresRecreate=%v", body.RequiresRecreate)
	}
}

func TestTokenRotationInvalidatesOld(t *testing.T) {
	e := newTestEnv(t)
	old := e.tokens.Current()
	resp := e.req(t, "POST", "/v1/auth/token/rotate", old, "")
	if resp.StatusCode != 200 {
		t.Fatalf("rotate status=%d", resp.StatusCode)
	}
	var body map[string]string
	json.NewDecoder(resp.Body).Decode(&body)
	if body["token"] == old || body["token"] == "" {
		t.Fatal("token not rotated")
	}
	if resp := e.req(t, "GET", "/v1/state", old, ""); resp.StatusCode != 401 {
		t.Fatalf("old token still valid: %d", resp.StatusCode)
	}
	if resp := e.req(t, "GET", "/v1/state", body["token"], ""); resp.StatusCode != 200 {
		t.Fatalf("new token rejected: %d", resp.StatusCode)
	}
}

func readSSE(t *testing.T, resp *http.Response, events chan<- string) {
	t.Helper()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			events <- data
		}
	}
}

func TestEventsSnapshotAndUpdateWithinSecond(t *testing.T) {
	e := newTestEnv(t)
	sub := func() (<-chan string, func()) {
		req, _ := http.NewRequest("GET", e.srv.URL+"/v1/events", nil)
		req.Header.Set("Authorization", "Bearer "+e.tokens.Current())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
			resp.Body.Close()
			t.Fatalf("X-Accel-Buffering=%q, want no", got)
		}
		ch := make(chan string, 8)
		go readSSE(t, resp, ch)
		return ch, func() { resp.Body.Close() }
	}
	ch1, c1 := sub()
	ch2, c2 := sub()
	defer c1()
	defer c2()

	// Снимок приходит сразу при подключении.
	for i, ch := range []<-chan string{ch1, ch2} {
		select {
		case data := <-ch:
			if !strings.Contains(data, `"disconnected"`) {
				t.Fatalf("sub%d: snapshot=%s", i+1, data)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("sub%d: no snapshot", i+1)
		}
	}

	start := time.Now()
	e.state.Update(func(st *state.State) { st.ExpressVPN.Connection = state.ConnConnecting })
	for i, ch := range []<-chan string{ch1, ch2} {
		select {
		case data := <-ch:
			if !strings.Contains(data, `"connecting"`) {
				t.Fatalf("sub%d: event=%s", i+1, data)
			}
		case <-time.After(time.Second):
			t.Fatalf("sub%d: no event within 1s", i+1)
		}
	}
	if time.Since(start) > time.Second {
		t.Fatal("event took longer than 1s")
	}
}

func TestConcurrentConnectConflicts(t *testing.T) {
	e := newTestEnv(t)
	e.acts.block = make(chan struct{})
	tok := e.tokens.Current()

	resp1 := e.req(t, "POST", "/v1/actions/connect", tok, `{"location":"a"}`)
	if resp1.StatusCode != 202 {
		t.Fatalf("first connect: %d", resp1.StatusCode)
	}
	var b1 map[string]string
	json.NewDecoder(resp1.Body).Decode(&b1)
	if b1["operation"] == "" {
		t.Fatal("no operation id")
	}

	resp2 := e.req(t, "POST", "/v1/actions/connect", tok, `{"location":"b"}`)
	if resp2.StatusCode != 409 {
		t.Fatalf("second connect: %d want 409", resp2.StatusCode)
	}
	var b2 map[string]string
	json.NewDecoder(resp2.Body).Decode(&b2)
	if b2["operation"] != b1["operation"] {
		t.Fatalf("conflict op=%q want %q", b2["operation"], b1["operation"])
	}

	close(e.acts.block)
	// Операция завершается и становится succeeded.
	deadline := time.Now().Add(2 * time.Second)
	for {
		resp := e.req(t, "GET", "/v1/operations/"+b1["operation"], tok, "")
		var op Operation
		json.NewDecoder(resp.Body).Decode(&op)
		if op.Status == OpSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation stuck: %+v", op)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// После завершения новый connect снова принимается.
	resp3 := e.req(t, "POST", "/v1/actions/connect", tok, "")
	if resp3.StatusCode != 202 {
		t.Fatalf("third connect: %d", resp3.StatusCode)
	}
}

func TestUnknownAction404(t *testing.T) {
	e := newTestEnv(t)
	resp := e.req(t, "POST", "/v1/actions/frobnicate", e.tokens.Current(), "")
	if resp.StatusCode != 404 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestLogsRedactSecrets(t *testing.T) {
	e := newTestEnv(t)
	tok := e.tokens.Current()

	// Код активации регистрируется как секрет при login-действии...
	resp := e.req(t, "POST", "/v1/actions/login", tok, `{"activationCode":"SECRETCODE42"}`)
	if resp.StatusCode != 202 {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	// ...и даже если компонент попытается записать его в лог, он маскируется.
	e.logs.Append("expressvpn", "info", "login attempt with code SECRETCODE42")
	resp = e.req(t, "GET", "/v1/logs?component=expressvpn", tok, "")
	var body struct {
		Entries []logs.Entry `json:"entries"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Entries) == 0 {
		t.Fatal("no entries")
	}
	for _, e := range body.Entries {
		if strings.Contains(e.Message, "SECRETCODE42") {
			t.Fatalf("secret leaked: %s", e.Message)
		}
	}
	if !strings.Contains(body.Entries[0].Message, "***") {
		t.Fatalf("expected mask in %q", body.Entries[0].Message)
	}
}

func TestLogsFilterAndLimit(t *testing.T) {
	e := newTestEnv(t)
	tok := e.tokens.Current()
	e.logs.Append("uplink", "info", "one")
	e.logs.Append("proxy", "info", "two")
	e.logs.Append("uplink", "warn", "three")

	resp := e.req(t, "GET", "/v1/logs?component=uplink&limit=100", tok, "")
	var body struct {
		Entries []logs.Entry `json:"entries"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Entries) != 2 {
		t.Fatalf("entries=%d want 2", len(body.Entries))
	}
	if body.Entries[0].Message != "one" || body.Entries[1].Message != "three" {
		t.Fatalf("order wrong: %+v", body.Entries)
	}

	if resp := e.req(t, "GET", "/v1/logs?component=bogus", tok, ""); resp.StatusCode != 400 {
		t.Fatalf("bogus component: %d", resp.StatusCode)
	}
}

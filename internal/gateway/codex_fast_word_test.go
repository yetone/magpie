package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// Archivemylife on X: a Responses request for a Codex account's model with
// service_tier "fast", Codex's own name for its Fast mode, reached the
// ChatGPT backend with no tier at all. Codex sends Fast as "priority"
// (ServiceTier::request_value in openai/codex), so "fast" goes as
// "priority" on every OpenAI API it can arrive on, and the route says the
// tier was asked; Claude's speed "fast" already did. Another tier, and a
// request with none, goes as before.
func TestFastTierWordReachesCodex(t *testing.T) {
	codexSignedIn(t)
	d := daybreak(t)
	to, _ := url.Parse(provider.CodexBase)
	provider.CodexBase = "https://chatgpt.com/backend-api/codex"
	s := New()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r.URL.Scheme, r.URL.Host = to.Scheme, to.Host
		return http.DefaultTransport.RoundTrip(r)
	})}
	const m = `"model":"codex/gpt-daybreak-blue-latest"`
	const msgs = `"messages":[{"role":"user","content":"Respond with OK."}]`
	for _, c := range []struct{ name, path, body, want string }{
		{"Responses, fast", "/v1/responses", `{` + m + `,"input":"Respond with OK.","stream":true,"service_tier":"fast"}`, "priority"},
		{"Responses, whole, fast", "/v1/responses", `{` + m + `,"input":"Respond with OK.","service_tier":"fast"}`, "priority"},
		{"Responses, priority", "/v1/responses", `{` + m + `,"input":"Respond with OK.","stream":true,"service_tier":"priority"}`, "priority"},
		{"Chat, fast", "/v1/chat/completions", `{` + m + `,"stream":true,` + msgs + `,"service_tier":"fast"}`, "priority"},
		{"Chat, whole, fast", "/v1/chat/completions", `{` + m + `,` + msgs + `,"service_tier":"fast"}`, "priority"},
		{"Messages, speed fast", "/v1/messages", `{` + m + `,"max_tokens":64,"stream":true,` + msgs + `,"speed":"fast"}`, "priority"},
		{"Responses, flex", "/v1/responses", `{` + m + `,"input":"Respond with OK.","stream":true,"service_tier":"flex"}`, ""},
		{"Responses, none", "/v1/responses", `{` + m + `,"input":"Respond with OK.","stream":true}`, ""},
		{"Chat, none", "/v1/chat/completions", `{` + m + `,"stream":true,` + msgs + `}`, ""},
	} {
		r := postFrom(t, s, "opencode", c.path, c.body)
		b := d.last(t)
		if got := gjson.GetBytes(b, "service_tier").String(); got != c.want {
			t.Errorf("%s: the backend got service_tier %q, want %q: %s", c.name, got, c.want, b)
		}
		if c.want != "" && r.Tier != "priority" && r.Tier != "fast" {
			t.Errorf("%s: the route says tier %q was asked", c.name, r.Tier)
		}
	}
}

// fastTier changes "fast" alone, and only as a top-level string; every
// other body comes back byte for byte.
func TestFastTier(t *testing.T) {
	if got := gjson.GetBytes(fastTier([]byte(`{"model":"m","service_tier":"fast","input":"hi"}`)), "service_tier").String(); got != "priority" {
		t.Errorf("fast went as %q", got)
	}
	for _, in := range []string{
		`{"model":"m","service_tier":"priority"}`,
		`{"model":"m","service_tier":"flex"}`,
		`{"model":"m","service_tier":"Fast"}`,
		`{"model":"m","metadata":{"service_tier":"fast"}}`,
		`{"model":"m","service_tier":["fast"]}`,
		`{"model":"m"}`,
		`not json`,
	} {
		if got := string(fastTier([]byte(in))); got != in {
			t.Errorf("fastTier(%s) = %s", in, got)
		}
	}
}

// #7793: on the native /backend-api/codex ChatGPT-auth route, unsupported
// client service_tier values (such as "flex", an OpenAI API billing tier)
// must be stripped so the ChatGPT backend doesn't 400 every turn, while
// "priority", "ultrafast", and "fast" (-> "priority") still reach it, and
// API-key Codex turns relayed to codexAPIBase keep "flex".
func TestCodexBackendDropsUnsupportedServiceTier(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var gotChatGPT, gotAPI [][]byte
	chatgpt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotChatGPT = append(gotChatGPT, b)
		mu.Unlock()
		if t := gjson.GetBytes(b, "service_tier"); t.Exists() && t.String() != "priority" && t.String() != "ultrafast" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"Unsupported service_tier: ` + t.String() + `"}}`))
			return
		}
		if r.URL.Path == "/responses/compact" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":[]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-6.2-sol\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}))
	t.Cleanup(chatgpt.Close)
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotAPI = append(gotAPI, b)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"gpt-6.2-sol\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"))
	}))
	t.Cleanup(apiSrv.Close)

	origBase, origAPI := provider.CodexBase, codexAPIBase
	provider.CodexBase, codexAPIBase = chatgpt.URL, apiSrv.URL
	t.Cleanup(func() { provider.CodexBase, codexAPIBase = origBase, origAPI })

	s := New()
	send := func(path, auth, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", CodexPath+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+auth)
		if !strings.HasPrefix(auth, "sk-") {
			req.Header.Set("chatgpt-account-id", "acct-test")
		}
		req.Header.Set("User-Agent", "codex_cli_rs/0.172.0 (Windows 11; x86_64)")
		req.Header.Set("originator", "codex_cli_rs")
		s.Handler().ServeHTTP(rec, req)
		return rec
	}

	for _, c := range []struct {
		name, path, body string
		wantPresent      bool
		wantTier         string
	}{
		{"flex stripped", "/responses", `{"model":"gpt-6.2-sol","stream":true,"input":"hi","service_tier":"flex"}`, false, ""},
		{"standard stripped", "/responses", `{"model":"gpt-6.2-sol","stream":true,"input":"hi","service_tier":"standard"}`, false, ""},
		{"empty stripped", "/responses", `{"model":"gpt-6.2-sol","stream":true,"input":"hi","service_tier":""}`, false, ""},
		{"null stripped", "/responses", `{"model":"gpt-6.2-sol","stream":true,"input":"hi","service_tier":null}`, false, ""},
		{"priority kept", "/responses", `{"model":"gpt-6.2-sol","stream":true,"input":"hi","service_tier":"priority"}`, true, "priority"},
		{"ultrafast kept", "/responses", `{"model":"gpt-6.2-sol","stream":true,"input":"hi","service_tier":"ultrafast"}`, true, "ultrafast"},
		{"fast mapped to priority", "/responses", `{"model":"gpt-6.2-sol","stream":true,"input":"hi","service_tier":"fast"}`, true, "priority"},
		{"none kept absent", "/responses", `{"model":"gpt-6.2-sol","stream":true,"input":"hi"}`, false, ""},
		{"compact flex stripped", "/responses/compact", `{"model":"gpt-6.2-sol","input":"hi","service_tier":"flex"}`, false, ""},
	} {
		rec := send(c.path, "chatgpt-oauth-token", c.body)
		if rec.Code != 200 {
			t.Fatalf("%s: got %d %s", c.name, rec.Code, rec.Body.String())
		}
		mu.Lock()
		sent := gotChatGPT[len(gotChatGPT)-1]
		mu.Unlock()
		got := gjson.GetBytes(sent, "service_tier")
		if got.Exists() != c.wantPresent || (c.wantPresent && got.String() != c.wantTier) {
			t.Errorf("%s: upstream got service_tier exists=%v val=%q, want exists=%v val=%q; body=%s",
				c.name, got.Exists(), got.String(), c.wantPresent, c.wantTier, sent)
		}
	}

	// API-key Codex turns relay to codexAPIBase (api.openai.com/v1), where "flex" is valid.
	rec := send("/responses", "sk-test-openai-key", `{"model":"gpt-6.2-sol","stream":true,"input":"hi","service_tier":"flex"}`)
	if rec.Code != 200 {
		t.Fatalf("api-key flex: got %d %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	apiSent := gotAPI[len(gotAPI)-1]
	mu.Unlock()
	if got := gjson.GetBytes(apiSent, "service_tier").String(); got != "flex" {
		t.Errorf("api-key flex: upstream got service_tier=%q, want \"flex\"; body=%s", got, apiSent)
	}
}

func TestCodexTierTakenOut(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"model":"m","service_tier":"flex","input":"hi"}`, `{"model":"m","input":"hi"}`},
		{`{"model":"m","service_tier":"standard"}`, `{"model":"m"}`},
		{`{"model":"m","service_tier":""}`, `{"model":"m"}`},
		{`{"model":"m","service_tier":null}`, `{"model":"m"}`},
		{`{"model":"m","service_tier":123}`, `{"model":"m"}`},
		{`{"model":"m","service_tier":"priority"}`, `{"model":"m","service_tier":"priority"}`},
		{`{"model":"m","service_tier":"ultrafast"}`, `{"model":"m","service_tier":"ultrafast"}`},
		{`{"model":"m","metadata":{"service_tier":"flex"}}`, `{"model":"m","metadata":{"service_tier":"flex"}}`},
		{`{"model":"m"}`, `{"model":"m"}`},
		{`not json`, `not json`},
	} {
		if got := string(codexTierTakenOut([]byte(c.in))); got != c.want {
			t.Errorf("codexTierTakenOut(%s) = %s, want %s", c.in, got, c.want)
		}
	}
}

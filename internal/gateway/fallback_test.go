package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// twoProviders: "plan" (the one agents pick) falls back to "spare".
func twoProviders(t *testing.T, plan, spare *fake) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	for _, x := range []struct {
		p provider.Provider
		f *fake
	}{
		{provider.Provider{ID: "plan", Name: "Plan", Key: "k", Models: []string{"m1"}, Fallback: []string{"spare/m2", "nowhere/x"}}, plan},
		{provider.Provider{ID: "spare", Name: "Spare", Key: "k", Models: []string{"m2"}}, spare},
	} {
		up := httptest.NewServer(x.f)
		t.Cleanup(up.Close)
		x.p.Chat = up.URL + "/v1"
		if err := provider.Save(x.p); err != nil {
			t.Fatal(err)
		}
	}
}

const chatReq = `{"model":"plan/m1","messages":[{"role":"user","content":"hi"}]}`

func TestFallbackWhenOutOfQuota(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		msg  string
	}{
		{"rate limit", 429, "slow down"},
		{"no balance", 402, "Insufficient Balance"},
		{"overloaded", 529, "overloaded"},
		{"quota as 403", 403, "您的套餐额度已用完"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &fake{t: t, ctype: "application/json", code: tc.code, reply: `{"error":{"message":"` + tc.msg + `"}}`}
			spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
			twoProviders(t, plan, spare)
			code, body := post(t, "/v1/chat/completions", chatReq)
			if code != 200 || !strings.Contains(body, "from-spare") || strings.Contains(body, tc.msg) {
				t.Fatalf("%d %s", code, body)
			}
			if !bytes.Contains(spare.got, []byte(`"model":"m2"`)) {
				t.Fatalf("spare got %s", spare.got)
			}
		})
	}
}

func TestFallbackRestsTheFailedProvider(t *testing.T) {
	plan := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"rate limited"}}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	s := New()
	send := func() (int, string) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
		return rec.Code, rec.Body.String()
	}
	send()
	if c := s.Recent()[0]; c.Provider != "spare" || !strings.Contains(c.Fallback, "plan: ") {
		t.Fatalf("recorded %+v", c)
	}
	plan.got = nil
	if code, body := send(); code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if plan.got != nil {
		t.Fatal("a resting provider was tried first")
	}
}

func TestNoFallbackForOtherErrors(t *testing.T) {
	plan := &fake{t: t, ctype: "application/json", code: 400, reply: `{"error":{"message":"messages: field required"}}`}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 400 || !strings.Contains(body, "field required") || spare.got != nil {
		t.Fatalf("%d %s (spare got %s)", code, body, spare.got)
	}
}

func TestLastFallbackErrorReachesTheAgent(t *testing.T) {
	plan := &fake{t: t, ctype: "application/json", code: 429, reply: `{"error":{"message":"plan limit"}}`}
	spare := &fake{t: t, ctype: "application/json", code: 503, reply: `{"error":{"message":"spare down"}}`}
	twoProviders(t, plan, spare)
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 503 || !strings.Contains(body, "spare down") {
		t.Fatalf("%d %s", code, body)
	}
}

// byKey answers per API key: a key named in limited is out of quota.
type byKey struct {
	limited map[string]bool
	seen    []string
}

func (b *byKey) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	b.seen = append(b.seen, key)
	w.Header().Set("Content-Type", "application/json")
	if b.limited[key] {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"message":"rate limited"}}`)
		return
	}
	io.WriteString(w, `{"id":"from-`+key+`","choices":[]}`)
}

func TestSeveralKeysOnTakeOverFromEachOther(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	up := &byKey{limited: map[string]bool{"k-personal": true}}
	srv := httptest.NewServer(up)
	defer srv.Close()
	p := provider.Provider{ID: "plan", Name: "Plan", Chat: srv.URL + "/v1", Models: []string{"m1"},
		Key: "k-personal", KeyName: "Personal",
		Keys: []provider.KeyAccount{{Name: "Idle", Key: "k-idle", Off: true}, {Name: "Team", Key: "k-team"}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	s := New()
	send := func() string {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
		return rec.Body.String()
	}
	if body := send(); !strings.Contains(body, "from-k-team") {
		t.Fatalf("body %s", body)
	}
	if c := s.Recent()[0]; c.Provider != "plan" || !strings.Contains(c.Fallback, "plan (Personal): ") {
		t.Fatalf("recorded %+v", c)
	}
	// the limited key rests; the one that's off is never tried
	up.seen = nil
	if body := send(); !strings.Contains(body, "from-k-team") || strings.Join(up.seen, ",") != "k-team" {
		t.Fatalf("body %s, tried %v", body, up.seen)
	}
}

// Two ChatGPT accounts ticked: the one Codex is signed in to is out of
// quota, so the request goes to the saved one, signed with its own tokens.
func TestSubscriptionAccountsTakeOver(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": email}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": acct}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(auth("me@example.com", "acct-1")), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "codex", "user": "spare@example.com", "on": true, "seen": time.Now(), "auth": auth("spare@example.com", "acct-2")},
	}), 0o600)

	var tried []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		tried = append(tried, r.Header.Get("chatgpt-account-id"))
		if r.Header.Get("chatgpt-account-id") == "acct-1" {
			w.WriteHeader(429)
			io.WriteString(w, `{"error":{"message":"You've hit your usage limit"}}`)
			return
		}
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	defer up.Close()
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = old }()

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping"}`)
	if code != 200 || !strings.Contains(body, "pong") {
		t.Fatalf("status %d: %s", code, body)
	}
	if strings.Join(tried, ",") != "acct-1,acct-2" {
		t.Fatalf("tried %v", tried)
	}
	// the first sits out a minute; the next request goes straight on
	tried = nil
	post(t, "/v1/responses", `{"model":"codex/gpt-5.5","input":"ping"}`)
	if strings.Join(tried, ",") != "acct-2" {
		t.Fatalf("second request tried %v", tried)
	}
}

// Two Claude Max accounts ticked (#177): the one Claude Code is signed in to
// is out of quota, and the request goes to the saved spare, streamed or not.
// With magpie wired into Claude Code, `claude auth status` tells of magpie's
// token and no email: the account is still named from ~/.claude.json, so it
// is the saved me@example.com, not a "Claude Max" served beside it.
func TestClaudeAccountsTakeOver(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	far := time.Now().Add(24 * time.Hour).UnixMilli()
	oauth := func(tok string) map[string]any {
		return map[string]any{"claudeAiOauth": map[string]any{"accessToken": tok, "refreshToken": "r-" + tok, "expiresAt": far, "subscriptionType": "max"}}
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), mustJSON(oauth("tok-primary")), 0o600)
	os.WriteFile(filepath.Join(home, ".claude.json"), mustJSON(map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "me@example.com", "accountUuid": "u-me"}}), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "claude", "user": "me@example.com", "plan": "max", "on": true, "seen": time.Now(), "auth": oauth("tok-me")},
		{"agent": "claude", "user": "spare@example.com", "plan": "max", "on": true, "seen": time.Now(), "auth": oauth("tok-spare")},
	}), 0o600)
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	// out of quota, the CLI says so in a result and waits on its next input
	script := `#!/bin/sh
if [ "$1" = auth ]; then echo '{"loggedIn":true,"authMethod":"oauth_token","apiProvider":"firstParty"}'; exit 0; fi
TOK=; [ -n "$CLAUDE_CONFIG_DIR" ] && TOK=$(sed -n 's/.*"accessToken": *"\([^"]*\)".*/\1/p' "$CLAUDE_CONFIG_DIR/.credentials.json")
echo "${TOK:-own}" >> ` + log + `
while read -r line; do
  if [ -z "$TOK" ] || [ "$TOK" = tok-me ]; then
    echo '{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","rateLimitType":"five_hour","resetsAt":1790700000}}'
    echo '{"type":"assistant","message":{"id":"x","model":"<synthetic>","role":"assistant","content":[{"type":"text","text":"You'"'"'ve hit your limit · resets 3am"}]},"error":"rate_limit"}'
    echo '{"type":"result","subtype":"success","is_error":true,"result":"You'"'"'ve hit your limit · resets 3am"}'
  else
    echo '{"type":"stream_event","event":{"type":"message_start","message":{"id":"m","model":"claude-sonnet-5","usage":{"input_tokens":1}}}}'
    echo '{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}}'
    echo '{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}'
    echo '{"type":"stream_event","event":{"type":"message_stop"}}'
    echo '{"type":"result","subtype":"success","is_error":false,"result":"pong"}'
  fi
done
`
	os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)

	for _, stream := range []string{"true", "false"} {
		restingUntil.Lock()
		restingUntil.m = map[string]time.Time{}
		restingUntil.Unlock()
		os.Remove(log)
		done := make(chan struct{})
		var code int
		var body string
		go func() {
			defer close(done)
			code, body = post(t, "/v1/messages", `{"model":"claude/claude-sonnet-5","max_tokens":50,"stream":`+stream+`,"messages":[{"role":"user","content":"ping"}]}`)
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatalf("stream=%s: no answer", stream)
		}
		b, _ := os.ReadFile(log)
		if code != 200 || !strings.Contains(body, "pong") {
			t.Fatalf("stream=%s: status %d: %s (runs %q)", stream, code, body, b)
		}
		// the signed-in account once, then the spare: never it again under
		// its saved name
		if runs := strings.Fields(string(b)); strings.Join(runs, ",") != "own,tok-spare" && strings.Join(runs, ",") != "tok-spare" {
			t.Fatalf("stream=%s: runs %v", stream, runs)
		}
	}
	for _, p := range provider.All() {
		if p.Account != nil && p.Account.Agent == "claude" && p.Account.User != "me@example.com" {
			t.Fatalf("Claude Code's own account named %q", p.Account.User)
		}
	}
}

// A relay that hands out one key for Anthropic and another for OpenAI:
// each key is used on its own endpoint only, and the one that suits the
// model goes first whatever the order.
func TestKeysMadeForOneProtocol(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		key := r.Header.Get("x-api-key")
		if key == "" {
			key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		}
		seen = append(seen, r.URL.Path+" "+key)
		if (r.URL.Path == "/v1/messages") != (key == "k-ant") {
			w.WriteHeader(401)
			io.WriteString(w, `{"error":{"message":"this key is for another protocol"}}`)
			return
		}
		if bytes.Contains(b, []byte(`"stream":true`)) && r.URL.Path == "/v1/messages" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
				`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
				`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
				`event: message_stop`+"\n"+`data: {"type":"message_stop"}`))
			return
		}
		if bytes.Contains(b, []byte(`"stream":true`)) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(
				`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"}}]}`,
				`data: {"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`,
				`data: [DONE]`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/messages" {
			io.WriteString(w, `{"id":"m1","type":"message","role":"assistant","model":"x","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer srv.Close()
	p := provider.Provider{ID: "relay", Name: "Relay", Chat: srv.URL + "/v1", Anthropic: srv.URL,
		Key: "k-oai", KeyProtocol: provider.Chat, Models: []string{"claude-opus-4-8", "gpt-5.5", "glm-5"},
		Keys: []provider.KeyAccount{{Key: "k-ant", Protocol: provider.Anthropic}}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	// the vendor lists glm-5 to the Anthropic key only
	if err := catalog.SaveLive("relay", srv.URL, []catalog.Model{{ID: "gpt-5.5", Keys: []string{provider.KeyID("k-oai")}},
		{ID: "glm-5", Keys: []string{provider.KeyID("k-ant")}}}); err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct{ path, body, want string }{
		// Claude Code asking for Claude: the Anthropic key, relayed as-is
		{"/v1/messages", `{"model":"relay/claude-opus-4-8","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`, "/v1/messages k-ant"},
		// an OpenAI agent asking for Claude: translated, to the Anthropic key
		{"/v1/chat/completions", `{"model":"relay/claude-opus-4-8","messages":[{"role":"user","content":"hi"}]}`, "/v1/messages k-ant"},
		// Claude Code asking for GPT: translated, to the OpenAI key
		{"/v1/messages", `{"model":"relay/gpt-5.5","max_tokens":9,"messages":[{"role":"user","content":"hi"}]}`, "/v1/chat/completions k-oai"},
		// a model only the Anthropic key lists goes there, whatever the agent spoke
		{"/v1/chat/completions", `{"model":"relay/glm-5","messages":[{"role":"user","content":"hi"}]}`, "/v1/messages k-ant"},
	} {
		seen = nil
		code, body := post(t, x.path, x.body)
		if code != 200 || len(seen) != 1 || seen[0] != x.want {
			t.Fatalf("%s %s: %d %s, upstream saw %v", x.path, x.body, code, body, seen)
		}
	}
	if got := modelFamily("openrouter/openai/o3-mini"); got != provider.Chat {
		t.Errorf("o3: %q", got)
	}
	if got := modelFamily("deepseek-chat"); got != "" {
		t.Errorf("deepseek: %q", got)
	}
}

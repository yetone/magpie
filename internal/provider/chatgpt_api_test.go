package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOpenAI stands in for auth.openai.com and api.openai.com, as Sign in
// with ChatGPT uses them.
type fakeOpenAI struct {
	t        *testing.T
	mu       sync.Mutex
	auth     *httptest.Server
	api      *httptest.Server
	forms    []url.Values // what the token endpoint was sent
	revoked  []url.Values
	nonce    string // the ID token's
	scope    string
	noID     bool
	refuse   string // a refresh refused with this OAuth code
	modelsOf string // the token /models was asked with
	n        int
}

func newFakeOpenAI(t *testing.T) *fakeOpenAI {
	f := &fakeOpenAI{t: t, scope: "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"}
	f.auth = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": f.auth.URL, "revocation_endpoint": f.auth.URL + "/oauth/revoke"})
		case "/oauth/revoke":
			r.ParseForm()
			f.mu.Lock()
			f.revoked = append(f.revoked, r.PostForm)
			f.mu.Unlock()
		case "/api/accounts/oauth/token":
			r.ParseForm()
			f.mu.Lock()
			defer f.mu.Unlock()
			f.forms = append(f.forms, r.PostForm)
			f.n++
			if r.PostForm.Get("grant_type") == "refresh_token" && f.refuse != "" {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"error": f.refuse, "error_description": "no"})
				return
			}
			out := map[string]any{
				"access_token":  "at-" + string(rune('0'+f.n)),
				"refresh_token": "rt-" + string(rune('0'+f.n)),
				"token_type":    "Bearer", "expires_in": 3600, "scope": f.scope,
			}
			if !f.noID {
				out["id_token"] = fakeJWT(map[string]any{"iss": f.auth.URL, "aud": r.PostForm.Get("client_id"),
					"sub": "user-1", "email": "me@example.com", "nonce": f.nonce, "exp": float64(time.Now().Add(time.Hour).Unix()),
					"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus"}})
			}
			json.NewEncoder(w).Encode(out)
		default:
			http.NotFound(w, r)
		}
	}))
	f.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			f.mu.Lock()
			f.modelsOf = r.Header.Get("Authorization")
			f.mu.Unlock()
			io.WriteString(w, `{"models":[
				{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list","context_window":272000,"input_modalities":["text","image"],"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]},
				{"slug":"gpt-hidden","display_name":"Hidden","visibility":"hide"},
				{"slug":"gpt-5.4-mini","display_name":"GPT-5.4 mini","visibility":"list"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.auth.Close)
	t.Cleanup(f.api.Close)
	t.Cleanup(SIWCForTest(f.auth.URL, f.api.URL+"/v1"))
	old := siwcCallbackAddr
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	siwcCallbackAddr = ln.Addr().String()
	ln.Close()
	t.Cleanup(func() { siwcCallbackAddr = old })
	return f
}

func (f *fakeOpenAI) lastForm() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forms[len(f.forms)-1]
}

// signInVia runs a sign-in to its callback, which OpenAI sends with the
// client id it issued (clientID, "" for none), and waits for its end.
func signInVia(t *testing.T, f *fakeOpenAI, clientID string, nonce func(string) string) SignInState {
	t.Helper()
	st, err := StartSignIn(ChatGPTAPIID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { CancelSignIn(st.ID) })
	u, err := url.Parse(st.URL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	f.mu.Lock()
	f.nonce = nonce(q.Get("nonce"))
	f.mu.Unlock()
	back := url.Values{"code": {"the-code"}, "state": {q.Get("state")}, "scope": {q.Get("scope")}}
	if clientID != "" {
		back.Set("client_id", clientID)
	}
	res, err := http.Get(q.Get("redirect_uri") + "?" + back.Encode())
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := WaitSignIn(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func same(s string) string { return s }

// #933: OpenAI's Sign in with ChatGPT, as an open-source app on the user's
// machine uses it: registered on the way as dynamic_agent_client, the
// code traded under the client id OpenAI issued, the account kept with it,
// its models OpenAI's /v1/models for the token, and requests sent to
// api.openai.com's Responses.
func TestChatGPTAPISignIn(t *testing.T) {
	claudeHome(t)
	f := newFakeOpenAI(t)

	st, err := StartSignIn(ChatGPTAPIID)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(st.URL)
	q := u.Query()
	CancelSignIn(st.ID)
	if !strings.HasPrefix(st.URL, f.auth.URL+"/api/accounts/authorize?") {
		t.Fatalf("opens %s", st.URL)
	}
	for k, want := range map[string]string{"client_id": "dynamic_agent_client", "agent_name_hint": "magpie",
		"response_type": "code", "resource": "https://api.openai.com/v1", "code_challenge_method": "S256",
		"scope": "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	for _, k := range []string{"state", "nonce", "code_challenge"} {
		if q.Get(k) == "" {
			t.Errorf("no %s", k)
		}
	}
	host := q.Get("ext_agent_host_id")
	if !strings.HasPrefix(host, "urn:uuid:") || len(host) != len("urn:uuid:")+36 {
		t.Errorf("ext_agent_host_id %q", host)
	}
	if r, _ := url.Parse(q.Get("redirect_uri")); r == nil || r.Hostname() != "127.0.0.1" || r.Path != "/auth/callback" {
		t.Errorf("redirect_uri %q", q.Get("redirect_uri"))
	}

	done := signInVia(t, f, "oaiapp_123", same)
	if done.State != "done" || done.User != "me@example.com" || done.Plan != "plus" {
		t.Fatalf("sign-in ended %+v", done)
	}
	form := f.lastForm()
	for k, want := range map[string]string{"grant_type": "authorization_code", "client_id": "oaiapp_123",
		"code": "the-code", "resource": "https://api.openai.com/v1"} {
		if form.Get(k) != want {
			t.Errorf("code trade %s = %q, want %q", k, form.Get(k), want)
		}
	}
	if form.Get("code_verifier") == "" || form.Get("client_secret") != "" {
		t.Errorf("code trade %v", form)
	}
	// the same machine id next time
	st2, _ := StartSignIn(ChatGPTAPIID)
	u2, _ := url.Parse(st2.URL)
	CancelSignIn(st2.ID)
	if got := u2.Query().Get("ext_agent_host_id"); got != host {
		t.Errorf("host id %q, then %q", host, got)
	}

	c, ok := siwcLookup("me@example.com")
	if !ok || c.ClientID != "oaiapp_123" || c.HostID != host || c.Access != "at-1" || c.Refresh != "rt-1" || c.Subject != "user-1" {
		t.Fatalf("kept %+v", c)
	}
	var p Provider
	for _, a := range Accounts() {
		if a.ID == ChatGPTAPIID {
			p = a
		}
	}
	if p.Account == nil || p.Responses != f.api.URL+"/v1" || p.Account.User != "me@example.com" {
		t.Fatalf("provider %+v", p)
	}
	if got := Logins(ChatGPTAPIID); len(got) != 1 || !got[0].Active {
		t.Fatalf("logins %+v", got)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 || ms[0].ID != "gpt-5.5" || ms[1].ID != "gpt-5.4-mini" || !ms[0].Images || len(ms[0].Efforts) != 2 {
		t.Errorf("models %+v", ms)
	}
	if f.modelsOf != "Bearer at-1" {
		t.Errorf("models asked with %q", f.modelsOf)
	}
	req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
	if err := p.Account.sign(context.Background(), req, nil); err != nil || req.Header.Get("Authorization") != "Bearer at-1" {
		t.Errorf("signed %q, %v", req.Header.Get("Authorization"), err)
	}

	// signed out: OpenAI revokes the refresh token
	if err := ForgetLogin(ChatGPTAPIID, "me@example.com"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.revoked)
		f.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.revoked) != 1 || f.revoked[0].Get("token") != "rt-1" || f.revoked[0].Get("client_id") != "oaiapp_123" ||
		f.revoked[0].Get("token_type_hint") != "refresh_token" {
		t.Errorf("revoked %v", f.revoked)
	}
	if _, ok := siwcLookup("me@example.com"); ok {
		t.Error("still kept")
	}
}

// A sign-in OpenAI didn't finish registering, one whose grant leaves out
// the direct use of the token, or whose ID token isn't this sign-in's,
// keeps nothing.
func TestChatGPTAPISignInRefused(t *testing.T) {
	for _, tt := range []struct {
		name, client, want string
		nonce              func(string) string
		scope              string
	}{
		{"no client id", "", "no client id", same, ""},
		{"no direct scope", "oaiapp_1", "chatgpt.tokens.use.direct", same, "openid profile email offline_access"},
		{"other nonce", "oaiapp_1", "isn't this sign-in's", func(string) string { return "someone-else" }, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			claudeHome(t)
			f := newFakeOpenAI(t)
			if tt.scope != "" {
				f.scope = tt.scope
			}
			st := signInVia(t, f, tt.client, tt.nonce)
			if st.State != "failed" || !strings.Contains(st.Error, tt.want) {
				t.Fatalf("ended %+v", st)
			}
			if ls := Logins(ChatGPTAPIID); len(ls) != 0 {
				t.Errorf("kept %+v", ls)
			}
		})
	}
}

// A token about to expire is refreshed under the client id OpenAI issued,
// for the API's resource, and the rotated refresh token kept; a refresh
// token OpenAI won't take again has the account signed in again.
func TestChatGPTAPIRefresh(t *testing.T) {
	claudeHome(t)
	f := newFakeOpenAI(t)
	if st := signInVia(t, f, "oaiapp_9", same); st.State != "done" {
		t.Fatalf("sign-in %+v", st)
	}
	user := "me@example.com"
	expire := func() {
		if err := editSideLogin(ChatGPTAPIID, user, func(ls []savedLogin, i int) ([]savedLogin, error) {
			c, _ := siwcSaved(ls[i])
			c.Expires = time.Now().Add(time.Minute)
			ls[i].Auth, _ = json.Marshal(c)
			return ls, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	expire()
	c, err := siwcFresh(context.Background(), user, false)
	if err != nil {
		t.Fatal(err)
	}
	form := f.lastForm()
	if form.Get("grant_type") != "refresh_token" || form.Get("client_id") != "oaiapp_9" || form.Get("refresh_token") != "rt-1" ||
		form.Get("resource") != "https://api.openai.com/v1" || form.Has("scope") {
		t.Errorf("refresh sent %v", form)
	}
	if c.Access != "at-2" || c.Refresh != "rt-2" || time.Until(c.Expires) < 50*time.Minute {
		t.Errorf("refreshed %+v", c)
	}
	if kept, _ := siwcLookup(user); kept.Refresh != "rt-2" || kept.IDToken == "" {
		t.Errorf("kept %+v", kept)
	}
	// fresh: not refreshed again
	n := len(f.forms)
	if _, err := siwcFresh(context.Background(), user, false); err != nil || len(f.forms) != n {
		t.Errorf("refreshed a fresh token (%v)", err)
	}

	expire()
	f.mu.Lock()
	f.refuse = "refresh_token_reused"
	f.mu.Unlock()
	if _, err := siwcFresh(context.Background(), user, false); !errors.Is(err, ErrSIWCSignIn) {
		t.Fatalf("a dead refresh token: %v", err)
	}
	loginsMu.Lock()
	ls := readLogins()
	loginsMu.Unlock()
	if len(ls) != 1 || !strings.Contains(ls[0].Lapsed, "sign in again") {
		t.Errorf("not marked lapsed: %+v", ls)
	}
}

// The request goes as Sign in with ChatGPT takes it: stored nowhere and
// streamed, without the fields it refuses or the tools OpenAI would run
// itself, a system message as a developer one; the agent's instructions
// and its own tools are left as they are.
func TestChatGPTAPIBody(t *testing.T) {
	in := `{"model":"gpt-5.5","instructions":"You are my agent.","store":true,"stream":false,"max_output_tokens":100,
		"temperature":0.2,"top_p":1,"metadata":{"a":"b"},"user":"u","truncation":"auto","previous_response_id":"resp_1",
		"prompt_cache_retention":"24h","safety_identifier":"s","prompt_cache_key":"k","reasoning":{"effort":"high"},
		"input":[{"role":"system","content":"be brief"},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},
			{"type":"additional_tools","role":"developer","tools":[{"type":"tool_search","execution":"client"},{"type":"namespace","name":"functions","tools":[]}]}],
		"tools":[{"type":"function","name":"shell","parameters":{"type":"object"}},{"type":"image_generation"},{"type":"web_search"}],
		"tool_choice":"auto"}`
	var m map[string]any
	if err := json.Unmarshal(siwcBody([]byte(in)), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"max_output_tokens", "temperature", "top_p", "metadata", "user", "truncation",
		"previous_response_id", "prompt_cache_retention", "safety_identifier"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s left in", k)
		}
	}
	if m["store"] != false || m["stream"] != true || m["instructions"] != "You are my agent." || m["prompt_cache_key"] != "k" || m["reasoning"] == nil {
		t.Errorf("body %v", m)
	}
	input := m["input"].([]any)
	if input[0].(map[string]any)["role"] != "developer" {
		t.Errorf("system message %v", input[0])
	}
	if at := input[2].(map[string]any)["tools"].([]any); len(at) != 1 || at[0].(map[string]any)["type"] != "namespace" {
		t.Errorf("additional tools %v", at)
	}
	tools := m["tools"].([]any)
	if len(tools) != 2 || tools[0].(map[string]any)["name"] != "shell" || tools[1].(map[string]any)["type"] != "web_search" {
		t.Errorf("tools %v", tools)
	}
	if m["tool_choice"] != "auto" {
		t.Errorf("tool_choice %v", m["tool_choice"])
	}

	// a string input is a user message
	m = nil
	_ = json.Unmarshal(siwcBody([]byte(`{"model":"gpt-5.5","input":"hello","tools":[{"type":"file_search"}],"tool_choice":"auto"}`)), &m)
	if in, ok := m["input"].([]any); !ok || len(in) != 1 {
		t.Errorf("input %v", m["input"])
	}
	if _, ok := m["tools"]; ok {
		t.Errorf("tools %v", m["tools"])
	}
}

// A ChatGPT sign-in's request names its conversation as a Codex account's
// does: the client's prompt_cache_key, or one taken from how the
// conversation starts, in the body and in session-id and thread-id (#933).
func TestChatGPTAPISession(t *testing.T) {
	claudeHome(t)
	f := newFakeOpenAI(t)
	if st := signInVia(t, f, "oaiapp_9", same); st.State != "done" {
		t.Fatalf("sign-in %+v", st)
	}
	p, ok := siwcAccount()
	if !ok {
		t.Fatal("no ChatGPT API account")
	}
	turn := func(body string) (string, http.Header) {
		t.Helper()
		out := p.Prepare([]byte(body))
		var m struct {
			Key string `json:"prompt_cache_key"`
		}
		json.Unmarshal(out, &m)
		req, _ := http.NewRequest("POST", p.Responses+"/responses", nil)
		if err := p.Sign(context.Background(), req, Responses, out); err != nil {
			t.Fatal(err)
		}
		return m.Key, req.Header
	}
	// Codex names its thread
	key, h := turn(`{"model":"gpt-6.1-sol","prompt_cache_key":"019a-thread","input":"hi"}`)
	if key != "019a-thread" || h.Get("session-id") != "019a-thread" || h.Get("thread-id") != "019a-thread" {
		t.Fatalf("key %q headers %v", key, h)
	}
	// a client that doesn't: the same name on every turn
	user := `{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the bug"}]}`
	k1, h1 := turn(`{"model":"gpt-6.1-sol","instructions":"You are omp.","input":[` + user + `]}`)
	k2, h2 := turn(`{"model":"gpt-6.1-sol","instructions":"You are omp.","input":[` + user +
		`,{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"and the test?"}]}]}`)
	if k1 == "" || k1 != k2 || h1.Get("session-id") != k1 || h2.Get("thread-id") != k1 {
		t.Fatalf("keys %q %q, headers %v %v", k1, k2, h1, h2)
	}
}

// OpenAI's refusals of a ChatGPT token say what to do: the plan's usage
// shared with ChatGPT, an account that isn't eligible, a capability the
// token can't use.
func TestChatGPTAPIExplain(t *testing.T) {
	for _, tt := range []struct {
		status int
		body   string
		want   string
	}{
		{429, `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"limit"}}`, "chatgpt.com/settings/usage"},
		{403, `{"detail":{"code":"subscription_sharing_user_not_eligible"}}`, "isn't eligible"},
		{503, `{"detail":"subscription_sharing_usage_unavailable: try later"}`, "try again"},
		{400, `{"error":{"code":"subscription_sharing_unsupported_capability","param":"tools[0].type"}}`, "tools[0].type"},
		{401, `{"detail":"Unauthorized"}`, "sign in again"},
		{500, `{"error":{"message":"boom"}}`, ""},
	} {
		got := siwcExplain(tt.status, []byte(tt.body))
		if tt.want == "" && got != "" || !strings.Contains(got, tt.want) {
			t.Errorf("%d %s: %q", tt.status, tt.body, got)
		}
	}
}

// A ChatGPT API account's usage line says why there is none, and where it
// is told, rather than a read that failed (danzi002 on Discord: accounts
// added through the API show no allowance, ones signed in as ChatGPT do).
// OpenAI's API is asked nothing for it.
func TestChatGPTAPIUsageSaysWhere(t *testing.T) {
	claudeHome(t)
	f := newFakeOpenAI(t)
	if st := signInVia(t, f, "oaiapp_9", same); st.State != "done" {
		t.Fatalf("sign-in %+v", st)
	}
	n := len(f.forms)
	q, ok := LoginUsage(context.Background(), ChatGPTAPIID)["me@example.com"]
	if !ok || q.Error != siwcNoUsage || q.SeeAt != SIWCUsageURL || len(q.Windows) != 0 {
		t.Fatalf("read %+v (%v)", q, ok)
	}
	if !strings.Contains(q.Error, "chatgpt.com/settings/usage") || !strings.Contains(q.Error, "added as ChatGPT") || strings.Contains(q.Error, "side by side") {
		t.Fatalf("said %q", q.Error)
	}
	if len(f.forms) != n {
		t.Errorf("OpenAI was asked %d times for it", len(f.forms)-n)
	}
}

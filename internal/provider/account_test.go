package provider

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/testenv"
)

// fakeJWT is a token whose payload is the given claims; nobody checks the
// signature, so "sig" will do.
func fakeJWT(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "h." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func writeFile(t *testing.T, path string, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// signIn writes a Codex and a Copilot login into a temp home.
func signIn(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows: os.UserHomeDir reads this, not HOME
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	exp := float64(time.Now().Add(time.Hour).Unix())
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": "me@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-1"}}),
			"access_token":  fakeJWT(map[string]any{"exp": exp}),
			"refresh_token": "r", "account_id": "acct-1",
		},
	})
	// Codex lists its models in its own cache; magpie reads that, nothing else.
	writeFile(t, filepath.Join(home, ".codex", "models_cache.json"), map[string]any{
		"models": []any{map[string]any{"slug": "gpt-5.5", "display_name": "GPT-5.5", "visibility": "list", "priority": 1}},
	})
	writeFile(t, filepath.Join(home, ".config", "github-copilot", "apps.json"), map[string]any{
		"github.com:Iv1.x": map[string]any{"user": "octocat", "oauth_token": "gho_x"},
	})
	return home
}

// isolate keeps tests off the machine's own Claude Code and Cursor Keychain
// logins and forgets anything a previous test cached.
func isolate(t *testing.T) {
	t.Helper()
	oldKeychain, oldBase, oldExe := claudeKeychain, claudeBase, claudeExecutable
	oldCursor, oldDevin := cursorKeychain, DevinExecutable
	claudeKeychain, cursorKeychain = false, false
	claudeExecutable = func() string { return "" }
	// the machine's own devin, if it has one, is no test's
	DevinExecutable = func() string { return "" }
	forgetClaudeCredential()
	forgetClaudeStatus()
	forgetDevinStatus()
	// nor an account's allowance another test read, which LoginUsage and
	// the Usage page share
	loginUsageCache.Lock()
	loginUsageCache.m, loginUsageCache.pending = nil, nil
	loginUsageCache.Unlock()
	// nor what Copilot refused an account, or its sessions and picks: they
	// are kept by GitHub token, and every test signed in (signIn) has the
	// same one
	ForgetCopilotForTest()
	t.Cleanup(func() {
		claudeKeychain, claudeBase, claudeExecutable = oldKeychain, oldBase, oldExe
		cursorKeychain, DevinExecutable = oldCursor, oldDevin
		forgetClaudeCredential()
		forgetClaudeStatus()
		forgetDevinStatus()
	})
}

func TestAccountsAreProviders(t *testing.T) {
	signIn(t)
	all := All()
	codex, ok := find(all, "codex")
	if !ok || codex.Account == nil || codex.Account.User != "me@example.com" || codex.Account.Plan != "pro" || !codex.Ready() {
		t.Fatalf("codex: %+v", codex)
	}
	copilot, ok := find(all, "copilot")
	if !ok || copilot.Account == nil || copilot.Account.User != "octocat" || copilot.Chat == "" {
		t.Fatalf("copilot: %+v", copilot)
	}
	var ids []string
	for _, e := range Catalog() {
		ids = append(ids, e.ID)
	}
	if !contains(ids, "codex/gpt-5.5") {
		t.Fatalf("catalog: %v", ids)
	}
	p, model, ok := Resolve("codex/gpt-5.5")
	if !ok || p.ID != "codex" || model != "gpt-5.5" || p.Account == nil {
		t.Fatalf("resolve: %+v %q %v", p, model, ok)
	}

	// picks are the only thing saved; the file never holds the login
	if err := Save(Provider{ID: "codex", Name: "x", Chat: "https://ignored", Key: "ignored", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "ignored") || !strings.Contains(string(b), `"gpt-5.5"`) {
		t.Fatalf("stored: %s", b)
	}
	codex, _ = find(All(), "codex")
	if len(codex.Exposed()) != 1 || codex.Exposed()[0].ID != "gpt-5.5" || codex.Responses == "" {
		t.Fatalf("picks: %+v", codex.Exposed())
	}
	// removing an account hides it from magpie; adding it back restores
	// it with its picks
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "codex"); ok {
		t.Fatal("codex still listed after remove")
	}
	if _, _, ok := Resolve("codex/gpt-5.5"); ok {
		t.Fatal("removed codex still resolves")
	}
	if x := Excluded(); len(x) != 1 || x[0].Provider != "codex" || x[0].Agent != "codex" {
		t.Fatalf("excluded: %+v", x)
	}
	// saving its picks again (anything that saves it) keeps it removed
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := find(All(), "codex"); ok || len(Excluded()) != 1 {
		t.Fatal("a save brought a removed codex back")
	}
	// not being reminded of it (#116) keeps it removed, through a save
	if err := QuietAccount("codex"); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if x := Excluded(); len(x) != 1 || !x[0].Quiet {
		t.Fatalf("quiet: %+v", x)
	}
	if _, ok := find(All(), "codex"); ok {
		t.Fatal("quieting brought codex back")
	}
	// hidden from the Add sheet too (#116): kept through a save, listed
	// there again by untuck, still removed and quiet
	if err := TuckAccount("codex", true); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "codex", Models: []string{"gpt-5.5"}}); err != nil {
		t.Fatal(err)
	}
	if x := Excluded(); len(x) != 1 || !x[0].Quiet || !x[0].Tucked {
		t.Fatalf("tucked: %+v", x)
	}
	if err := TuckAccount("codex", false); err != nil {
		t.Fatal(err)
	}
	if x := Excluded(); len(x) != 1 || !x[0].Quiet || x[0].Tucked {
		t.Fatalf("untucked: %+v", x)
	}
	if err := TuckAccount("codex", true); err != nil {
		t.Fatal(err)
	}
	if err := ShowAccount("codex"); err != nil {
		t.Fatal(err)
	}
	if codex, ok = find(All(), "codex"); !ok || len(codex.Models) != 1 || codex.Account == nil || len(Excluded()) != 0 {
		t.Fatalf("after add back: %+v", codex)
	}
	// removed again, it is reminded of again
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	if x := Excluded(); len(x) != 1 || x[0].Quiet {
		t.Fatalf("removed again: %+v", x)
	}
	if err := ShowAccount("codex"); err != nil {
		t.Fatal(err)
	}
	codex, _ = find(All(), "codex")
	// only through routing groups holds for an account too
	codex.Unlisted = true
	if err := Save(codex); err != nil {
		t.Fatal(err)
	}
	if codex, _ = find(All(), "codex"); !codex.Unlisted {
		t.Fatal("unlisted lost on an account")
	}
	for _, e := range Catalog() {
		if e.Provider.ID == "codex" {
			t.Fatalf("unlisted account in the catalog: %s", e.ID)
		}
	}
	codex.Unlisted = false
	Save(codex)

	// signed out: gone, and a stale picks entry is not a provider
	Save(Provider{ID: "copilot", Models: []string{"gpt-5.5"}})
	os.Remove(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "github-copilot", "apps.json"))
	if _, ok := find(All(), "copilot"); ok {
		t.Fatal("copilot still listed after sign-out")
	}

	// no Claude credential in this home: no claude provider
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("claude listed without credentials")
	}
}

// claudeSignIn writes a Claude Code OAuth login into a temp home.
func claudeSignIn(t *testing.T, home string, expiry time.Time) string {
	t.Helper()
	path := filepath.Join(home, ".claude", ".credentials.json")
	writeFile(t, path, map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken": "sk-ant-oat01-old", "refreshToken": "sk-ant-ort01-old",
			"expiresAt": expiry.UnixMilli(), "subscriptionType": "max",
			"scopes": []string{"user:inference", "user:profile"},
		},
		// what magpie must not clobber when it writes a refreshed token back
		"mcpOAuth": map[string]any{"plugin:slack:slack|x": map[string]any{"serverName": "Slack"}},
	})
	return path
}

// claudeHome sets up a temp home with the usual env; the caller adds credentials.
func claudeHome(t *testing.T) string {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Windows finds the home in USERPROFILE
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	forgetClaudeCredential()
	return home
}

func TestClaudeAccountIsProvider(t *testing.T) {
	home := claudeHome(t)
	creds := claudeSignIn(t, home, time.Now().Add(time.Hour))

	// Claude's models are the catalog's: magpie asks Anthropic nothing with
	// the account's sign-in, not even its model list
	asked := false
	anthropic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
		w.WriteHeader(500)
	}))
	defer anthropic.Close()
	claudeBase = anthropic.URL
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), []byte(`{"anthropic": {"models": {
	  "claude-sonnet-5": {"id":"claude-sonnet-5","name":"Claude Sonnet 5","modalities":{"input":["text"],"output":["text"]}},
	  "claude-opus-5-5": {"id":"claude-opus-5-5","name":"Claude Opus 5.5","modalities":{"input":["text"],"output":["text"]}}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	p, ok := find(All(), "claude")
	if !ok || p.Account == nil || p.Account.User != "Claude Max" || p.Account.Plan != "max" || !p.Ready() {
		t.Fatalf("claude: %+v %v", p, ok)
	}
	if p.Anthropic != claudeBase || p.Chat != "" || p.Responses != "" || p.Host() == "" {
		t.Fatalf("endpoints: %+v", p)
	}

	if got := len(p.Available()); got != 2 {
		t.Fatalf("available before a fetch: %d", got)
	}
	if ms, err := p.Fetch(context.Background()); err != nil || len(ms) != 2 || asked {
		t.Fatalf("fetch: %v %v asked Anthropic: %v", ms, err, asked)
	}
	p, _ = find(All(), "claude")
	if at, ok := p.Fetched(); !ok || time.Since(at) > time.Minute {
		t.Fatalf("not marked fetched: %v %v", at, ok)
	}
	var ids []string
	for _, e := range Catalog() {
		ids = append(ids, e.ID)
	}
	if !contains(ids, "claude/claude-opus-5-5") {
		t.Fatalf("catalog: %v", ids)
	}
	if rp, model, ok := Resolve("claude/claude-opus-5-5"); !ok || rp.ID != "claude" || model != "claude-opus-5-5" {
		t.Fatalf("resolve: %+v %q %v", rp, model, ok)
	}

	// picks are saved, the login never is
	if err := Save(Provider{ID: "claude", Name: "x", Models: []string{"claude-opus-5-5"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "sk-ant-") || !strings.Contains(string(b), `"claude-opus-5-5"`) {
		t.Fatalf("stored: %s", b)
	}
	p, _ = find(All(), "claude")
	if p.Account == nil || len(p.Exposed()) != 1 || p.Exposed()[0].ID != "claude-opus-5-5" {
		t.Fatalf("picks: %+v", p.Exposed())
	}

	// nothing goes to the API in Claude Code's name: the account is used
	// by running Claude Code, so a direct request is refused unsigned and
	// a body is sent as written
	req, _ := http.NewRequest("POST", p.Anthropic+"/v1/messages", nil)
	if err := p.Sign(context.Background(), req, Anthropic, nil); !errors.Is(err, errClaudeViaCLI) || req.Header.Get("Authorization") != "" {
		t.Fatalf("signed a direct request: %v %v", err, req.Header)
	}
	if body := `{"model":"claude-sonnet-5","messages":[]}`; string(p.Prepare([]byte(body))) != body {
		t.Fatalf("prepare: %s", p.Prepare([]byte(body)))
	}

	// signing out of Claude Code removes the provider
	os.Remove(creds)
	forgetClaudeCredential()
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("claude still listed after sign-out")
	}
}

func TestCopilotSignAndModels(t *testing.T) {
	signIn(t)
	var mu sync.Mutex
	var enabled []string
	var api *httptest.Server
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess" || r.Header.Get("Copilot-Integration-Id") == "" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/models" {
			io := `{"data":[
			  {"id":"gpt-5.5","name":"GPT-5.5","model_picker_enabled":true,"capabilities":{"type":"chat","supports":{"reasoning_effort":["low","high"]}}},
			  {"id":"claude-sonnet-5","name":"Claude Sonnet 5","model_picker_category":"versatile","policy":{"state":"disabled","terms":"Enable access"},"capabilities":{"type":"chat"}},
			  {"id":"claude-opus-5","name":"Claude Opus 5","model_picker_category":"powerful","policy":{"state":"disabled"},"capabilities":{"type":"chat"}},
			  {"id":"gpt-4.1","name":"GPT-4.1","model_picker_category":"versatile","policy":{"state":"enabled"},"capabilities":{"type":"chat"}},
			  {"id":"gpt-4.1-2025-04-14","name":"GPT-4.1","policy":{"state":"enabled"},"capabilities":{"type":"chat"}},
			  {"id":"kimi-k3-base","name":"Kimi K3 (GitHub)","vendor":"Experimental","model_picker_category":"powerful","policy":{"state":"enabled"},"capabilities":{"type":"chat"}},
			  {"id":"exec-agent-a","name":"Exec","model_picker_enabled":true,"capabilities":{"type":"chat"}},
			  {"id":"text-embedding-3-small","name":"Emb","model_picker_enabled":true,"capabilities":{"type":"embeddings"}}]}`
			w.Write([]byte(io))
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/policy") {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			enabled = append(enabled, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/models/"), "/policy")+" "+string(b))
			mu.Unlock()
			return
		}
		w.Write([]byte(`{"path":"` + r.URL.Path + `","initiator":"` + r.Header.Get("X-Initiator") + `"}`))
	}))
	defer api.Close()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_x" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	defer tokens.Close()
	old := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	defer func() { CopilotTokenURL = old }()
	copilotSessions = map[string]copilotSession{}

	p, _ := find(All(), "copilot")
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "gpt-5.5,claude-sonnet-5,gpt-4.1,auto" || len(ms[0].Efforts) != 2 {
		t.Fatalf("models: %v %+v", ids, ms)
	}
	p, _ = find(All(), "copilot")
	if got := p.Available(); len(got) != 4 || got[0].ID != "gpt-5.5" {
		t.Fatalf("available after fetch: %+v", got)
	}

	body := []byte(`{"model":"gpt-5.5","messages":[{"role":"user","content":"hi"},{"role":"tool","content":"x"}]}`)
	req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, body); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(req.URL.String(), api.URL) || req.URL.Path != "/chat/completions" || req.Header.Get("X-Initiator") != "agent" {
		t.Fatalf("signed: %s %v", req.URL, req.Header)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	json.NewDecoder(res.Body).Decode(&got)
	if got["path"] != "/chat/completions" || got["initiator"] != "agent" {
		t.Fatalf("api saw %v", got)
	}
	if len(enabled) != 0 {
		t.Fatalf("enabled an enabled model: %v", enabled)
	}

	// a model whose terms wait has them accepted on its first request only
	for range 2 {
		req, _ = http.NewRequest("POST", p.Chat+"/chat/completions", nil)
		if err := p.Sign(context.Background(), req, Chat, []byte(`{"model":"claude-sonnet-5","messages":[]}`)); err != nil {
			t.Fatal(err)
		}
	}
	if len(enabled) != 1 || enabled[0] != `claude-sonnet-5 {"state":"enabled"}` {
		t.Fatalf("enabled: %v", enabled)
	}

	// after a restart the list is asked again before a first request
	copilotTerms = map[string]map[string]bool{}
	enabled = nil
	req, _ = http.NewRequest("POST", p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, []byte(`{"model":"claude-sonnet-5","messages":[]}`)); err != nil {
		t.Fatal(err)
	}
	if len(enabled) != 1 {
		t.Fatalf("enabled after restart: %v", enabled)
	}
}

// Copilot lists GPT-4.1, GPT-4o and the models before them with no
// supported_endpoints, and serves them on chat completions alone: asked on
// /v1/messages, Copilot answers "no model endpoints available given user
// constraints", which read as the plan's refusal (#754: an Education
// account's copilot/gpt-4o from Claude Code). They are asked on Chat, a
// model the list names for no picker (gpt-4o) too.
func TestCopilotModelsWithoutEndpointsOnChat(t *testing.T) {
	signIn(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[
			  {"id":"claude-haiku-4.5","name":"Claude Haiku 4.5","model_picker_enabled":true,"model_picker_category":"lightweight","policy":{"state":"enabled"},"supported_endpoints":["/chat/completions","/v1/messages"],"capabilities":{"type":"chat"}},
			  {"id":"gpt-4.1","name":"GPT-4.1","model_picker_enabled":false,"model_picker_category":"versatile","policy":{"state":"enabled"},"capabilities":{"type":"chat"}},
			  {"id":"gpt-4o","name":"GPT-4o","model_picker_enabled":false,"capabilities":{"type":"chat"}},
			  {"id":"text-embedding-3-small","name":"Emb","model_picker_enabled":false,"capabilities":{"type":"embeddings"}}]}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer api.Close()
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"token": "sess", "expires_at": time.Now().Add(time.Hour).Unix(), "endpoints": map[string]string{"api": api.URL}})
	}))
	defer tokens.Close()
	old := CopilotTokenURL
	CopilotTokenURL = tokens.URL
	defer func() { CopilotTokenURL = old }()
	copilotSessions = map[string]copilotSession{}
	copilotSeenMu.Lock()
	copilotSeen = map[string][]string{}
	copilotSeenMu.Unlock()

	p, _ := find(All(), "copilot")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ = find(All(), "copilot")
	for model, want := range map[string]string{
		"claude-haiku-4.5": "chat anthropic",
		"gpt-4.1":          "chat", // listed
		"gpt-4o":           "chat", // not listed, and asked for by hand
	} {
		var got []string
		for _, a := range p.APIs(model) {
			got = append(got, string(a))
		}
		if strings.Join(got, " ") != want {
			t.Errorf("APIs(%s) = %v, want %s", model, got, want)
		}
	}
	// what Copilot's /v1/messages said is kept, and said not to be the plan
	msg := p.Explain("Copilot: 400 Bad Request: no model endpoints available given user constraints", 400, []byte("no model endpoints available given user constraints\n"))
	if !strings.Contains(msg, "no model endpoints available given user constraints") || !strings.Contains(msg, "Anthropic's Messages API") {
		t.Errorf("explained: %s", msg)
	}
	if got := p.Explain("Copilot: 400", 400, []byte(`{"error":{"code":"model_not_supported"}}`)); got != "Copilot: 400" {
		t.Errorf("a refusal of the model was explained as the API's: %s", got)
	}
}

// Logging out of Claude Code can leave its credentials behind; what the CLI
// says wins, so a signed-out account is not a provider.
func TestClaudeSignedOut(t *testing.T) {
	shellFakes(t)
	home := claudeHome(t)
	claudeSignIn(t, home, time.Now().Add(time.Hour))
	status := `{"loggedIn": true, "email": "me@example.com", "subscriptionType": "max"}`
	exe := filepath.Join(home, "claude")
	fake := func() {
		testenv.Program(t, exe, "#!/bin/sh\ncat <<'X'\n"+status+"\nX\n")
		forgetClaudeStatus()
	}
	claudeExecutable = func() string { return exe }
	fake()
	if p, ok := find(All(), "claude"); !ok || p.Account.User != "me@example.com" {
		t.Fatalf("signed in: %+v %v", p, ok)
	}
	status = `{"loggedIn": false, "authMethod": "none"}`
	fake()
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("claude listed after sign-out")
	}
}

func TestLastRole(t *testing.T) {
	for body, want := range map[string]string{
		`{"messages":[{"role":"system"},{"role":"user"}]}`: "user",
		`{"messages":[{"role":"user"},{"role":"tool"}]}`:   "tool",
		`{"input":"hi"}`: "user",
		`{"input":[{"role":"user","content":"hi"}]}`:                                                                       "user",
		`{"input":[{"role":"user"},{"type":"function_call_output","call_id":"c","output":"x"}]}`:                           "",
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]}]}`:                "tool",
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t"},{"type":"text","text":"and"}]}]}`: "user",
		`{"messages":[{"role":"user","content":"hi"}]}`:                                                                    "user",
		`not json`: "",
	} {
		if got := lastRole([]byte(body)); got != want {
			t.Errorf("lastRole(%s) = %q, want %q", body, got, want)
		}
	}
}

func TestCopilotAPIs(t *testing.T) {
	got := copilotAPIs([]string{"/responses", "ws:/responses", "/v1/messages", "/chat/completions", "/embeddings"})
	if strings.Join(got, " ") != "responses anthropic chat" {
		t.Errorf("copilotAPIs = %v", got)
	}
	// a model listed with no supported_endpoints (GPT-4.1, GPT-4o) is
	// served on chat completions alone (#754)
	for _, none := range [][]string{nil, {}} {
		if got := copilotAPIs(none); strings.Join(got, " ") != "chat" {
			t.Errorf("copilotAPIs(%#v) = %v, want chat", none, got)
		}
	}
}

// A sign-in magpie wrote on several lines comes back from `security -w` as
// hex; it is read, and written on one line so Claude Code can read it (#70).
func TestKeychainText(t *testing.T) {
	c := claudeCredentials{OAuth: claudeAuth{AccessToken: "a", RefreshToken: "r", Scopes: []string{"user:inference"}}}
	indented, _ := c.marshal()
	b, wasHex := keychainText([]byte(hex.EncodeToString(indented)))
	if !wasHex {
		t.Fatal("hex not recognised")
	}
	if got, ok := parseClaudeCredentials(b); !ok || got.OAuth.AccessToken != "a" {
		t.Fatalf("parsed %+v %v", got, ok)
	}
	plain := []byte(`{"claudeAiOauth":{"accessToken":"a"}}`)
	if b, wasHex := keychainText(plain); wasHex || string(b) != string(plain) {
		t.Fatal("plain JSON changed")
	}
	if _, wasHex := keychainText([]byte("abcd")); wasHex {
		t.Fatal("hex that isn't JSON taken")
	}
}

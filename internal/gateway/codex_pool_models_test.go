package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// codexPlanModels are the ChatGPT backend's /codex/models entries, as it
// lists them (slug, display_name, priority, levels, windows): a Pro 5x
// ("prolite") plan's eight, of which a Free plan lists the three below.
var codexPlanModels = []struct {
	slug, name string
	free       bool
	levels     []string
}{
	{"gpt-6.1-sol", "GPT-6.1-Sol", false, []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
	{"gpt-6-astra", "GPT-6-Astra", false, []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
	{"gpt-6-sol", "GPT-6-Sol", false, []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
	{"gpt-6-luna", "GPT-6-Luna", true, []string{"low", "medium", "high", "xhigh", "max"}},
	{"gpt-5.6-sol", "GPT-5.6-Sol", false, []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
	{"gpt-5.6-terra", "GPT-5.6-Terra", true, []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
	{"gpt-5.6-luna", "GPT-5.6-Luna", true, []string{"low", "medium", "high", "xhigh", "max"}},
	{"gpt-5.5", "GPT-5.5", false, []string{"low", "medium", "high", "xhigh"}},
}

func codexPlanList(free bool) string {
	var out []map[string]any
	for i, m := range codexPlanModels {
		if free && !m.free {
			continue
		}
		var ls []map[string]any
		for _, l := range m.levels {
			ls = append(ls, map[string]any{"effort": l, "description": ""})
		}
		out = append(out, map[string]any{"slug": m.slug, "display_name": m.name, "visibility": "list",
			"priority": i + 1, "supported_reasoning_levels": ls, "default_reasoning_level": "medium",
			"input_modalities": []string{"text", "image"}, "context_window": 272000, "max_context_window": 872000,
			"supported_in_api": true, "upgrade": nil})
	}
	b, _ := json.Marshal(map[string]any{"models": out})
	return string(b)
}

// codexFreeBesidePro signs Codex in to a Free ChatGPT account, saves a
// Pro 5x one on beside it, and points the backend at a fake that lists
// each plan's models and refuses the Free account the others. tried takes
// the account:model of each turn the backend was sent since it was last
// called.
func codexFreeBesidePro(t *testing.T) (tried func() string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	restingUntil.Lock()
	restingUntil.m = map[string]time.Time{}
	restingUntil.Unlock()
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct, plan string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token": claims(map[string]any{"email": email,
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": plan}}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": acct}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(auth("free@example.com", "acct-free", "free")), 0o600)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), mustJSON([]map[string]any{
		{"agent": "codex", "user": "pro@example.com", "plan": "prolite", "on": true, "seen": time.Now(), "auth": auth("pro@example.com", "acct-prolite", "prolite")},
	}), 0o600)

	var mu sync.Mutex
	var sent []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acct := r.Header.Get("chatgpt-account-id")
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, codexPlanList(acct == "acct-free"))
			return
		}
		var q struct {
			Model string `json:"model"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &q)
		mu.Lock()
		sent = append(sent, acct+":"+q.Model)
		mu.Unlock()
		if acct == "acct-free" && !slices.Contains([]string{"gpt-6-luna", "gpt-5.6-terra", "gpt-5.6-luna"}, q.Model) {
			w.WriteHeader(400)
			io.WriteString(w, `{"detail":"The '`+q.Model+`' model is not supported when using Codex with a ChatGPT account."}`)
			return
		}
		io.WriteString(w, sse(
			`data: {"type":"response.created","response":{"id":"r1","model":"`+q.Model+`"}}`,
			`data: {"type":"response.output_text.delta","delta":"pong"}`,
			`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
	}))
	t.Cleanup(up.Close)
	old := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })

	return func() string {
		mu.Lock()
		defer mu.Unlock()
		got := strings.Join(sent, ",")
		sent = nil
		return got
	}
}

// Raven on Discord: Codex signed in to a Free ChatGPT account, a Pro 5x
// ("prolite") one saved beside it and used first. The codex provider listed
// only the Free plan's three models, fetched with the sign-in Codex has, so
// GPT-6.1-Sol and the rest of the Pro plan's were nowhere to pick. Its list
// is now what the accounts together have, in the Pro plan's order, and a
// model only the Pro account has goes to it alone.
func TestCodexProviderListsEveryAccountsModels(t *testing.T) {
	tried := codexFreeBesidePro(t)
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	want := "gpt-6.1-sol,gpt-6-astra,gpt-6-sol,gpt-6-luna,gpt-5.6-sol,gpt-5.6-terra,gpt-5.6-luna,gpt-5.5"
	if got := strings.Join(ids, ","); got != want {
		t.Fatalf("the codex provider lists %s, want %s", got, want)
	}
	live, _, _ := catalog.Live("codex")
	if len(live) != len(codexPlanModels) {
		t.Fatalf("the saved list has %d models, want %d", len(live), len(codexPlanModels))
	}
	for _, m := range ms {
		if m.ID == "gpt-6.1-sol" && (m.Name != "GPT-6.1-Sol" || !slices.Contains(m.Efforts, "ultra")) {
			t.Fatalf("gpt-6.1-sol came in as %+v", m)
		}
	}
	// the Free account still has only its own
	free := &provider.Account{Agent: "codex", User: "free@example.com"}
	if free.Lists("gpt-6.1-sol") || !free.Lists("gpt-6-luna") {
		t.Fatal("the Free account's own list took in the Pro plan's models")
	}

	code, body := post(t, "/v1/responses", `{"model":"codex/gpt-6.1-sol","input":"ping"}`)
	got := tried()
	if code != 200 || !strings.Contains(body, "pong") || got != "acct-prolite:gpt-6.1-sol" {
		t.Fatalf("gpt-6.1-sol: status %d, tried %s: %s", code, got, body)
	}
}

// bigfisheatsmall on X: Codex kept signed in to a Free ChatGPT account, a
// Team one the only account on in magpie. dsh listed the Team plan's
// models; Codex, joined to magpie beside its sign-in, was handed the
// backend's list for the Free sign-in and magpie's models without any
// ChatGPT account's (CodexListed), so a model only the Team account has
// was in no picker of Codex's. Codex's list now has those by magpie's id,
// and a turn on one goes to the account that has it.
func TestCodexListHasAnotherAccountsModels(t *testing.T) {
	tried := codexFreeBesidePro(t)
	if err := provider.SetLoginOn("codex", "free@example.com", false); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeepLogin("codex", true); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	// as Codex asks: its own sign-in, the Free account's
	free := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer free-token")
		req.Header.Set("chatgpt-account-id", "acct-free")
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", CodexPath+"/models", nil)
	free(req)
	New().Handler().ServeHTTP(rec, req)
	var list struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("status %d: %v: %s", rec.Code, err, rec.Body)
	}
	var slugs []string
	for _, m := range list.Models {
		slug, _ := m["slug"].(string)
		slugs = append(slugs, slug)
		if v, _ := m["visibility"].(string); slug == "codex/gpt-6.1-sol" && v != "list" {
			t.Errorf("codex/gpt-6.1-sol is %q in Codex's picker, want list", v)
		}
	}
	for _, want := range []string{"gpt-6-luna", "gpt-5.6-terra", "gpt-5.6-luna", "codex/gpt-6.1-sol", "codex/gpt-6-astra", "codex/gpt-5.5"} {
		if !slices.Contains(slugs, want) {
			t.Errorf("Codex's list lacks %s: %v", want, slugs)
		}
	}
	// the Free plan's own stay as the backend lists them, never twice
	for _, dup := range []string{"codex/gpt-6-luna", "codex/gpt-5.6-terra", "codex/gpt-5.6-luna"} {
		if slices.Contains(slugs, dup) {
			t.Errorf("Codex's list has %s beside the backend's own entry: %v", dup, slugs)
		}
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"codex/gpt-6.1-sol","input":"ping","stream":true}`))
	free(req)
	New().Handler().ServeHTTP(rec, req)
	if got := tried(); rec.Code != 200 || !strings.Contains(rec.Body.String(), "pong") || got != "acct-prolite:gpt-6.1-sol" {
		t.Fatalf("codex/gpt-6.1-sol from Codex: status %d, tried %s: %s", rec.Code, got, rec.Body)
	}

	// spawn_agent's models for a lead on another of magpie's models: the
	// Free sign-in's (Codex's models_cache.json) and the Team plan's
	// beyond them, by magpie's id
	if err := os.WriteFile(catalog.CodexModelsCache(), []byte(codexPlanList(true)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Responses: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	var spawn []string
	for _, c := range spawnChoices(req, "relay/m1", true) {
		spawn = append(spawn, c.slug)
	}
	for _, want := range []string{"gpt-6-luna", "codex/gpt-6.1-sol", "relay/m1"} {
		if !slices.Contains(spawn, want) {
			t.Errorf("spawn_agent's models lack %s: %v", want, spawn)
		}
	}
	if slices.Contains(spawn, "codex/gpt-6-luna") {
		t.Errorf("spawn_agent's models have codex/gpt-6-luna beside gpt-6-luna: %v", spawn)
	}
}

// One account, Codex's own sign-in: the backend lists every model it has,
// and Codex's list has none of them again by magpie's id.
func TestCodexListOneAccountNoneTwice(t *testing.T) {
	codexFreeBesidePro(t)
	ls := filepath.Join(filepath.Dir(provider.Path()), "logins.json")
	if err := os.WriteFile(ls, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", CodexPath+"/models", nil)
	req.Header.Set("Authorization", "Bearer free-token")
	req.Header.Set("chatgpt-account-id", "acct-free")
	New().Handler().ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), `"codex/`) {
		t.Fatalf("Codex's list has a codex/ model beside its own sign-in's: %s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"gpt-6-luna"`) {
		t.Fatalf("Codex's list lacks its own gpt-6-luna: %s", rec.Body)
	}
}

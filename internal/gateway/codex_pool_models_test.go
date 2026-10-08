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

// Raven on Discord: Codex signed in to a Free ChatGPT account, a Pro 5x
// ("prolite") one saved beside it and used first. The codex provider listed
// only the Free plan's three models, fetched with the sign-in Codex has, so
// GPT-6.1-Sol and the rest of the Pro plan's were nowhere to pick. Its list
// is now what the accounts together have, in the Pro plan's order, and a
// model only the Pro account has goes to it alone.
func TestCodexProviderListsEveryAccountsModels(t *testing.T) {
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
	var tried []string
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
		tried = append(tried, acct+":"+q.Model)
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
	mu.Lock()
	got := strings.Join(tried, ",")
	tried = nil
	mu.Unlock()
	if code != 200 || !strings.Contains(body, "pong") || got != "acct-prolite:gpt-6.1-sol" {
		t.Fatalf("gpt-6.1-sol: status %d, tried %s: %s", code, got, body)
	}
}

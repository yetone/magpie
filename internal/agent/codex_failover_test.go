package agent

import (
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

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// Codex on one of its own models, signed in to ChatGPT, goes through
// magpie while another of its accounts is on there — so a turn the first
// has no allowance left for goes to the next — and straight to OpenAI
// once none is.
func TestCodexOwnModelThroughMagpieWhileAccountsOn(t *testing.T) {
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": email}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	me, _ := json.Marshal(auth("me@example.com", "acct-1"))
	home, read := codexHome(t, string(me), "model = \"gpt-5.5\"\n")
	cx := codex(home)
	logins := func(on bool) {
		b, _ := json.Marshal([]map[string]any{{"agent": "codex", "user": "spare@example.com", "on": on,
			"seen": time.Now(), "auth": auth("spare@example.com", "acct-2")}})
		os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
		os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), b, 0o600)
	}
	logins(false)
	if err := cx.Fields[0].Set("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "openai_base_url") {
		t.Fatalf("one account:\n%s", cfg)
	}
	logins(true)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `openai_base_url = "http://127.0.0.1:`) || !strings.Contains(cfg, `model = "gpt-5.4"`) {
		t.Fatalf("second account on:\n%s", cfg)
	}
	// picked again, it stays so
	if err := cx.Fields[0].Set("gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "gpt-5.5"`) {
		t.Fatalf("picked with two accounts on:\n%s", cfg)
	}
	logins(false)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "gpt-5.5"`) {
		t.Fatalf("second account off:\n%s", cfg)
	}
}

// A base URL the user wrote in config.toml by hand, to have Codex's own
// models go through magpie, stays when magpie starts (Sync) with no other
// ChatGPT account on to fail over to: failover takes away only the one it
// wrote (#856: it was gone again after every magpie update).
func TestCodexHandWrittenBaseURLStays(t *testing.T) {
	base := gateway.URL() + gateway.CodexPath
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`,
		"model = \"gpt-5.5\"\nopenai_base_url = \""+base+"\"\n")
	cx := codex(home)
	for i := 0; i < 2; i++ {
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		if cfg := read(); !strings.Contains(cfg, `openai_base_url = "`+base+`"`) || !strings.Contains(cfg, `model = "gpt-5.5"`) {
			t.Fatalf("sync %d took the user's base URL away:\n%s", i, cfg)
		}
	}
}

// #1385 (shenghsi): Codex signed in to ChatGPT, not connected, with two
// more of the user's ChatGPT accounts on in magpie. failover points Codex at
// the gateway; the Agents row says it goes through magpie for that and what
// turns it off, and Codex's model list is its own, not its own and 62 of
// magpie's. Connected, it is magpie's models' again.
func TestCodexFailoverOnlyKeepsOwnModels(t *testing.T) {
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": email}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	me, _ := json.Marshal(auth("me@example.com", "acct-1"))
	home, read := codexHome(t, string(me), "model = \"gpt-5.5\"\n")
	var saved []map[string]any
	for i, u := range []string{"spare1@example.com", "spare2@example.com"} {
		saved = append(saved, map[string]any{"agent": "codex", "user": u, "on": true,
			"seen": time.Now(), "auth": auth(u, "acct-"+string(rune('2'+i)))})
	}
	b, _ := json.Marshal(saved)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), b, 0o600)
	// Codex fetched magpie's whole list before
	cache := filepath.Join(home, ".codex", "models_cache.json")
	now := time.Now().UTC().Format(time.RFC3339)
	os.WriteFile(cache, []byte(`{"etag":"\"v1+magpie-`+provider.CodexListTag()+`\"","fetched_at":"`+now+`","models":[]}`), 0o644)

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"models":[{"slug":"gpt-5.5","priority":1}]}`)
	}))
	defer up.Close()
	wasBase, wasHook := provider.CodexBase, gateway.CodexOwnOnly
	provider.CodexBase = up.URL + "/backend-api/codex"
	gateway.CodexOwnOnly = CodexOwnOnly
	defer func() { provider.CodexBase, gateway.CodexOwnOnly = wasBase, wasHook }()
	models := func() []string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", gateway.CodexPath+"/models", nil)
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		gateway.New().Handler().ServeHTTP(rec, req)
		var got struct {
			Models []map[string]any `json:"models"`
		}
		json.Unmarshal(rec.Body.Bytes(), &got)
		var slugs []string
		for _, m := range got.Models {
			slug, _ := m["slug"].(string)
			slugs = append(slugs, slug)
		}
		return slugs
	}

	cx := codex(home)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	s := stashLoad()
	if cfg := read(); !strings.Contains(cfg, "openai_base_url") || s["codex.failover"] != "1" || s["codex.joined"] != "" || s["codex.beside"] != "" {
		t.Fatalf("not the reporter's state: stash %v\n%s", s, cfg)
	}
	if cx.Wired() || !cx.FailingOver() || !CodexOwnOnly() || !strings.Contains(cx.FailoverSaid(), "Providers › Codex") {
		t.Errorf("wired %v, failing over %v, own only %v, said %q", cx.Wired(), cx.FailingOver(), CodexOwnOnly(), cx.FailoverSaid())
	}
	if got := models(); len(got) != 1 || got[0] != "gpt-5.5" {
		t.Errorf("failover-only Codex lists %v, want just gpt-5.5", got)
	}
	if c, _ := os.ReadFile(cache); !strings.Contains(string(c), "1970-01-01") {
		t.Errorf("Codex's cached list of magpie's models kept:\n%s", c)
	}

	// connected, magpie's models are in its list and the row says connected
	if ok, err := cx.Join(); !ok || err != nil {
		t.Fatalf("join %v %v", ok, err)
	}
	if !cx.Wired() || cx.FailingOver() || CodexOwnOnly() || cx.FailoverSaid() != "" {
		t.Errorf("joined: wired %v, failing over %v, own only %v", cx.Wired(), cx.FailingOver(), CodexOwnOnly())
	}
	if got := strings.Join(models(), " "); !strings.Contains(got, "fake/m1") {
		t.Errorf("joined Codex lists %v, want magpie's too", got)
	}
}

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

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// TylerBai_loveai on X: Codex (the app) signed in to ChatGPT and not
// connected, two more of the user's ChatGPT accounts added and on in
// magpie. magpie pointed Codex at its gateway by itself for account
// failover (#1385), so every Codex request went through magpie while the
// Agents row said not connected, until a Disconnect and cleanup. A Codex
// that isn't connected goes straight to OpenAI: no start, sync or pick of
// one of its own models writes magpie into its config, however many of its
// accounts are on.
func TestCodexNotConnectedStaysOffMagpie(t *testing.T) {
	me, _ := json.Marshal(codexChatGPTAuth("me@example.com", "acct-1"))
	const own = "model = \"gpt-5.5\"\n"
	home, read := codexHome(t, string(me), own)
	var saved []map[string]any
	for i, u := range []string{"spare1@example.com", "spare2@example.com"} {
		saved = append(saved, map[string]any{"agent": "codex", "user": u, "on": true,
			"seen": time.Now(), "auth": codexChatGPTAuth(u, "acct-"+string(rune('2'+i)))})
	}
	b, _ := json.Marshal(saved)
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), b, 0o600)
	cx := codex(home)
	check := func(when string) {
		t.Helper()
		cfg := read()
		if strings.Contains(cfg, "openai_base_url") || strings.Contains(cfg, "model_provider") || strings.Contains(cfg, "127.0.0.1") {
			t.Fatalf("%s: an unconnected Codex pointed at magpie:\n%s", when, cfg)
		}
		if cx.Wired() || cx.FailingOver() || cx.FailoverSaid() != "" || CodexOwnOnly() {
			t.Fatalf("%s: wired %v, failing over %v", when, cx.Wired(), cx.FailingOver())
		}
		if s := stashLoad(); s["codex.failover"] != "" {
			t.Fatalf("%s: failover marked: %v", when, s)
		}
	}
	SyncCatalog()
	check("magpie's start")
	for i := 0; i < 2; i++ {
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		check("sync")
	}
	if err := cx.Pick("model", "gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model = "gpt-5.4"`) {
		t.Fatalf("pick not written:\n%s", cfg)
	}
	check("own model picked")
}

// A Codex a magpie from before pointed at the gateway for failover, the
// reporter's config as they upgrade: the base URL it marked goes as Sync
// next runs, the Codex app's app-server restarted for it like any change of
// route, and it stays gone. So does CC Switch's OpenAI mirror that failover
// pointed at magpie (xuxing0727 on X), back to CC Switch's own bytes.
func TestCodexFailoverFromBeforeTakenOut(t *testing.T) {
	me, _ := json.Marshal(codexChatGPTAuth("me@example.com", "acct-1"))
	accountsOn := func() {
		b, _ := json.Marshal([]map[string]any{{"agent": "codex", "user": "spare@example.com", "on": true,
			"seen": time.Now(), "auth": codexChatGPTAuth("spare@example.com", "acct-2")}})
		os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
		os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), b, 0o600)
	}
	home, read := codexHome(t, string(me), "model = \"gpt-5.5\"\n")
	accountsOn()
	if err := edit.SetTOMLTop(filepath.Join(home, ".codex", "config.toml"), edit.KV{Path: "openai_base_url", Value: here(home).codexURL()}); err != nil {
		t.Fatal(err)
	}
	stash(map[string]string{"codex.failover": "1"})
	cx := codex(home)
	if !cx.FailingOver() {
		t.Fatalf("not the reporter's state:\n%s", read())
	}
	for i := 0; i < 2; i++ {
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		if cfg := read(); cfg != "model = \"gpt-5.5\"\n" {
			t.Fatalf("sync %d left failover's base URL:\n%s", i, cfg)
		}
		if cx.FailingOver() || stashLoad()["codex.failover"] != "" {
			t.Fatalf("sync %d: failing over %v", i, cx.FailingOver())
		}
	}

	const unified = "model_provider = \"custom\"\nmodel = \"gpt-5.5\"\n\n" +
		"[model_providers.custom]\nname = \"OpenAI\"\nrequires_openai_auth = true\nsupports_websockets = true\nwire_api = \"responses\"\n"
	home, read = codexHome(t, string(me), unified)
	accountsOn()
	if err := edit.SetTOMLKey(filepath.Join(home, ".codex", "config.toml"), "model_providers.custom", "base_url", here(home).codexURL()); err != nil {
		t.Fatal(err)
	}
	stash(map[string]string{"codex.mirror_failover": "custom"})
	cx = codex(home)
	if !cx.FailingOver() {
		t.Fatalf("not the mirror's state:\n%s", read())
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); cfg != unified || cx.FailingOver() {
		t.Fatalf("CC Switch's mirror left on magpie (failing over %v):\n%s", cx.FailingOver(), cfg)
	}
}

// A base URL the user wrote in config.toml by hand, to have Codex's own
// models go through magpie, stays when magpie starts (Sync): what failover
// wrote is taken away, and only that (#856: it was gone again after every
// magpie update).
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
// more of the user's ChatGPT accounts on in magpie, pointed at the gateway
// by a magpie from before for failover. Till Sync takes that out, the
// Agents row says it goes through magpie for that, and Codex's model list
// is its own, not its own and 62 of magpie's. Connected, it is magpie's
// models' again.
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
	// what a magpie from before wrote for failover
	if err := edit.SetTOMLTop(filepath.Join(home, ".codex", "config.toml"), edit.KV{Path: "openai_base_url", Value: here(home).codexURL()}); err != nil {
		t.Fatal(err)
	}
	stash(map[string]string{"codex.failover": "1"})

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
	if cfg := read(); !strings.Contains(cfg, "openai_base_url") {
		t.Fatalf("not the reporter's state:\n%s", cfg)
	}
	if cx.Wired() || !cx.FailingOver() || !CodexOwnOnly() || !strings.Contains(cx.FailoverSaid(), "Providers › Codex") {
		t.Errorf("wired %v, failing over %v, own only %v, said %q", cx.Wired(), cx.FailingOver(), CodexOwnOnly(), cx.FailoverSaid())
	}
	if got := models(); len(got) != 1 || got[0] != "gpt-5.5" {
		t.Errorf("failover-only Codex lists %v, want just gpt-5.5", got)
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

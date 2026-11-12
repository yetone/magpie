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
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// codexDeskReset forgets the backend's list for the Codex app, before the
// test and after it.
func codexDeskReset(t *testing.T) {
	t.Helper()
	reset := func() {
		codexDesk.Lock()
		done := codexDesk.done
		codexDesk.Unlock()
		if done != nil {
			<-done
		}
		codexDesk.Lock()
		codexDesk.body, codexDesk.got, codexDesk.tried, codexDesk.asking, codexDesk.done, codexDesk.revoked, codexDesk.failed = nil, time.Time{}, time.Time{}, false, nil, false, false
		codexDesk.who, codexDesk.asked, codexDesk.want = "", "", ""
		codexDesk.Unlock()
	}
	reset()
	t.Cleanup(reset)
	was := CodexDesktopChanged
	t.Cleanup(func() { CodexDesktopChanged = was })
}

// codexDeskAsked waits for the ask CodexDesktopCatalog started.
func codexDeskAsked(t *testing.T) {
	t.Helper()
	codexDesk.Lock()
	done := codexDesk.done
	codexDesk.Unlock()
	if done == nil {
		t.Fatal("the backend was never asked")
	}
	<-done
}

// The list the Codex app reads from a file beside the ChatGPT sign-in
// (laddieyao on Discord): the backend's entries as it sent them, its
// base_instructions untouched, asked for with Codex's own sign-in, and
// magpie's models after them. Not known until the backend has answered,
// and a failed ask after that keeps the last list it gave: unknown, never
// an empty list.
func TestCodexDesktopCatalog(t *testing.T) {
	codexSignedIn(t)
	codexDeskReset(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: "http://127.0.0.1:1",
		Models: []string{"claude-opus-5.5"}}); err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	var auth atomic.Value
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		if fail.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		io.WriteString(w, `{"models":[{"slug":"gpt-5.5","priority":1,"base_instructions":"You are Codex, based on GPT-5.","visibility":"list"}]}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()
	var changed atomic.Int32
	CodexDesktopChanged = func() { changed.Add(1) }

	if b, ok := CodexDesktopCatalog(); ok || b != nil {
		t.Fatalf("known before the backend answered: %s", b)
	}
	codexDeskAsked(t)
	if changed.Load() != 1 {
		t.Fatalf("changed told %d times", changed.Load())
	}
	if a, _ := auth.Load().(string); a == "" {
		t.Fatal("asked without Codex's sign-in")
	}
	b, ok := CodexDesktopCatalog()
	if !ok {
		t.Fatal("not known after the backend answered")
	}
	var got struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	slugs := map[string]map[string]any{}
	for _, m := range got.Models {
		slugs[m["slug"].(string)] = m
	}
	if m := slugs["gpt-5.5"]; m == nil || m["base_instructions"] != "You are Codex, based on GPT-5." {
		t.Fatalf("the backend's entry: %v", m)
	}
	if slugs["relay/claude-opus-5.5"] == nil {
		t.Fatalf("magpie's models missing: %s", b)
	}

	// an ask that fails keeps the list the backend gave last
	fail.Store(true)
	codexDesk.Lock()
	codexDesk.got, codexDesk.tried = time.Time{}, time.Time{}
	codexDesk.Unlock()
	if _, ok := CodexDesktopCatalog(); !ok {
		t.Fatal("the last list forgotten as the backend is asked again")
	}
	codexDeskAsked(t)
	if b2, ok := CodexDesktopCatalog(); !ok || string(b2) != string(b) {
		t.Fatalf("after a failed ask: %v\n%s", ok, b2)
	}
	if changed.Load() != 1 {
		t.Fatalf("a failed ask told as a change")
	}
}

// The list is the list of the account Codex is signed in to. Switched to
// another account (chzbz on X: a 20x one, GPT-6.1 Sol not in the Codex
// app's picker), the list from before is not handed out as this one's:
// the backend is asked at once, not an hour on, and the file then has
// the new account's models.
func TestCodexDesktopCatalogFollowsTheSignIn(t *testing.T) {
	codexSignedIn(t)
	codexDeskReset(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// the access token's claims say whose sign-in asked
		var tok []byte
		if parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "."); len(parts) == 3 {
			tok, _ = base64.RawURLEncoding.DecodeString(parts[1])
		}
		if strings.Contains(string(tok), `"acct-pro"`) {
			io.WriteString(w, `{"models":[{"slug":"gpt-6.1-sol","priority":1,"visibility":"list"},{"slug":"gpt-5.5","priority":2,"visibility":"list"}]}`)
			return
		}
		io.WriteString(w, `{"models":[{"slug":"gpt-5.5","priority":1,"visibility":"list"}]}`)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()
	var changed atomic.Int32
	CodexDesktopChanged = func() { changed.Add(1) }
	has := func(b []byte, slug string) bool {
		var got struct {
			Models []map[string]any `json:"models"`
		}
		json.Unmarshal(b, &got)
		for _, m := range got.Models {
			if m["slug"] == slug {
				return true
			}
		}
		return false
	}

	CodexDesktopCatalog()
	codexDeskAsked(t)
	b, ok := CodexDesktopCatalog()
	if !ok || has(b, "gpt-6.1-sol") {
		t.Fatalf("the Plus account's list: %v\n%s", ok, b)
	}

	// Codex signed in to the Pro account now, as magpie's switch writes it
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	pro := map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token": claims(map[string]any{"email": "pro@example.com",
			"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro"}}),
		"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "who": "acct-pro"}),
		"refresh_token": "r-pro", "account_id": "acct-pro"}}
	home, _ := os.UserHomeDir()
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), mustJSON(pro), 0o600); err != nil {
		t.Fatal(err)
	}
	told := changed.Load()
	if b, ok := CodexDesktopCatalog(); ok {
		t.Fatalf("the other account's list handed out as the Pro account's:\n%s", b)
	}
	codexDeskAsked(t)
	if changed.Load() == told {
		t.Fatal("the Pro account's list not told as a change")
	}
	if b, ok := CodexDesktopCatalog(); !ok || !has(b, "gpt-6.1-sol") {
		t.Fatalf("the Pro account's list: %v\n%s", ok, b)
	}
}

// A model the vendor lists after the picks were saved (OpenAI listed
// GPT-6.1 Terra to ChatGPT accounts on 10-26) stays in the account's own
// list: the picks it is narrowed by are the picks with those listed since
// (provider.Picks), as magpie's catalog serves them — or the file and the
// list Codex reads froze at what was listed when the user picked, while
// /v1/models had the model (wenzijie2002: codex/gpt-6.1-terra picked in
// magpie, the Codex app's picker without it, the log "fetched its list
// again, 8 models" and 7 in the file).
func TestCodexDesktopCatalogKeepsNewlyListedBesidePicks(t *testing.T) {
	codexSignedIn(t)
	codexDeskReset(t)
	list := `{"models":[{"slug":"gpt-6-sol","priority":1,"visibility":"list"}]}`
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, list)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()

	// picks saved while the account listed gpt-6-sol alone
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "codex", Models: []string{"gpt-6-sol"}}); err != nil {
		t.Fatal(err)
	}

	// gpt-6.1-terra listed since; the account's list asked for again
	list = `{"models":[{"slug":"gpt-6-sol","priority":1,"visibility":"list"},` +
		`{"slug":"gpt-6.1-terra","priority":2,"visibility":"list","base_instructions":"You are Codex, based on GPT-6.1 Terra."}]}`
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	CodexDesktopCatalog()
	codexDeskAsked(t)
	b, ok := CodexDesktopCatalog()
	if !ok {
		t.Fatal("not known after the backend answered")
	}
	var got struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	var own []string
	for _, m := range got.Models {
		if s, _ := m["slug"].(string); slices.Contains([]string{"gpt-6-sol", "gpt-6.1-terra"}, s) {
			own = append(own, s)
		}
	}
	if !slices.Equal(own, []string{"gpt-6-sol", "gpt-6.1-terra"}) {
		t.Errorf("the account's own models beside the picks: %v\n%s", own, b)
	}
}

// A backend that can't be reached from here (a firewall, a proxy that
// leaves chatgpt.com out; not_4O4 on X, Windows) never gave a list, so the
// file for the Codex app was never written and its picker had GPT alone
// beside the sign-in. Once the ask has failed with no list from before,
// Codex's own last list (models_cache.json) stands in, with magpie's
// models after it; with no such cache the file stays as it was.
func TestCodexDesktopCatalogBackendUnreachable(t *testing.T) {
	codexSignedIn(t)
	codexDeskReset(t)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Anthropic: "http://127.0.0.1:1",
		Models: []string{"claude-opus-5.5"}}); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()
	var changed atomic.Int32
	CodexDesktopChanged = func() { changed.Add(1) }

	// no cache either: unknown, the file as it was
	if _, ok := CodexDesktopCatalog(); ok {
		t.Fatal("known before the backend answered")
	}
	codexDeskAsked(t)
	if b, ok := CodexDesktopCatalog(); ok {
		t.Fatalf("no list and no cache, yet known: %s", b)
	}
	if changed.Load() != 1 {
		t.Fatalf("the failed ask told %d times, want once", changed.Load())
	}

	if err := os.WriteFile(catalog.CodexModelsCache(),
		[]byte(`{"etag":"x","models":[{"slug":"gpt-5.5","priority":1,"base_instructions":"You are Codex, based on GPT-5.","visibility":"list"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	codexDesk.Lock()
	codexDesk.tried = time.Time{}
	codexDesk.Unlock()
	CodexDesktopCatalog()
	codexDeskAsked(t)
	b, ok := CodexDesktopCatalog()
	if !ok {
		t.Fatal("the cached list does not stand in for a backend that can't be reached")
	}
	if !strings.Contains(string(b), `"gpt-5.5"`) || !strings.Contains(string(b), "relay/claude-opus-5.5") {
		t.Fatalf("want Codex's cached GPT and magpie's models:\n%s", b)
	}
}

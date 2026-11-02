package gateway

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
		codexDesk.body, codexDesk.got, codexDesk.tried, codexDesk.asking, codexDesk.done, codexDesk.revoked = nil, time.Time{}, time.Time{}, false, nil, false
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

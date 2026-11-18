package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The Codex app's picker follows the order dragged on the Agents page
// (Codex's list, Order), for every kind of entry in it: the account's own
// models the backend lists, the account's own the backend's list for the
// app lacks yet (handed by magpie's id, codex/<slug>, "· Codex"), routing
// groups and another provider's models (Wangalan167673 on X: GPT-6.2 Astra
// and Sol last, Gemini 4 Hydrogen before Argon, the groups after GPT-6.1
// Sol).
func TestCodexDesktopCatalogInUsersOrder(t *testing.T) {
	codexSignedIn(t)
	codexDeskReset(t)
	full := `{"models":[` +
		`{"slug":"gpt-6.1-sol","priority":1,"visibility":"list","base_instructions":"You are Codex."},` +
		`{"slug":"gpt-6.1-terra","priority":2,"visibility":"list","base_instructions":"You are Codex."},` +
		`{"slug":"gpt-6.2-astra","priority":3,"visibility":"list","base_instructions":"You are Codex."},` +
		`{"slug":"gpt-6.2-sol","priority":4,"visibility":"list","base_instructions":"You are Codex."}]}`
	short := `{"models":[` +
		`{"slug":"gpt-6.1-sol","priority":1,"visibility":"list","base_instructions":"You are Codex."},` +
		`{"slug":"gpt-6.1-terra","priority":2,"visibility":"list","base_instructions":"You are Codex."}]}`
	var list atomic.Value
	list.Store(full)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, list.Load().(string))
	}))
	defer up.Close()
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	defer func() { provider.CodexBase = was }()

	// the account's own list has GPT-6.2; the one asked for the app not yet
	p, err := provider.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	list.Store(short)
	if err := provider.Save(provider.Provider{ID: "agy-cli", Name: "agy-cli", Key: "k", Anthropic: "http://127.0.0.1:1",
		Models: []string{"gemini-4-hydrogen", "gemini-4-argon", "claude-opus-5.5", "claude-sonnet-5.5"}}); err != nil {
		t.Fatal(err)
	}
	for _, g := range []string{"free-workers", "code-review", "code-daily"} {
		if err := provider.SaveGroup(provider.Group{ID: g, Name: g, Members: []string{"agy-cli/gemini-4-argon"}}); err != nil {
			t.Fatal(err)
		}
	}

	// the order as the Agents page saves it: by entry id
	byModel := map[string]string{}
	for _, e := range provider.CatalogOf("codex") {
		key := e.ID
		if provider.CodexOwn(e) {
			key = e.Model
		}
		byModel[key] = e.ID
	}
	var order, want []string
	for _, k := range []string{"gpt-6.1-terra", "gpt-6.1-sol", "gpt-6.2-sol", "gpt-6.2-astra",
		"group/code-daily", "group/code-review", "group/free-workers",
		"agy-cli/gemini-4-argon", "agy-cli/gemini-4-hydrogen", "agy-cli/claude-opus-5.5", "agy-cli/claude-sonnet-5.5"} {
		id, ok := byModel[k]
		if !ok {
			t.Fatalf("no catalog entry for %s: %v", k, byModel)
		}
		order = append(order, id)
		want = append(want, k)
	}
	if err := provider.SetModelOrder("codex", order); err != nil {
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
	// as the app lists them: by priority
	slices.SortStableFunc(got.Models, func(a, b map[string]any) int {
		return int(a["priority"].(float64) - b["priority"].(float64))
	})
	var shown []string
	for _, m := range got.Models {
		if v, _ := m["visibility"].(string); v == "hide" {
			continue
		}
		slug := m["slug"].(string)
		// the account's own the app's list lacks goes by magpie's id
		slug = strings.TrimPrefix(slug, "codex/")
		shown = append(shown, slug)
	}
	if !slices.Equal(shown, want) {
		t.Errorf("the Codex app's list\n got %v\nwant %v", shown, want)
	}
}

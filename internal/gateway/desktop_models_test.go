package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// Claude Desktop's model menu lists the models picked for it on the Agents
// page (蓝猫): /v1/models, asked with its key, holds those and no other, each
// named by its own name (no provider's beside it, no description after it),
// and two of one name keep their provider's after it so they stay apart.
func TestClaudeDesktopPickedModels(t *testing.T) {
	setup(t, provider.Anthropic, &fake{})
	for _, p := range []provider.Provider{
		{ID: "opencode-go", Name: "OpenCode Go", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4.1-flash", "glm-5"}},
		{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"deepseek-v4.1-flash"}},
		{ID: "grok-2", Name: "Grok", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"grok-4.6"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for ref, name := range map[string]string{"opencode-go/deepseek-v4.1-flash": "DeepSeek V4.1 Flash", "relay/deepseek-v4.1-flash": "DeepSeek V4.1 Flash", "grok-2/grok-4.6": "grok-4.6"} {
		if err := provider.SetModelName(ref, name); err != nil {
			t.Fatal(err)
		}
	}
	list := func() map[string]desktopRow {
		t.Helper()
		req := httptest.NewRequest("GET", "/v1/models?limit=1000", nil)
		req.Header.Set("x-api-key", TokenFor("claude-desktop"))
		rec := httptest.NewRecorder()
		New().Handler().ServeHTTP(rec, req)
		var out struct {
			Data []desktopRow `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		got := map[string]desktopRow{}
		for _, d := range out.Data {
			if d.Description != "" {
				t.Errorf("%s has a description: %q", d.DisplayName, d.Description)
			}
			got[DesktopCatalogID(d.ID)] = d
		}
		return got
	}
	keys := func(m map[string]desktopRow) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}

	// both DeepSeeks shown: each with its provider's after it
	all := list()
	if all["opencode-go/deepseek-v4.1-flash"].DisplayName != "DeepSeek V4.1 Flash (opencode-go)" ||
		all["relay/deepseek-v4.1-flash"].DisplayName != "DeepSeek V4.1 Flash (relay)" || all["grok-2/grok-4.6"].DisplayName != "grok-4.6" {
		t.Fatalf("both shown: %v", all)
	}

	// three picked, the other DeepSeek, the routing group magpie found for
	// the two and GLM left out: only those three, the DeepSeek by its name
	// alone
	hide := []string{"relay/deepseek-v4.1-flash", "opencode-go/glm-5"}
	for id := range all {
		if strings.HasPrefix(id, provider.GroupPrefix) {
			hide = append(hide, id)
		}
	}
	if err := provider.SetHiddenModels("claude-desktop", hide); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.SetHiddenModels("claude-desktop", nil) })
	got := list()
	if want := []string{"fake/m1", "grok-2/grok-4.6", "opencode-go/deepseek-v4.1-flash"}; !slices.Equal(keys(got), want) {
		t.Fatalf("Desktop lists %v, want %v", keys(got), want)
	}
	if n := got["opencode-go/deepseek-v4.1-flash"].DisplayName; n != "DeepSeek V4.1 Flash" {
		t.Fatalf("named %q, want the name alone", n)
	}
	// another agent's list is its own
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/models", nil))
	var other struct{ Data []struct{ ID string } }
	json.Unmarshal(rec.Body.Bytes(), &other)
	if len(other.Data) != len(all) {
		t.Fatalf("another agent's list: %s", rec.Body)
	}
}

// desktopNames: the name alone, a provider's id after two of one name, the
// whole id where that still leaves two alike.
func TestDesktopNames(t *testing.T) {
	es := []provider.Entry{
		{ID: "a/x", Name: "X"}, {ID: "b/x", Name: "x"}, {ID: "c/y", Name: "Y"},
		{ID: "d/z", Name: "Z"}, {ID: "d/z2", Name: "Z"}, {ID: "e/w"},
	}
	want := []string{"X (a)", "x (b)", "Y", "Z (d/z)", "Z (d/z2)", "e/w"}
	if got := desktopNames(es); !slices.Equal(got, want) {
		t.Fatalf("desktopNames = %q, want %q", got, want)
	}
}

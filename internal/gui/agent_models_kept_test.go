package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// whysugarsweet on X: Claude Desktop narrowed to some providers (magpie
// visible, from the terminal) never listed OpenCode Go added after, while
// Pi listed it, and the Agents page's list had no way to it. The models
// the visibility keeps are in the list, off; ticking one shows the agent
// its provider, and with nothing kept after, the agent is narrowed no more.
func TestAgentModelsKeptByVisibility(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, p := range []provider.Provider{
		{ID: "deepseek-x", Name: "DeepSeek X", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"d1"}},
		{ID: "opencode-go", Name: "OpenCode Go", Preset: "opencode-go", Key: "k", Anthropic: provider.OpenCodeGoAnthropic, Chat: provider.OpenCodeGoChat, Models: []string{"kimi-k2.6", "glm-5.1"}},
		{ID: "zen-x", Name: "Zen X", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"z1"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	s := settings.Load()
	s.Visible = map[string][]string{"claude-desktop": {"deepseek-x"}}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	agentModelsAPI(mux)
	call := func(method, body string) (out struct {
		Models []agentModelJSON
		Count  *modelCountJSON
	}) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, "/api/agent-models/claude-desktop", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s %d %s", method, w.Code, w.Body)
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	find := func(ms []agentModelJSON, id string) agentModelJSON {
		t.Helper()
		for _, m := range ms {
			if m.ID == id {
				return m
			}
		}
		t.Fatalf("no %s in %+v", id, ms)
		return agentModelJSON{}
	}
	got := call("GET", "")
	if m := find(got.Models, "deepseek-x/d1"); m.Kept || m.Hidden {
		t.Fatalf("listed one: %+v", m)
	}
	for _, id := range []string{"opencode-go/kimi-k2.6", "opencode-go/glm-5.1", "zen-x/z1"} {
		if m := find(got.Models, id); !m.Kept || !m.Hidden || m.Group == "" {
			t.Fatalf("kept one not in the list, off: %+v", m)
		}
	}

	// a list sending the kept ones among what it hides takes none of them
	// out one by one, and narrows the agent no differently
	got = call("POST", `{"hidden":["opencode-go/kimi-k2.6","opencode-go/glm-5.1","zen-x/z1"]}`)
	if h := settings.Load().HiddenModels["claude-desktop"]; len(h) != 0 {
		t.Fatalf("kept ones saved as taken out: %v", h)
	}
	if got.Count == nil || got.Count.Shown != 1 || got.Count.Listed != 1 {
		t.Fatalf("count: %+v", got.Count)
	}

	// Kimi ticked: OpenCode Go is shown, GLM listed and off
	got = call("POST", `{"hidden":["opencode-go/glm-5.1","zen-x/z1"],"widen":["opencode-go/kimi-k2.6"]}`)
	if v := settings.Load().Visible["claude-desktop"]; !slices.Equal(v, []string{"deepseek-x", "opencode-go"}) {
		t.Fatalf("visible: %v", v)
	}
	if m := find(got.Models, "opencode-go/kimi-k2.6"); m.Kept || m.Hidden {
		t.Fatalf("ticked: %+v", m)
	}
	if m := find(got.Models, "opencode-go/glm-5.1"); m.Kept || !m.Hidden {
		t.Fatalf("its provider's other: %+v", m)
	}
	if m := find(got.Models, "zen-x/z1"); !m.Kept {
		t.Fatalf("another provider widened too: %+v", m)
	}
	if got.Count == nil || got.Count.Shown != 2 || got.Count.Listed != 3 {
		t.Fatalf("count: %+v", got.Count)
	}
	shown, _ := provider.CatalogFor("claude-desktop")
	if !slices.ContainsFunc(shown, func(e provider.Entry) bool { return e.ID == "opencode-go/kimi-k2.6" }) {
		t.Fatalf("Desktop's catalog lacks it: %v", shown)
	}

	// the last kept one ticked: nothing is kept, so it is narrowed no more
	call("POST", `{"hidden":["opencode-go/glm-5.1"],"widen":["zen-x/z1"]}`)
	if v, ok := settings.Load().Visible["claude-desktop"]; ok {
		t.Fatalf("still narrowed: %v", v)
	}
	if h := settings.Load().HiddenModels["claude-desktop"]; !slices.Equal(h, []string{"opencode-go/glm-5.1"}) {
		t.Fatalf("taken out: %v", h)
	}
}

// Shown only its picks, a kept one ticked is a pick of the agent's.
func TestAgentModelsKeptPicked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"a1"}},
		{ID: "late", Name: "Late", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"b1", "b2"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	s := settings.Load()
	s.Visible = map[string][]string{"pi": {"relay"}}
	s.PickedModels = map[string][]string{"pi": {"relay/a1"}}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	agentModelsAPI(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/agent-models/pi", strings.NewReader(`{"shown":["relay/a1","late/b2"],"widen":["late/b2"]}`)))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if p := settings.Load().PickedModels["pi"]; !slices.Equal(p, []string{"late/b2", "relay/a1"}) {
		t.Fatalf("picks: %v", p)
	}
	if _, ok := settings.Load().Visible["pi"]; ok {
		t.Fatal("still narrowed with nothing kept")
	}
}

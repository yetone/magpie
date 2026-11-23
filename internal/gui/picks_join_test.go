package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// eiyuuzaki2333 on X: an agent shown only the models picked for it (#1337)
// had every model ticked in a new provider's editor ticked again on the
// Agents page. With its own switch on, the models ticked in a provider's
// editor, as it is added or later, are ticked for it too; one unticked
// for the agent since stays off, a model the provider served before it was
// picked doesn't join, and an agent without the switch keeps its list.
func TestProviderPicksJoinAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"magpie/relay/a1\"\n"), 0o600)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"a1", "a2"}}); err != nil {
		t.Fatal(err)
	}
	h := Handler(nil, nil)
	do := func(method, path, body string) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s %s: %d %s", method, path, body, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	joins := func() bool {
		var out struct{ Joins bool }
		json.Unmarshal(do("GET", "/api/agent-models/codex", ""), &out)
		return out.Joins
	}
	picks := func(a string) []string { return settings.Load().PickedModels[a] }

	do("POST", "/api/agent-models/codex", `{"only":true}`)
	if joins() {
		t.Fatal("on before it was switched on")
	}
	do("POST", "/api/agent-models/codex", `{"joins":true}`)
	if !joins() {
		t.Fatal("the switch isn't kept")
	}
	// another agent shown only its picks, its switch off
	s := settings.Load()
	s.PickedModels["claude"] = []string{"relay/a1"}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}

	// a provider added with two models picked
	do("POST", "/api/provider/save", `{"new":true,"id":"fresh","name":"Fresh","chat":"http://127.0.0.1:2/v1","key":"k","models":["x1","x2"],"headers":{}}`)
	if got := picks("codex"); !slices.Equal(got, []string{"fresh/x1", "fresh/x2", "relay/a1", "relay/a2"}) {
		t.Fatalf("codex after the add: %v", got)
	}
	if got := picks("claude"); !slices.Equal(got, []string{"relay/a1"}) {
		t.Fatalf("an agent without the switch was changed: %v", got)
	}
	// unticked for the agent, then a model picked later in the editor:
	// only that one joins
	do("POST", "/api/agent-models/codex", `{"shown":["relay/a1","relay/a2","fresh/x2"]}`)
	do("POST", "/api/provider/save", `{"id":"fresh","from":"fresh","name":"Fresh","chat":"http://127.0.0.1:2/v1","models":["x1","x2","x3"],"headers":{}}`)
	if got := picks("codex"); !slices.Equal(got, []string{"fresh/x2", "fresh/x3", "relay/a1", "relay/a2"}) {
		t.Fatalf("codex after the edit: %v", got)
	}

	// a provider serving its vendor's list, with nothing picked, gives
	// nothing; a first untick that makes the rest picks gives none of them
	if err := provider.Save(provider.Provider{ID: "wide", Name: "Wide", Chat: "http://127.0.0.1:3/v1", Models: nil}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("wide", "http://127.0.0.1:3/v1", []catalog.Model{{ID: "w1"}, {ID: "w2"}, {ID: "w3"}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := provider.Find("wide"); p == nil || len(p.Exposed()) != 3 {
		t.Fatal("the vendor's list isn't served; the case isn't reached")
	}
	do("POST", "/api/provider/save", `{"id":"wide","from":"wide","name":"Wide","chat":"http://127.0.0.1:3/v1","models":["w1","w2"],"headers":{}}`)
	if got := picks("codex"); slices.ContainsFunc(got, func(id string) bool { return strings.HasPrefix(id, "wide/") }) {
		t.Fatalf("a model served before it was picked joined: %v", got)
	}

	// switched off: a model picked later stays off
	do("POST", "/api/agent-models/codex", `{"joins":false}`)
	do("POST", "/api/provider/save", `{"id":"fresh","from":"fresh","name":"Fresh","chat":"http://127.0.0.1:2/v1","models":["x1","x2","x3","x4"],"headers":{}}`)
	if slices.Contains(picks("codex"), "fresh/x4") {
		t.Fatalf("switched off, a pick joined: %v", picks("codex"))
	}
}

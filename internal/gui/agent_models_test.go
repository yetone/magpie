package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The Agents page's model list for an agent: every model it may be shown,
// the one it's set to marked and never taken out.
func TestAgentModelsAPI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"magpie/relay/m1\"\n"), 0o600)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2", "m3"}}); err != nil {
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
		mux.ServeHTTP(w, httptest.NewRequest(method, "/api/agent-models/codex", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s %d %s", method, w.Code, w.Body)
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	find := func(ms []agentModelJSON, id string) agentModelJSON {
		for _, m := range ms {
			if m.ID == id {
				return m
			}
		}
		t.Fatalf("no %s in %+v", id, ms)
		return agentModelJSON{}
	}
	got := call("GET", "")
	if m := find(got.Models, "relay/m1"); !m.InUse || m.Hidden || m.Group != "Relay" {
		t.Fatalf("%+v", m)
	}
	if m := find(got.Models, "relay/m2"); m.InUse || m.Hidden {
		t.Fatalf("%+v", m)
	}
	// the one in use stays whatever is asked
	got = call("POST", `{"hidden":["relay/m1","relay/m2"]}`)
	if saved := settings.Load().HiddenModels["codex"]; !slices.Equal(saved, []string{"relay/m2"}) {
		t.Fatalf("saved %v", saved)
	}
	if !find(got.Models, "relay/m2").Hidden || find(got.Models, "relay/m1").Hidden ||
		got.Count == nil || got.Count.Shown != got.Count.Listed-1 {
		t.Fatalf("%+v %+v", got.Models, got.Count)
	}
	if c := modelCount("codex"); !reflect.DeepEqual(c, got.Count) {
		t.Fatalf("count %+v, answered %+v", c, got.Count)
	}
	got = call("POST", `{"hidden":[]}`)
	if got.Count.Shown != got.Count.Listed {
		t.Fatalf("%+v", got.Count)
	}
}

// Every model taken out of an agent's lists leaves its pickers no catalog
// entry, yet the line under its name stays, "Showing 0 / N", as the way to
// put them back (#356).
func TestAgentModelLineAllHidden(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"gpt-5.5\"\n"), 0o600)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	a, err := agent.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	line := func() *modelCountJSON { return agentModelCount(a.ID, agentFields(a, a.Values())) }
	c := line()
	if c == nil || c.Shown != c.Listed || c.Listed < 2 {
		t.Fatalf("all shown: %+v", c)
	}
	var all []string
	listed, _ := provider.ListedFor("codex")
	for _, e := range listed {
		all = append(all, e.ID)
	}
	if err := provider.SetHiddenModels("codex", all); err != nil {
		t.Fatal(err)
	}
	if takesCatalog(agentFields(a, a.Values())) {
		t.Fatal("a picker still lists a catalog entry; the case isn't reached")
	}
	if c := line(); c == nil || c.Shown != 0 || c.Listed != len(all) {
		t.Fatalf("all hidden: %+v", c)
	}
	provider.SetHiddenModels("codex", nil)
	if c := line(); c == nil || c.Shown != c.Listed {
		t.Fatalf("put back: %+v", c)
	}
}

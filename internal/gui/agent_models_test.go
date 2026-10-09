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
	line := func() *modelCountJSON { return agentModelCount(a, agentFields(a, a.Values())) }
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

// Cursor Private Inference has no field picking among the catalog, yet its
// own picker is the gateway's list as its key is shown it (mamba on
// Discord: its models couldn't be picked in magpie), so its row counts the
// models shown, as Claude Desktop's does, and its list picks them.
func TestCursorLocalModelLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	a, err := agent.Find(agent.CursorLocalID)
	if err != nil {
		t.Fatal(err)
	}
	if takesCatalog(agentFields(a, a.Values())) {
		t.Fatal("a field of its picks among the catalog; the case isn't reached")
	}
	line := func() *modelCountJSON { return agentModelCount(a, agentFields(a, a.Values())) }
	c := line()
	if c == nil || c.Shown != c.Listed || c.Listed < 2 {
		t.Fatalf("no line, or not all shown: %+v", c)
	}
	if err := provider.SetHiddenModels(agent.CursorLocalID, []string{"relay/m2"}); err != nil {
		t.Fatal(err)
	}
	if c := line(); c == nil || c.Shown != c.Listed-1 {
		t.Fatalf("one taken out: %+v", c)
	}
}

// An agent's list switched to "only models I pick" (nianlee-official,
// #1337), through the page's own handler: the switch is the agent's, what
// is ticked is what is saved (the model it is set to among it), a model
// that comes later is listed unticked, a list still sending what it hides
// is read as picks, and with none picked the line under its name stays.
func TestAgentModelsOnlyPicked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	setModel := func(m string) {
		os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \""+m+"\"\n"), 0o600)
	}
	setModel("magpie/relay/a1")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"a1", "a2", "a3"}}); err != nil {
		t.Fatal(err)
	}
	h := Handler(nil, nil)
	call := func(method, body string) (out struct {
		Models []agentModelJSON
		Count  *modelCountJSON
		Only   bool
	}) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/api/agent-models/codex", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, body, w.Code, w.Body)
		}
		json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	off := func(ms []agentModelJSON) (out []string) {
		for _, m := range ms {
			if m.Hidden {
				out = append(out, m.ID)
			}
		}
		return out
	}
	if got := call("GET", ""); got.Only {
		t.Fatal("a new agent is shown only its picks")
	}
	if got := call("POST", `{"hidden":["relay/a3"]}`); got.Only || !slices.Equal(off(got.Models), []string{"relay/a3"}) {
		t.Fatalf("hidden: %+v", got)
	}
	// on: the same shown, a3 still off
	if got := call("POST", `{"only":true}`); !got.Only || !slices.Equal(off(got.Models), []string{"relay/a3"}) {
		t.Fatalf("switched on: %+v", got)
	}
	if got := call("GET", ""); !got.Only {
		t.Fatal("the switch isn't kept")
	}
	// what is ticked is saved; the one in use stays whatever is asked
	got := call("POST", `{"shown":["relay/a2"]}`)
	if saved := settings.Load().PickedModels["codex"]; !slices.Equal(saved, []string{"relay/a1", "relay/a2"}) {
		t.Fatalf("saved %v", saved)
	}
	if !slices.Equal(off(got.Models), []string{"relay/a3"}) || got.Count.Shown != 2 || got.Count.Listed != 3 {
		t.Fatalf("%+v %+v", got.Models, got.Count)
	}
	// a provider added since: its model is listed, unticked
	if err := provider.Save(provider.Provider{ID: "fresh", Name: "Fresh", Key: "k", Chat: "http://127.0.0.1:2/v1", Models: []string{"x1"}}); err != nil {
		t.Fatal(err)
	}
	if got := call("GET", ""); !slices.Equal(off(got.Models), []string{"relay/a3", "fresh/x1"}) {
		t.Fatalf("a new provider's model: hidden %v", off(got.Models))
	}
	// a list opened before the switch sends what it hides: the rest it
	// lists are the picks
	if got := call("POST", `{"hidden":["relay/a2","relay/a3"]}`); !got.Only || !slices.Equal(off(got.Models), []string{"relay/a2", "relay/a3"}) {
		t.Fatalf("hidden sent in picks: %+v", off(got.Models))
	}

	// none picked and the agent set to none of them: the line stays, the
	// way back to the list
	setModel("gpt-5.5")
	call("POST", `{"shown":[]}`)
	a, err := agent.Find("codex")
	if err != nil {
		t.Fatal(err)
	}
	if takesCatalog(agentFields(a, a.Values())) {
		t.Fatal("a picker still lists a catalog entry; the case isn't reached")
	}
	if c := agentModelCount(a, agentFields(a, a.Values())); c == nil || c.Shown != 0 {
		t.Fatalf("none picked: %+v", c)
	}

	// off: what is shown stays, and a model that comes later shows
	call("POST", `{"shown":["relay/a1"]}`)
	if got := call("POST", `{"only":false}`); got.Only || !slices.Equal(off(got.Models), []string{"relay/a2", "relay/a3", "fresh/x1"}) {
		t.Fatalf("switched off: %+v", off(got.Models))
	}
	if err := provider.Save(provider.Provider{ID: "fresh", Name: "Fresh", Key: "k", Chat: "http://127.0.0.1:2/v1", Models: []string{"x1", "x2"}}); err != nil {
		t.Fatal(err)
	}
	if got := call("GET", ""); slices.Contains(off(got.Models), "fresh/x2") {
		t.Fatalf("switched off, a new model hidden: %v", off(got.Models))
	}
}

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

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The Gateway page's switches (辰林鸭~ on Discord): models turned off for
// every agent at once, the one an agent is set to passed by with its name,
// a routing group never; each agent's list shows them off and keeps the
// agent's own pick of each as it was, whatever its list sends.
func TestAgentsOffAPI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"magpie/relay/m1\"\n"), 0o600)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2", "m3", "m4"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "pool", Name: "Pool", Members: []string{"relay/m2", "relay/m3"}}); err != nil {
		t.Fatal(err)
	}
	// Codex took m3 out of its own list before
	if err := provider.SetHiddenModels("codex", []string{"relay/m3"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	agentModelsAPI(mux)
	agentsOffAPI(mux)
	var list struct{ Models []agentModelJSON }
	call := func(method, path, body string, out any) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s %s %d %s", method, path, w.Code, w.Body)
		}
		// a fresh list each time: the one decoded into keeps the fields an
		// answer leaves out (omitempty)
		list.Models = nil
		json.Unmarshal(w.Body.Bytes(), out)
	}
	var st agentsOffJSON
	call("GET", "/api/agents-off", "", &st)
	if len(st.Off) != 0 || st.Keep["relay/m1"] != "Codex" {
		t.Fatalf("before: %+v", st)
	}
	call("POST", "/api/agents-off", `{"ids":["relay/m1","relay/m2","relay/m3","group/pool"],"off":true}`, &st)
	if !slices.Equal(st.Off, []string{"relay/m2", "relay/m3"}) || st.Keep["relay/m1"] != "Codex" {
		t.Fatalf("turned off: %+v", st)
	}
	if got := settings.Load().HiddenModels[provider.EveryAgent]; !slices.Equal(got, []string{"relay/m2", "relay/m3"}) {
		t.Fatalf("saved %v", got)
	}
	find := func(id string) agentModelJSON {
		t.Helper()
		for _, m := range list.Models {
			if m.ID == id {
				return m
			}
		}
		t.Fatalf("no %s in %+v", id, list.Models)
		return agentModelJSON{}
	}
	call("GET", "/api/agent-models/codex", "", &list)
	for id, off := range map[string]bool{"relay/m1": false, "relay/m2": true, "relay/m3": true, "relay/m4": false} {
		if m := find(id); m.Off != off || m.Hidden != off {
			t.Errorf("%s: %+v", id, m)
		}
	}
	// the picker sends what it shows hidden, the ones off among them: the
	// agent's own pick of each is what it was, m3 out and m2 in
	call("POST", "/api/agent-models/codex", `{"hidden":["relay/m2","relay/m3","relay/m4"]}`, &list)
	if got := settings.Load().HiddenModels["codex"]; !slices.Equal(got, []string{"relay/m3", "relay/m4"}) {
		t.Fatalf("codex's own %v", got)
	}
	call("POST", "/api/agent-models/codex", `{"hidden":[]}`, &list)
	if got := settings.Load().HiddenModels["codex"]; !slices.Equal(got, []string{"relay/m3"}) {
		t.Fatalf("codex's own after all shown %v", got)
	}
	if !find("relay/m2").Hidden || find("relay/m4").Hidden {
		t.Fatalf("%+v", list.Models)
	}
	// back on: Codex's own pick of m3 holds, m2 is listed again
	call("POST", "/api/agents-off", `{"ids":["relay/m2","relay/m3"],"off":false}`, &st)
	call("GET", "/api/agent-models/codex", "", &list)
	if len(st.Off) != 0 || find("relay/m2").Hidden || !find("relay/m3").Hidden {
		t.Fatalf("back on: %+v %+v", st, list.Models)
	}
}

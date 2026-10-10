package gui

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// ZCode's row picks which of magpie's models its picker lists, as every
// other agent's does (#1508, NovaTrailX: settings.hiddenModels had no
// zcode, and both its files got every model). Through the page's own
// handlers: its row has the line and the menu, the list takes one out, the
// row counts one fewer, and the files magpie writes for it leave it out.
func TestZCodeModelList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".zcode", "v2")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"provider":{"mine":{"name":"Mine","kind":"openai"}}}`), 0o644)
	h := Handler(nil, nil)
	call := func(path, body string, out any) {
		t.Helper()
		method := "POST"
		if body == "" {
			method = "GET"
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("%s %d %s", path, rec.Code, rec.Body)
		}
		json.Unmarshal(rec.Body.Bytes(), out)
	}
	row := func() agentJSON {
		var s stateJSON
		call("/api/state", "", &s)
		for _, a := range s.Agents {
			if a.ID == "zcode" {
				return a
			}
		}
		t.Fatalf("no zcode in %+v", s.Agents)
		return agentJSON{}
	}
	var s stateJSON
	call("/api/agents/connect/zcode", "{}", &s)
	r := row()
	if !r.Wired || !r.Menu || r.Models == nil || r.Models.Shown != 2 || r.Models.Listed != 2 {
		t.Fatalf("connected, its row: wired %v, menu %v, models %+v", r.Wired, r.Menu, r.Models)
	}
	var list struct {
		Models []agentModelJSON
		Count  *modelCountJSON
	}
	call("/api/agent-models/zcode", "", &list)
	if len(list.Models) != 2 {
		t.Fatalf("its list: %+v", list.Models)
	}
	call("/api/agent-models/zcode", `{"hidden":["relay/m2"]}`, &list)
	if list.Count == nil || list.Count.Shown != 1 {
		t.Fatalf("one taken out: %+v", list.Count)
	}
	if got := provider.HiddenModels("zcode"); !got["relay/m2"] || len(got) != 1 {
		t.Fatalf("settings.hiddenModels.zcode: %v", got)
	}
	if r := row(); r.Models == nil || r.Models.Shown != 1 || r.Models.Listed != 2 {
		t.Fatalf("its row after: %+v", r.Models)
	}
	// what a change to the catalog runs (catalog.Changed)
	agent.SyncCatalog()
	var c struct {
		Provider map[string]struct {
			Models map[string]any `json:"models"`
		} `json:"provider"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	json.Unmarshal(b, &c)
	if _, ok := c.Provider["mine"]; !ok {
		t.Fatalf("the user's own provider went: %s", b)
	}
	var ids []string
	for id := range c.Provider["magpie"].Models {
		ids = append(ids, id)
	}
	if !slices.Equal(ids, []string{"relay/m1"}) {
		t.Fatalf("config.json lists %v", ids)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "provider_config.json"))
	if !strings.Contains(string(b), `"personalModelIds":["relay/m1"]`) {
		t.Fatalf("provider_config.json: %s", b)
	}
}

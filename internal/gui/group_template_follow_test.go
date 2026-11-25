package gui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A group added from a template follows it (AlanWang1027713 on X: "让 AI
// 配，不用人工配"). The Routing page's editor saves a group back with
// everything it read, in its own shape: empty lists, a null time, the
// order it placed, no template. Saved unchanged, renamed, the group still
// follows; with a rule pointed elsewhere, the picks are the user's.
func TestEditorSaveKeepsTemplateFollowing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	b, err := os.ReadFile(filepath.Join("..", "provider", "testdata", "models-dev-deepseek-2026-11-10.json"))
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	if err := os.WriteFile(catalog.CachePath(), b, 0o644); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Key: "sk-test", Catalog: "deepseek",
		Chat: "https://api.deepseek.com/v1", Anthropic: "https://api.deepseek.com/anthropic",
		Models: []string{"deepseek-v4-pro", "deepseek-v4-flash"}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	groupRoutes(mux)
	do := func(method, path string, body any) []byte {
		t.Helper()
		var in bytes.Buffer
		if body != nil {
			json.NewEncoder(&in).Encode(body)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, &in))
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	group := func(id string) map[string]any {
		t.Helper()
		var st struct {
			Groups []map[string]any `json:"groups"`
		}
		if err := json.Unmarshal(do("GET", "/api/groups", nil), &st); err != nil {
			t.Fatal(err)
		}
		for _, g := range st.Groups {
			if g["id"] == id {
				return g
			}
		}
		t.Fatalf("no group %s", id)
		return nil
	}
	do("POST", "/api/groups/template", map[string]any{"kind": "smart", "name": "Smart"})
	g := group("smart-split")
	if g["template"] != "smart" {
		t.Fatalf("added: template %v, want smart", g["template"])
	}
	list := func(k string) []any {
		if v, ok := g[k].([]any); ok {
			return v
		}
		return []any{}
	}
	str := func(k string) string { s, _ := g[k].(string); return s }
	// the editor's draft of a group as read (client.js openGroup), each
	// rule spread with its lists made, and its save (saveBtn)
	save := func(name string, rules []any) {
		do("POST", "/api/groups/save", map[string]any{
			"id": "smart-split", "from": "smart-split", "name": name, "note": "", "members": list("members"), "placed": list("members"),
			"match": list("match"), "routing": str("routing"), "pick": "", "affinity": "", "sink": false, "noSink": true,
			"firstToken": 0, "patience": 0, "rules": rules, "effort": "", "classifier": str("classifier"), "classifierMore": list("classifierMore"),
			"intentSure": nil, "context": 0, "compact": 0, "levels": []any{}, "family": "", "fast": []any{}, "ultra": []any{}, "off": []any{},
			"weights": map[string]any{}, "effortMaps": map[string]any{}, "accounts": []any{}, "keys": list("keys"), "redact": "follow", "unlists": false,
		})
	}
	drafted := func() []any {
		var rs []any
		for _, r := range list("rules") {
			m := map[string]any{}
			for k, v := range r.(map[string]any) {
				m[k] = v
			}
			if _, ok := m["intent"]; !ok {
				m["intent"] = ""
			}
			m["agents"], m["folders"], m["exceptFolders"], m["where"], m["time"] = []any{}, []any{}, []any{}, []any{}, nil
			rs = append(rs, m)
		}
		return rs
	}
	save("Smart work", drafted())
	if g = group("smart-split"); g["template"] != "smart" || g["name"] != "Smart work" {
		t.Fatalf("saved unchanged, renamed: template %v name %v, want it following", g["template"], g["name"])
	}
	rules := drafted()
	for _, r := range rules {
		if m := r.(map[string]any); m["intent"] != "" && m["intent"] != nil {
			m["use"] = "deepseek/deepseek-v4-pro"
		}
	}
	save("Smart work", rules)
	g = group("smart-split")
	if g["template"] != nil {
		t.Errorf("a rule pointed elsewhere: template %v, want the picks the user's", g["template"])
	}
	for _, r := range list("rules") {
		if m := r.(map[string]any); m["intent"] != nil && m["use"] != "deepseek/deepseek-v4-pro" {
			t.Errorf("rule %v: want the user's pick kept", m)
		}
	}
	if !slices.Contains(list("members"), any("deepseek/deepseek-v4-flash")) {
		t.Errorf("members %v", list("members"))
	}
}

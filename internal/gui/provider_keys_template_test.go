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

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A group following a template (Group.Template) has its members picked
// again from the user's models as they change, with no save by the user:
// one with no model of a provider's today may have one tomorrow. A key
// unticked from that provider never keeps it, and a key held to it is
// shown reaching the provider through it.
func TestProviderKeysUntickLeavesATemplateGroup(t *testing.T) {
	sandboxHome(t)
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
	for _, p := range []provider.Provider{
		{ID: "deepseek", Name: "DeepSeek", Key: "sk-test", Catalog: "deepseek",
			Chat: "https://api.deepseek.com/v1", Anthropic: "https://api.deepseek.com/anthropic",
			Models: []string{"deepseek-v4-pro", "deepseek-v4-flash"}},
		{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	groupRoutes(mux)
	callerKeyRoutes(mux)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	if w := request("POST", "/api/groups/template", `{"kind":"smart","name":"Smart"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	i := slices.IndexFunc(provider.Groups(), func(g provider.Group) bool { return g.ID == "smart-split" })
	if i < 0 {
		t.Fatal("no group smart-split")
	}
	g := provider.Groups()[i]
	if g.Template != "smart" || len(g.Members) == 0 || slices.ContainsFunc(g.Members, func(m string) bool { return strings.HasPrefix(m, "relay/") }) {
		t.Fatalf("the template group, before: template %q members %v; want it following, with no member of relay's", g.Template, g.Members)
	}

	var s struct{ Keys []access.Key }
	if w := request("POST", "/api/caller-keys/add-key", `{"name":"Phone"}`); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &s) != nil {
		t.Fatal(w.Code, w.Body)
	}
	id := s.Keys[0].ID
	if w := request("POST", "/api/provider-keys", `{"provider":"relay","remove":["`+id+`"]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	keys, err := access.List()
	if err != nil {
		t.Fatal(err)
	}
	ms := keys[0].Models
	if !slices.Contains(ms, "deepseek/*") || slices.ContainsFunc(ms, func(m string) bool {
		return m == "group/*" || m == "group/smart-split" || strings.HasPrefix(m, "relay/")
	}) {
		t.Fatalf("unticked key's models: %v; want deepseek/* and not the group following a template", ms)
	}

	if w := request("POST", "/api/caller-keys/models-key", `{"key":"`+id+`","models":["deepseek/*","group/smart-split"]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var r struct{ Keys []access.ProviderKey }
	if w := request("GET", "/api/provider-keys?provider=relay", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &r) != nil {
		t.Fatal(w.Code, w.Body)
	}
	if len(r.Keys) != 1 || !slices.Equal(r.Keys[0].Via, []string{"group/smart-split"}) {
		t.Fatalf("a key held to a group following a template, on relay's side: %+v", r.Keys)
	}
}

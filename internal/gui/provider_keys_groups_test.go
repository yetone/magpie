package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A key unticked in a provider's editor keeps only the routing groups
// that can't reach the provider: a key that names a group is sent to
// every member of it, so the "group/*" the untick wrote took the key to
// the provider through any group with a member of its. A group reaches
// it by a member it names, a group in it, or a pattern; and the editor
// shows a key's group entry that reaches the provider as such.
func TestProviderKeysUntickKeepsGroupsOff(t *testing.T) {
	sandboxHome(t)
	for _, p := range []provider.Provider{
		{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}},
		{ID: "other", Name: "Other", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"n"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []provider.Group{
		{Name: "Mine", Members: []string{"other/n", "relay/m"}, Routing: provider.Ordered},
		{Name: "Theirs", Members: []string{"other/n"}, Routing: provider.Ordered},
		{Name: "Nested", Members: []string{"group/mine"}, Routing: provider.Ordered},
		{Name: "Pat", Match: []string{"re:^m$"}, Routing: provider.Ordered},
	} {
		if err := provider.SaveGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	callerKeyRoutes(mux)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
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
	if !slices.Contains(ms, "other/*") || !slices.Contains(ms, "group/theirs") || slices.ContainsFunc(ms, func(m string) bool {
		return m == "group/*" || m == "group/mine" || m == "group/nested" || m == "group/pat" || strings.HasPrefix(m, "relay/")
	}) {
		t.Fatalf("unticked key's models: %v", ms)
	}
	// a key held to another provider and a group with a member of relay's
	// reaches relay through it
	if w := request("POST", "/api/caller-keys/models-key", `{"key":"`+id+`","models":["other/*","group/nested"]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var r struct{ Keys []access.ProviderKey }
	if w := request("GET", "/api/provider-keys?provider=relay", ""); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &r) != nil {
		t.Fatal(w.Code, w.Body)
	}
	if len(r.Keys) != 1 || !slices.Equal(r.Keys[0].Via, []string{"group/nested"}) {
		t.Fatalf("a key reaching relay through a group: %+v", r.Keys)
	}
}

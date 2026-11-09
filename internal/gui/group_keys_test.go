package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// The Routing page binds a group to gateway keys (Group.Keys, Xerxes754
// on X): /api/groups lists the keys it may pick, the user's own keys but
// not one issued under another (it follows its issuer) nor a read-only
// one (it asks for no model), and the editor's save stores the picks.
// A save that names no keys, as a card's toggle does, keeps them.
func TestGroupKeysListedAndSaved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := provider.Save(provider.Provider{ID: "a", Name: "a", Key: "ka", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{ID: "team", Name: "Team", Members: []string{"a/m1"}}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range []access.Change{{Name: "Laptop"}, {Name: "Read only", ReadOnly: true}} {
		if _, err := access.Update("add-key", c); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := access.List()
	if err != nil || len(keys) != 2 {
		t.Fatal(keys, err)
	}
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	if _, err := access.Update("issuer-key", access.Change{Key: ids[0], Issues: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := access.IssueKey(access.Issue{Name: "Issued", TTL: time.Hour, Issuer: ids[0]}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	groupRoutes(mux)
	call := func(method, path, body string) groupsJSON {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		var out groupsJSON
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return out
	}
	got := call("GET", "/api/groups", "")
	if len(got.GatewayKeys) != 1 || got.GatewayKeys[0].ID != ids[0] || got.GatewayKeys[0].Name != "Laptop" {
		t.Fatalf("gatewayKeys %+v", got.GatewayKeys)
	}
	stored := func() []string {
		g, _, _ := provider.FindGroup("group/team")
		return g.Keys
	}
	call("POST", "/api/groups/save", `{"id":"team","from":"team","name":"Team","members":["a/m1"],"keys":["`+ids[0]+`"]}`)
	if k := stored(); len(k) != 1 || k[0] != ids[0] {
		t.Fatalf("saved keys %v", k)
	}
	call("POST", "/api/groups/save", `{"id":"team","from":"team","name":"Team","members":["a/m1"]}`)
	if k := stored(); len(k) != 1 {
		t.Fatalf("a save naming no keys left %v", k)
	}
	call("POST", "/api/groups/save", `{"id":"team","from":"team","name":"Team","members":["a/m1"],"keys":[]}`)
	if k := stored(); len(k) != 0 {
		t.Fatalf("cleared keys left %v", k)
	}
}

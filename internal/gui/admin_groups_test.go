package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// adminGroupsHome is a sandbox with two providers, the first with one
// key a member can be held to, for the routing groups' tests (#6307).
func adminGroupsHome(t *testing.T) (account string) {
	t.Helper()
	sandboxHome(t)
	for _, p := range []provider.Provider{
		{ID: "a", Name: "A", Key: "sk-a-canary-0123456789", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}},
		{ID: "b", Name: "B", Key: "sk-b-0123456789", Chat: "http://127.0.0.1:1/v1", Models: []string{"m3"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	return provider.KeyID("sk-a-canary-0123456789")
}

func storedGroup(id string) provider.Group {
	g, _, _ := provider.FindGroup(provider.GroupPrefix + id)
	return g
}

func adminGroupsOf(t *testing.T, w *httptest.ResponseRecorder) groupsJSON {
	t.Helper()
	var out groupsJSON
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	return out
}

// #6307 (ljayic on X): the Routing page's groups, their combos and each
// member's account are managed through the API, with the page's own
// checks: what it refuses, the API refuses.
func TestAdminAPIRoutingGroups(t *testing.T) {
	acct := adminGroupsHome(t)
	// create: a group of a model on one account, then a combo holding it
	w := adminDo(t, "POST", "/routing-groups/create", `{"id":"team","name":"Team","members":["a/m1#`+acct+`","b/m3"]}`)
	got := adminGroupsOf(t, w)
	if len(got.Groups) != 1 || got.Groups[0].ID != "team" {
		t.Fatalf("create: %+v", got.Groups)
	}
	if strings.Contains(w.Body.String(), "sk-a-canary") {
		t.Fatal("the answer tells a provider's key")
	}
	adminGroupsOf(t, adminDo(t, "POST", "/routing-groups/create", `{"name":"Combo","members":["group/team","a/m2"],"routing":"rotate"}`))
	// create refuses an id taken, where save writes over it
	if w := adminDo(t, "POST", "/routing-groups/create", `{"id":"team","members":["a/m2"]}`); w.Code != http.StatusConflict {
		t.Errorf("create over team: %d %s, want 409", w.Code, w.Body)
	}
	// the page's checks: an account the provider hasn't, a group given
	// an account, a group with nothing in it, an id that isn't one
	for _, body := range []string{
		`{"id":"x","members":["a/m1#nobody"]}`,
		`{"id":"x","members":["group/team#` + acct + `"]}`,
		`{"id":"x","members":[]}`,
		`{"id":"Not An Id!","members":["a/m1"]}`,
	} {
		if w := adminDo(t, "POST", "/routing-groups/create", body); w.Code != http.StatusBadRequest {
			t.Errorf("create %s: %d %s, want 400", body, w.Code, w.Body)
		}
	}
	// get, by its id and by the ref agents pick it by
	for _, id := range []string{"team", "group/team"} {
		w := adminDo(t, "GET", "/routing-groups/"+id, "")
		var g groupJSON
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &g) != nil || g.ID != "team" || len(g.Members) != 2 || g.Members[0] != "a/m1#"+acct {
			t.Fatalf("get %s: %d %s", id, w.Code, w.Body)
		}
	}
	if w := adminDo(t, "GET", "/routing-groups/nope", ""); w.Code != 404 {
		t.Errorf("get nope: %d, want 404", w.Code)
	}
	// update changes the fields it sends and keeps the rest
	adminGroupsOf(t, adminDo(t, "POST", "/routing-groups/update", `{"id":"team","name":"Team two"}`))
	if g := storedGroup("team"); g.Name != "Team two" || len(g.Members) != 2 || g.Members[0] != "a/m1#"+acct {
		t.Fatalf("update kept %+v", g)
	}
	adminGroupsOf(t, adminDo(t, "POST", "/routing-groups/update", `{"id":"team","members":["a/m2","b/m3"]}`))
	if g := storedGroup("team"); len(g.Members) != 2 || g.Members[0] != "a/m2" || g.Name != "Team two" {
		t.Fatalf("update members: %+v", g)
	}
	if w := adminDo(t, "POST", "/routing-groups/update", `{"id":"nope","name":"x"}`); w.Code != 404 {
		t.Errorf("update nope: %d, want 404", w.Code)
	}
	if w := adminDo(t, "POST", "/routing-groups/update", `{"id":"team","members":["a/m1#nobody"]}`); w.Code != 400 {
		t.Errorf("update to an account the provider hasn't: %d, want 400", w.Code)
	}
	// a rename: the combo names it by its new id
	if w := adminDo(t, "POST", "/routing-groups/update", `{"from":"team","id":"crew"}`); w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body)
	}
	if c := storedGroup("combo"); len(c.Members) == 0 || c.Members[0] != "group/crew" {
		t.Fatalf("the combo after the rename: %+v", c.Members)
	}
	// list, and the page's other actions
	if got := adminGroupsOf(t, adminDo(t, "GET", "/routing-groups", "")); len(got.Groups) != 2 {
		t.Fatalf("list: %+v", got.Groups)
	}
	adminGroupsOf(t, adminDo(t, "POST", "/routing-groups/switch", `{"id":"combo","on":false}`))
	adminGroupsOf(t, adminDo(t, "POST", "/routing-groups/arrange", `{"order":["combo","crew"]}`))
	// what isn't served: the found groups' switches, and anything unknown
	for _, a := range []string{"found", "skip", "nope"} {
		if w := adminDo(t, "POST", "/routing-groups/"+a, `{"on":false}`); w.Code != 404 {
			t.Errorf("%s: %d, want 404", a, w.Code)
		}
	}
	// delete: refused while the combo holds it, as the page refuses
	if w := adminDo(t, "POST", "/routing-groups/delete", `{"id":"crew"}`); w.Code != 400 {
		t.Errorf("delete a group in a combo: %d, want 400", w.Code)
	}
	adminGroupsOf(t, adminDo(t, "POST", "/routing-groups/delete", `{"ids":["combo","crew"]}`))
	if got := adminGroupsOf(t, adminDo(t, "GET", "/routing-groups", "")); len(got.Groups) != 0 {
		t.Fatalf("delete left %+v", got.Groups)
	}
}

// The routing groups are behind the management API's guard like every
// route under it, as the gateway's own listener serves them: no key,
// a key that may not manage magpie, a key allowed from this computer
// only from another, and a browser, are refused; and nothing is changed.
func TestAdminAPIRoutingGroupsGuarded(t *testing.T) {
	adminGroupsHome(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	s := settings.Load()
	s.LAN = true
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	secret := map[string]string{}
	for _, name := range []string{"plain", "local", "any"} {
		k, err := access.Update("add-key", access.Change{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		secret[name] = k
	}
	keys, _ := access.List()
	for _, k := range keys {
		if k.Name == "local" || k.Name == "any" {
			if _, err := access.Update("admin-key", access.Change{Key: k.ID, Admin: k.Name}); err != nil {
				t.Fatal(err)
			}
		}
	}
	h := gateway.New().Front()
	const loop, lan = "127.0.0.1:50000", "192.0.2.7:50000"
	call := func(method, path, from, key, body string, hdr map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, gateway.AdminPath+path, strings.NewReader(body))
		r.RemoteAddr, r.Host = from, "127.0.0.1:3425"
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	const create = `{"id":"team","members":["a/m1"]}`
	for _, c := range []struct {
		name, from, key string
		hdr             map[string]string
		want            int
	}{
		{"no key", loop, "", nil, 401},
		{"no key from the network", lan, "", nil, 401},
		{"a key that may not manage", loop, secret["plain"], nil, 403},
		{"a key that may not manage, from the network", lan, secret["plain"], nil, 403},
		{"a local key from the network", lan, secret["local"], nil, 403},
		{"a local key through a proxy", loop, secret["local"], map[string]string{"X-Forwarded-For": "203.0.113.9"}, 403},
		{"a browser with the key", loop, secret["local"], map[string]string{"Origin": "http://127.0.0.1:3425"}, 403},
	} {
		for _, rq := range []struct{ method, path, body string }{
			{"GET", "routing-groups", ""},
			{"GET", "routing-groups/team", ""},
			{"POST", "routing-groups/create", create},
			{"POST", "routing-groups/update", create},
			{"POST", "routing-groups/delete", `{"id":"team"}`},
		} {
			if w := call(rq.method, rq.path, c.from, c.key, rq.body, c.hdr); w.Code != c.want {
				t.Errorf("%s: %s %s: %d %s, want %d", c.name, rq.method, rq.path, w.Code, w.Body, c.want)
			}
		}
	}
	if len(provider.Groups()) != 0 {
		t.Fatal("a refused call made a group")
	}
	if w := call("POST", "routing-groups/create", loop, secret["local"], create, nil); w.Code != 200 {
		t.Fatalf("a local key from this computer: %d %s", w.Code, w.Body)
	}
	if w := call("GET", "routing-groups/team", lan, secret["any"], "", nil); w.Code != 200 {
		t.Fatalf("an any key from the network: %d %s", w.Code, w.Body)
	}
	if w := call("POST", "routing-groups/delete", lan, secret["any"], `{"id":"team"}`, nil); w.Code != 200 {
		t.Fatalf("an any key from the network deletes: %d %s", w.Code, w.Body)
	}
}

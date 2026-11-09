package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The Routing view's save with another id than the group had (from)
// renames it: a found group drops its auto- prefix.
func TestGroupSaveRenames(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k" + id, Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-6-astra"}}); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	groupRoutes(mux)
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/groups/save", strings.NewReader(body)))
		return w
	}
	const members = `"members":["a/gpt-6-astra","b/gpt-6-astra"]`
	if w := post(`{"id":"Bad Id!","from":"auto-gpt-6-astra",` + members + `}`); w.Code < 400 {
		t.Fatalf("a bad id taken: %d", w.Code)
	}
	if w := post(`{"id":"gpt-6-astra","from":"auto-gpt-6-astra","name":"Astra","routing":"order",` + members + `}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	g, _, ok := provider.FindGroup("group/gpt-6-astra")
	if !ok || g.Name != "Astra" || g.Routing != provider.Ordered {
		t.Fatalf("%v %+v", ok, g)
	}
	// the old id answers with the renamed group (00af08e7b), never a found
	// auto- group come back
	if g, _, ok := provider.FindGroup("group/auto-gpt-6-astra"); ok && g.ID != "gpt-6-astra" {
		t.Fatalf("auto-gpt-6-astra is back: %+v", g)
	}
	// a save without a new id stays a save
	if w := post(`{"id":"gpt-6-astra","from":"gpt-6-astra","name":"Astra 2",` + members + `}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if g, _, _ := provider.FindGroup("group/gpt-6-astra"); g.Name != "Astra 2" {
		t.Fatalf("%+v", g)
	}
}

// The Routing view's switch turns found groups off and on (蓝猫 on
// Discord): the state says which, and off lists none of them.
func TestGroupsFoundSwitch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k" + id, Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	groupRoutes(mux)
	found := func(body string) groupsJSON {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/groups/found", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
		var st groupsJSON
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := groupsState(); !st.Found || len(st.Groups) != 1 {
		t.Fatalf("on: %v %+v", st.Found, st.Groups)
	}
	if st := found(`{"on":false}`); st.Found || len(st.Groups) != 0 || provider.AutoGroupsOn() {
		t.Fatalf("off: %v %+v", st.Found, st.Groups)
	}
	if st := found(`{"on":true}`); !st.Found || len(st.Groups) != 1 || st.Groups[0].ID != "auto-m" {
		t.Fatalf("on again: %v %+v", st.Found, st.Groups)
	}
}

// groups/skip keeps providers out of the groups magpie finds
// (chenxiang0010 on Discord): the state names them, and lists every
// provider that can be, one kept out that is gone too.
func TestGroupsSkip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, id := range []string{"a", "b", "c"} {
		if err := provider.Save(provider.Provider{ID: id, Name: "P " + id, Key: "k" + id, Chat: "http://127.0.0.1:1/v1", Models: []string{"m"}}); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	groupRoutes(mux)
	skip := func(body string) groupsJSON {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/groups/skip", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
		var st groupsJSON
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := groupsState(); len(st.Skip) != 0 || len(st.Skippable) != 3 || len(st.Groups) != 1 || len(st.Groups[0].Members) != 3 {
		t.Fatalf("before: %+v %+v %+v", st.Skip, st.Skippable, st.Groups)
	}
	st := skip(`{"skip":["b","gone"]}`)
	if !slices.Equal(st.Skip, []string{"b", "gone"}) || len(st.Groups) != 1 || !slices.Equal(st.Groups[0].Members, []string{"a/m", "c/m"}) {
		t.Fatalf("b kept out: %+v %+v", st.Skip, st.Groups)
	}
	if i := slices.IndexFunc(st.Skippable, func(k skippableJSON) bool { return k.ID == "gone" }); i < 0 || st.Skippable[i].Name != "gone" {
		t.Fatalf("a provider kept out and gone can't be let back in: %+v", st.Skippable)
	}
	if st := skip(`{"skip":[]}`); len(st.Skip) != 0 || len(st.Groups[0].Members) != 3 {
		t.Fatalf("let back in: %+v %+v", st.Skip, st.Groups)
	}
}

// groups/delete with ids removes them all at once (lc on Discord), or none
// when it can't remove one.
func TestGroupsDeleteSeveral(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k" + id, Chat: "http://127.0.0.1:1/v1", Models: []string{"m", "n"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "Mine", Members: []string{"a/m", "b/n"}}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	groupRoutes(mux)
	del := func(body string) (int, groupsJSON) {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/groups/delete", strings.NewReader(body)))
		var st groupsJSON
		json.Unmarshal(w.Body.Bytes(), &st)
		return w.Code, st
	}
	shown := func(st groupsJSON) (out []string) {
		for _, g := range st.Groups {
			if !g.Hidden {
				out = append(out, g.ID)
			}
		}
		return out
	}
	if code, _ := del(`{"ids":["auto-m","gone"]}`); code == 200 {
		t.Fatal("a group not there was removed")
	}
	if got := shown(groupsState()); len(got) != 3 {
		t.Fatalf("a refused removal removed some: %v", got)
	}
	code, st := del(`{"ids":["auto-m","mine"]}`)
	if code != 200 {
		t.Fatalf("%d", code)
	}
	if got := shown(st); len(got) != 1 || got[0] != "auto-n" {
		t.Fatalf("left: %v", got)
	}
	// one by its id alone, as the editor's Remove does
	if code, st := del(`{"id":"auto-n"}`); code != 200 || len(shown(st)) != 0 {
		t.Fatalf("%d %v", code, shown(st))
	}
}

// The tray panel's pick (#2489) switches a Manual group's member and
// answers with the groups' state, its picked member the new one; a model
// not in the group is refused.
func TestGroupsPick(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k" + id, Chat: "http://127.0.0.1:1/v1", Models: []string{"gpt-6-astra"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{ID: "hand", Name: "Hand", Members: []string{"a/gpt-6-astra", "b/gpt-6-astra"}, Routing: provider.Manual}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	groupRoutes(mux)
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/groups/pick", strings.NewReader(body)))
		return w
	}
	w := post(`{"id":"hand","pick":"b/gpt-6-astra"}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var st struct {
		Groups []struct{ ID, Picked string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	picked := ""
	for _, g := range st.Groups {
		if g.ID == "hand" {
			picked = g.Picked
		}
	}
	if picked != "b/gpt-6-astra" {
		t.Fatalf("picked %q in %s", picked, w.Body)
	}
	if w := post(`{"id":"hand","pick":"c/gpt-6-astra"}`); w.Code < 400 {
		t.Fatalf("a model not in the group taken: %d", w.Code)
	}
	if g, _, _ := provider.FindGroup("group/hand"); g.Pick != "b/gpt-6-astra" {
		t.Fatalf("%+v", g)
	}
}

package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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
	if _, _, ok := provider.FindGroup("group/auto-gpt-6-astra"); ok {
		t.Fatal("auto-gpt-6-astra is back")
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

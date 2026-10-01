package gui

import (
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

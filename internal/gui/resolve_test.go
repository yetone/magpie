package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// quantum_lsfc on X: the editor's connect addresses are saved on a
// provider of the user's, normalised as netproxy.CheckResolve keeps them,
// open as saved, are kept by a Save that leaves them out, cleared by an
// empty one, and refused when they can't be used.
func TestProviderSaveResolve(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		return w
	}
	saved := func() map[string][]string {
		t.Helper()
		p, err := provider.Find("gw")
		if err != nil {
			t.Fatal(err)
		}
		info := providerInfo(*p, nil)
		if !info.TakesResolve || !reflect.DeepEqual(info.Resolve, p.Resolve) {
			t.Fatalf("the editor is shown %v (takes %v), saved %v", info.Resolve, info.TakesResolve, p.Resolve)
		}
		return p.Resolve
	}
	base := `"id":"gw","name":"Gateway","key":"sk-g","anthropic":"https://llm.corp.example/v1","icon":"generic"`
	if w := post(`{` + base + `,"new":true,"resolve":{" LLM.corp.example ":["dns:gw.managed.test","10.0.0.1"]}}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	want := map[string][]string{"llm.corp.example": {"dns:gw.managed.test", "10.0.0.1"}}
	if got := saved(); !reflect.DeepEqual(got, want) {
		t.Fatalf("saved %v, want %v", got, want)
	}
	keep := `{"id":"gw","from":"gw","name":"Gateway","anthropic":"https://llm.corp.example/v1","icon":"generic","models":[],"headers":{}`
	if w := post(keep + `}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if got := saved(); !reflect.DeepEqual(got, want) {
		t.Fatalf("a Save that left them out changed them: %v", got)
	}
	if w := post(keep + `,"resolve":{"llm.corp.example":["gateway.corp"]}}`); w.Code == 200 || !strings.Contains(w.Body.String(), "resolve: ") {
		t.Fatalf("a place that is a bare name was kept: %d %s", w.Code, w.Body.String())
	}
	if got := saved(); !reflect.DeepEqual(got, want) {
		t.Fatalf("a refused Save changed them: %v", got)
	}
	if w := post(keep + `,"resolve":{}}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if got := saved(); len(got) != 0 {
		t.Fatalf("emptied, still %v", got)
	}
}

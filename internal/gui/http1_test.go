package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// kepner_53726: the editor's HTTP/1.1 only tick is saved on a provider of
// the user's, opens as saved, and is kept by a Save that leaves it out.
func TestProviderSaveHTTP1(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	post := func(body string) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	h1 := func() bool {
		t.Helper()
		p, err := provider.Find("gw")
		if err != nil {
			t.Fatal(err)
		}
		if info := providerInfo(*p, nil); info.HTTP1 != p.HTTP1 {
			t.Fatalf("the editor is shown %v, saved %v", info.HTTP1, p.HTTP1)
		}
		return p.HTTP1
	}
	post(`{"id":"gw","name":"Gateway","key":"sk-g","anthropic":"https://llm.corp.example/v1","icon":"generic","new":true,"http1":true}`)
	if !h1() {
		t.Fatal("added with HTTP/1.1, saved without")
	}
	post(`{"id":"gw","from":"gw","name":"Gateway","anthropic":"https://llm.corp.example/v1","icon":"generic","models":[],"headers":{}}`)
	if !h1() {
		t.Fatal("a Save that left it out dropped it")
	}
	post(`{"id":"gw","from":"gw","name":"Gateway","anthropic":"https://llm.corp.example/v1","icon":"generic","models":[],"headers":{},"http1":false}`)
	if h1() {
		t.Fatal("unticked, still HTTP/1.1")
	}
}

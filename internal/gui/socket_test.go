package gui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #5781: a provider's Socket, set with `magpie provider set <id>
// socket=on`, opens in the editor as set, a Save that leaves it out keeps
// it, and the editor's tick saves it either way.
func TestProviderSaveSocket(t *testing.T) {
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
	info := func(id string) providerJSON {
		t.Helper()
		p, err := provider.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		in := providerInfo(*p, nil)
		if in.Socket != p.Socket {
			t.Fatalf("%s: the editor is shown %q, saved %q", id, in.Socket, p.Socket)
		}
		return in
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "sk-r", Responses: "https://relay.example/v1", Socket: provider.SocketOn}); err != nil {
		t.Fatal(err)
	}
	if got := info("relay"); !got.TakesSocket || got.Socket != "on" {
		t.Fatalf("set on from the CLI: takes %v, shown %q", got.TakesSocket, got.Socket)
	}
	edit := `{"id":"relay","from":"relay","name":"Relay","key":"","responses":"https://relay.example/v1","models":[],"headers":{}`
	post(edit + `}`)
	if info("relay").Socket != "on" {
		t.Fatal("a Save that left it out dropped the CLI's on")
	}
	post(edit + `,"socket":""}`)
	if info("relay").Socket != "" {
		t.Fatal("unticked in the editor, still saved")
	}
	post(edit + `,"socket":"auto"}`)
	if info("relay").Socket != "auto" {
		t.Fatal("ticked in the editor, not saved auto")
	}
	post(`{"id":"chat","name":"Chat","key":"sk-c","chat":"https://chat.example/v1","icon":"generic","new":true}`)
	if info("chat").TakesSocket {
		t.Error("a provider without a Responses URL is offered the socket")
	}
}

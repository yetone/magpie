package gui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/provider"
)

// starballl on X: a Remote magpie added from the editor with a key that
// may use all of the magpie it reaches, the user's own hub, has its MCP
// servers and its skills and prompts on, and its Library read, without a
// tick: the editor sends neither switch until it is changed. A switch the
// user unticked stays off; a key held to a budget leaves both off; a save
// of a provider already there changes neither.
func TestRemoteHubAddedWithFullKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			io.WriteString(w, `{"object":"list","data":[{"id":"plan/m1","object":"model"}]}`)
		case provider.RemoteKeyLimitPath:
			if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer full") {
				io.WriteString(w, `{"limited":false,"key":"Laptop","full":true}`)
				return
			}
			io.WriteString(w, `{"limited":true,"key":"Friend","heldTo":["budget"]}`)
		case "/v1":
			io.WriteString(w, `{"name":"magpie"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(hub.Close)
	read := make(chan string, 8)
	old := remoteLibraryRead
	remoteLibraryRead = func(id string) { read <- id }
	t.Cleanup(func() { remoteLibraryRead = old })

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
	at := strings.TrimPrefix(hub.URL, "http://")
	save := func(id, key, extra string, isNew bool) {
		t.Helper()
		body := `{"id":"` + id + `","name":"` + id + `","preset":"remote-magpie","chat":"` + at + `","key":"` + key + `","models":[],"headers":{}` + extra
		if isNew {
			body += `,"new":true}`
		} else {
			body += `,"from":"` + id + `"}`
		}
		post(body)
	}
	on := func(id string) (bool, bool) { return library.RemoteMCPOn()[id], library.BorrowedOn()[id] }
	readOf := func() string {
		t.Helper()
		select {
		case id := <-read:
			return id
		case <-time.After(10 * time.Second):
			return ""
		}
	}
	noRead := func(what string) {
		t.Helper()
		select {
		case id := <-read:
			t.Errorf("%s: the Library of %s was read", what, id)
		default:
		}
	}

	save("home", "full", "", true)
	if m, s := on("home"); !m || !s {
		t.Fatalf("added with a full key: switches %v %v, want both on", m, s)
	}
	if id := readOf(); id != "home" {
		t.Fatalf("added with a full key: its Library wasn't read (%q)", id)
	}

	save("home2", "full-2", `,"libraryMCP":false`, true)
	if m, s := on("home2"); m || !s {
		t.Fatalf("MCP unticked on Add: switches %v %v", m, s)
	}
	if id := readOf(); id != "home2" {
		t.Fatalf("MCP unticked on Add: its Library wasn't read (%q)", id)
	}

	save("friend", "held", "", true)
	if m, s := on("friend"); m || s {
		t.Fatalf("a key with a budget: switches %v %v", m, s)
	}
	noRead("a key with a budget")

	// a provider already there, saved with a full key: as it was
	if err := provider.Save(provider.Provider{ID: "office", Name: "office", Preset: provider.RemoteMagpiePreset, Key: "held", Chat: at}); err != nil {
		t.Fatal(err)
	}
	save("office", "full-3", "", false)
	if m, s := on("office"); m || s {
		t.Fatalf("an existing provider's save: switches %v %v", m, s)
	}
	noRead("an existing provider's save")
}

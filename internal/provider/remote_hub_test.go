package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// A Remote magpie whose models are listed is asked, with its key, whether
// its router has a server to give, while the library would give it one
// (RemoteHubAsk); one that says so is noted (RemoteHubFound). One that
// doesn't say, a magpie from before it, isn't; nor one the library
// doesn't ask about, nor one found to be this magpie. RemoteHubs lists the
// Remote magpies with their address and key, and fails while
// providers.json can't be read.
func TestRemoteHubNoted(t *testing.T) {
	isolate(t)
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	var mu sync.Mutex
	info, lister := `{"name":"magpie","mcp_hub":true}`, "another"
	var asked []string // the info's requests, by key
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1" {
			asked = append(asked, r.Header.Get("Authorization"))
			io.WriteString(w, info)
			return
		}
		io.WriteString(w, `{"object":"list","data":[{"id":"own/m1","object":"model","magpie_via":["`+lister+`"]}]}`)
	}))
	t.Cleanup(srv.Close)
	id, err := Add(Provider{ID: "rem", Name: "Rem", Preset: RemoteMagpiePreset, Key: "k1", Chat: strings.TrimPrefix(srv.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	want := true
	var found []string
	oldA, oldF := RemoteHubAsk, RemoteHubFound
	t.Cleanup(func() { RemoteHubAsk, RemoteHubFound = oldA, oldF })
	RemoteHubAsk = func(string) bool { return want }
	RemoteHubFound = func(id string) { found = append(found, id) }
	fetch := func() {
		t.Helper()
		p, err := Find(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	reset := func() { mu.Lock(); asked = nil; mu.Unlock(); found = nil }

	fetch()
	if !slices.Equal(found, []string{"rem"}) || !slices.Equal(asked, []string{"Bearer k1"}) {
		t.Fatalf("found %v, asked %v; want rem, with its key", found, asked)
	}
	hs, err := RemoteHubs()
	if err != nil || len(hs) != 1 || !reflect.DeepEqual(hs[0], RemoteHub{ID: "rem", Name: "Rem", Root: srv.URL, Key: "k1"}) {
		t.Fatalf("RemoteHubs: %+v %v", hs, err)
	}
	// renamed (treeman_26897): listed under its id now, with the one it
	// had, which its Library server follows
	if err := Rename("rem", "office"); err != nil {
		t.Fatal(err)
	}
	id = "office"
	if hs, err := RemoteHubs(); err != nil || len(hs) != 1 || hs[0].ID != "office" || !slices.Equal(hs[0].Was, []string{"rem"}) {
		t.Fatalf("RemoteHubs after a rename: %+v %v", hs, err)
	}
	// its switch turned on: asked at once, under its id now
	reset()
	AskRemoteHub(context.Background(), "office")
	if !slices.Equal(found, []string{"office"}) || len(asked) != 1 {
		t.Fatalf("asked at once: found %v, asked %v", found, asked)
	}

	reset()
	want = false
	fetch()
	if len(found) != 0 || len(asked) != 0 {
		t.Errorf("not wanted: found %v, asked %v", found, asked)
	}

	reset()
	want = true
	mu.Lock()
	info = `{"name":"magpie","version":"v0.1.1"}`
	mu.Unlock()
	fetch()
	if len(found) != 0 || len(asked) != 1 {
		t.Errorf("a magpie from before: found %v, asked %v", found, asked)
	}

	// this magpie itself
	reset()
	mu.Lock()
	info, lister = `{"name":"magpie","mcp_hub":true}`, catalog.Self()
	mu.Unlock()
	fetch()
	if len(found) != 0 || len(asked) != 0 {
		t.Errorf("this magpie: found %v, asked %v", found, asked)
	}
	if hs, err := RemoteHubs(); err != nil || len(hs) != 0 {
		t.Errorf("RemoteHubs with this magpie: %+v %v", hs, err)
	}
}

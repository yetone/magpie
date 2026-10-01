package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

// A Remote magpie's list has the image models the other magpie draws with,
// which draw there through it, and names each model with its provider
// there, so the same model of two of its relays isn't two of one name here
// (Jorben, 莫 on Discord). What it lists is still what its agents are
// shown: a model its user didn't pick, or of a provider kept to routing
// groups, isn't in it.
func TestRemoteMagpieModels(t *testing.T) {
	var mu sync.Mutex
	var vendor, hops []string // paths the vendor, and the remote magpie, were asked on
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		vendor = append(vendor, r.URL.Path+" "+gjsonModel(b))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}}]}`)
			return
		}
		io.WriteString(w, `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`)
	}))
	t.Cleanup(up.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, p := range []provider.Provider{
		{ID: "relay-a", Name: "Relay A", Key: "k", Models: []string{"claude-sonnet-5"}, Chat: up.URL + "/v1"},
		{ID: "relay-b", Name: "Relay B", Key: "k", Models: []string{"claude-sonnet-5"}, Chat: up.URL + "/v1"},
		{ID: "routed", Name: "Routed", Key: "k", Models: []string{"secret-1"}, Chat: up.URL + "/v1", Unlisted: true},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	// relay A's own list: a model to chat with, one not picked, and two
	// that draw — one whose id doesn't say so
	if err := catalog.SaveLive("relay-a", up.URL+"/v1", []catalog.Model{
		{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"}, {ID: "unpicked-1", Name: "Unpicked"},
		{ID: "seedream-4", Name: "Seedream 4", Draws: true}, {ID: "nano-banana-pro", Name: "Nano Banana Pro", Draws: true},
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"relay-b", "routed"} {
		if err := catalog.SaveLive(id, up.URL+"/v1", []catalog.Model{{ID: "claude-sonnet-5", Name: "Claude Sonnet 5"}, {ID: "secret-1"}}); err != nil {
			t.Fatal(err)
		}
	}
	h := New().Handler()
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			mu.Lock()
			hops = append(hops, r.URL.Path+" "+r.Header.Get("Content-Type"))
			mu.Unlock()
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(remote.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(remote.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, _ := provider.Find(id)
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	// the image models are listed as such, never as models to chat with
	var draws []string
	for _, m := range Drawers(*office) {
		draws = append(draws, m.ID)
	}
	for _, want := range []string{"relay-a/seedream-4", "relay-a/nano-banana-pro"} {
		if !slices.Contains(draws, want) {
			t.Errorf("%s isn't one of the remote's image models here: %v", want, draws)
		}
	}
	live, _, _ := catalog.Live("office")
	names := map[string]string{}
	for _, m := range live {
		names[m.ID] = m.Name
	}
	for _, id := range []string{"relay-a/seedream-4", "relay-a/nano-banana-pro"} {
		if _, ok := names[id]; ok {
			t.Errorf("%s is listed to chat with", id)
		}
	}
	// what the remote's agents aren't shown isn't here either
	for _, id := range []string{"relay-a/unpicked-1", "routed/secret-1"} {
		if _, ok := names[id]; ok {
			t.Errorf("%s is listed, which the remote's agents aren't shown", id)
		}
	}

	// the same model of two of the remote's relays, told apart
	a, okA := provider.EntryOf("office/relay-a/claude-sonnet-5")
	b, okB := provider.EntryOf("office/relay-b/claude-sonnet-5")
	if !okA || !okB {
		t.Fatalf("the relays' models aren't listed: %v", names)
	}
	if a.Label() == b.Label() || !strings.Contains(a.Label(), "Relay A") || !strings.Contains(b.Label(), "Relay B") {
		t.Errorf("the relays' models read %q and %q", a.Label(), b.Label())
	}

	// an image model of the remote's draws there, at its images API, which
	// asks the vendor the way the model draws: on its images API, or in
	// chat (nano-banana-pro)
	for _, c := range []struct{ model, vendor string }{
		{"relay-a/seedream-4", "/v1/images/generations seedream-4"},
		{"relay-a/nano-banana-pro", "/v1/chat/completions nano-banana-pro"},
	} {
		mu.Lock()
		vendor, hops = nil, nil
		mu.Unlock()
		code, body := post(t, "/v1/images/generations", `{"model":"office/`+c.model+`","prompt":"a magpie"}`)
		mu.Lock()
		gotHops, gotVendor := slices.Clone(hops), slices.Clone(vendor)
		mu.Unlock()
		if code != 200 || !strings.Contains(body, "aGVsbG8=") {
			t.Errorf("%s: %d %s", c.model, code, body)
		}
		if !slices.Equal(gotHops, []string{"/v1/images/generations application/json"}) || !slices.Equal(gotVendor, []string{c.vendor}) {
			t.Errorf("%s: remote asked on %v, vendor on %v", c.model, gotHops, gotVendor)
		}
	}
}

// A magpie from before the list said more takes the list as it was: the
// model's own name, no image models.
func TestRemoteMagpieOldList(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"object":"list","data":[{"id":"relay-a/claude-sonnet-5","object":"model","owned_by":"relay-a","display_name":"Claude Sonnet 5","native_endpoints":["/v1/messages"]}]}`)
	}))
	t.Cleanup(old.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Key: "k", Preset: provider.RemoteMagpiePreset, Chat: old.URL})
	if err != nil {
		t.Fatal(err)
	}
	office, _ := provider.Find(id)
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	live, _, _ := catalog.Live("office")
	if len(live) != 1 || live[0].Name != "Claude Sonnet 5" || !slices.Equal(live[0].APIs, []string{"anthropic"}) {
		t.Errorf("old list read as %+v", live)
	}
	if d := Drawers(*office); len(d) != 0 {
		t.Errorf("image models from a list with none: %+v", d)
	}
}

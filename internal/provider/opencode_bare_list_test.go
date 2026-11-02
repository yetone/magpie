package provider

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
)

// refusedOpenCode answers OpenCode's gateway as it answers a model list:
// an ask that carries the provider's key is refused (#4900, an expired or
// wrong key), and the same list asked bare is answered, as the vendor
// serves its /models to whoever asks.
type refusedOpenCode struct {
	mu    sync.Mutex
	keyed []string // the list URLs asked with a key
	bare  []string // the list URLs asked without one
}

func (f *refusedOpenCode) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
	h := http.Header{"Content-Type": {"application/json"}}
	u := r.URL.String()
	keyed := r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != ""
	f.mu.Lock()
	if keyed {
		f.keyed = append(f.keyed, u)
	} else {
		f.bare = append(f.bare, u)
	}
	f.mu.Unlock()
	if keyed {
		return &http.Response{
			StatusCode: 401, Header: h, Request: r,
			Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Invalid API key"}}`)),
		}, nil
	}
	return &http.Response{
		StatusCode: 200, Header: h, Request: r,
		Body: io.NopCloser(strings.NewReader(`{"data":[` +
			`{"id":"beam-free","name":"Beam Free"},` +
			`{"id":"iceman-contributor-free","name":"Iceman Contributor"},` +
			`{"id":"glm-5.3","name":"GLM 5.3"}]}`)),
	}, nil
}

// #4900 (51bit.eth, via yetone): OpenCode Go's /models refuses an expired
// key, and magpie lists the models.dev catalog in its place — free rows Go
// doesn't serve among them. The vendor's list is public: a list the key is
// refused is asked again bare, so the picker says what OpenCode serves,
// not what models.dev remembers of it.
func TestOpenCodeListAskedBareWhenTheKeyIsRefused(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	// what models.dev files under opencode-go on 2026-11-02: a free row
	// Go doesn't serve, beside none of the free rows it does
	body := `{"opencode-go":{"models":{"cat-jam-free":{"id":"cat-jam-free","name":"Cat Jam","cost":{"input":0,"output":0}}}}}`
	if err := os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog.CachePath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &refusedOpenCode{}
	oldT := http.DefaultClient.Transport
	http.DefaultClient.Transport = f
	t.Cleanup(func() { http.DefaultClient.Transport = oldT })

	p := Provider{ID: "opencode-go", Name: "OpenCode Go", Key: "expired", Chat: "https://opencode.ai/zen/go/v1"}
	ms, err := p.fetch(context.Background())
	if err != nil {
		t.Fatalf("the bare ask should have answered where the key was refused: %v", err)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	for _, want := range []string{"beam-free", "iceman-contributor-free", "glm-5.3"} {
		if !slices.Contains(ids, want) {
			t.Fatalf("the vendor's list is missing %s: %v", want, ids)
		}
	}
	// the vendor's word is kept: the picker lists it, not the catalog's guess
	var listed []string
	for _, m := range p.Available() {
		listed = append(listed, m.ID)
	}
	if slices.Contains(listed, "cat-jam-free") {
		t.Fatalf("the catalog's free row Go doesn't serve is still listed: %v", listed)
	}
	if !slices.Contains(listed, "beam-free") {
		t.Fatalf("the vendor's free model is not listed: %v", listed)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.keyed) == 0 || len(f.bare) == 0 {
		t.Fatalf("keyed asks %v, bare asks %v", f.keyed, f.bare)
	}
}

// A list refused at another vendor's host is not asked bare: no other
// vendor's list is known to answer without a key, and a partial public
// list would stand in for the keyed one.
func TestOpenCodeListNotAskedBareAtAnotherHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	f := &refusedOpenCode{}
	oldT := http.DefaultClient.Transport
	http.DefaultClient.Transport = f
	t.Cleanup(func() { http.DefaultClient.Transport = oldT })

	p := Provider{ID: "relay", Name: "Relay", Key: "expired", Chat: "https://relay.test/v1"}
	if _, err := p.fetch(context.Background()); err == nil {
		t.Fatal("a refused list at another host should fail as before")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bare) != 0 {
		t.Fatalf("asked bare at another host: %v", f.bare)
	}
}

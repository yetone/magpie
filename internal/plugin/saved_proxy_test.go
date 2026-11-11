package plugin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
)

// A plugin's request whose choice names a saved proxy goes through it;
// editing the saved proxy moves the next request to the new address, and
// one whose proxy was deleted fails and never goes direct.
func TestPluginSavedProxy(t *testing.T) {
	sandbox(t)
	var mu sync.Mutex
	hits := map[string]int{}
	proxy := func(name string) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hits[name]++
			mu.Unlock()
			fmt.Fprint(w, name)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	one, two := proxy("one"), proxy("two")
	t.Setenv("FAKE_BASE", "http://vendor.invalid/v1")
	t.Setenv("FAKE_USAGE", "http://vendor.invalid/usage")
	t.Setenv("FAKE_MODELS", "http://vendor.invalid/models")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := APIKey(ctx, "fakeco", 0, nil, "k1", NewAccount); err != nil {
		t.Fatal(err)
	}
	save := func(addr string) {
		t.Helper()
		var ps []settings.NamedProxy
		if addr != "" {
			ps = []settings.NamedProxy{{ID: "pxp", Name: "P", URL: addr}}
		}
		if err := settings.Save(settings.Settings{Proxies: ps}); err != nil {
			t.Fatal(err)
		}
	}
	fetch := func(choice string) (string, error) {
		res, err := Fetch(netproxy.With(ctx, choice), FetchRequest{Provider: "fakeco", Model: "fake-1", NPM: "@ai-sdk/openai-compatible",
			URL: "http://vendor.invalid/v1/chat/completions", Method: "POST", Body: []byte(`{}`)})
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		return string(b), err
	}
	save(one.URL)
	if b, err := fetch("@pxp"); err != nil || b != "one" {
		t.Fatalf("through the saved proxy: %q, %v", b, err)
	}
	save(two.URL)
	if b, err := fetch("@pxp"); err != nil || b != "two" {
		t.Fatalf("after the edit: %q, %v", b, err)
	}
	save("")
	mu.Lock()
	n := hits["one"] + hits["two"]
	mu.Unlock()
	if b, err := fetch("@pxp"); err == nil {
		t.Fatalf("a deleted proxy's request was carried: %q", b)
	}
	mu.Lock()
	m := hits["one"] + hits["two"]
	mu.Unlock()
	if m != n {
		t.Fatalf("the request went through a proxy after its saved proxy was deleted")
	}
}

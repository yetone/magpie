package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// The update feed and a release's download go through the proxy set in
// Settings, as magpie's other requests do (#294): a network that reaches
// usemagpie.ai and GitHub only through it gets its updates too.
func TestUpdateGoesThroughTheProxy(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(k, "")
	}
	body := []byte("new magpie")
	sum := sha256.Sum256(body)
	var mu sync.Mutex
	var seen []string
	// the proxy: a plain http:// request comes to it whole, by its URL
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.String())
		mu.Unlock()
		if r.URL.Path == "/asset" {
			w.Write(body)
			return
		}
		json.NewEncoder(w).Encode(Release{Version: "9.9.9", Assets: map[string]Asset{
			BinaryAsset(): {URL: "http://releases.magpie.invalid/asset", SHA256: hex.EncodeToString(sum[:])},
		}})
	}))
	defer px.Close()
	if err := settings.Save(settings.Settings{Proxy: px.URL}); err != nil {
		t.Fatal(err)
	}
	// hosts that resolve nowhere: only the proxy can answer for them
	t.Setenv("MAGPIE_UPDATE_FEED", "http://feed.magpie.invalid/api/latest")
	rel, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "9.9.9" {
		t.Fatalf("version %q", rel.Version)
	}
	path := filepath.Join(t.TempDir(), "magpie.new")
	if err := download(context.Background(), rel.Assets[BinaryAsset()], path); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"http://feed.magpie.invalid/api/latest", "http://releases.magpie.invalid/asset"}; !slices.Equal(seen, want) {
		t.Fatalf("through the proxy: %v, want %v", seen, want)
	}
}

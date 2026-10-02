package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// The Plugins page is drawn in parts (#488): the plugins suggested come at
// once, each with what npm said of it last (kept on disk), without asking
// npm; asking it is /api/plugins/npm's, which takes package names only.
func TestPluginMarketInParts(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("MAGPIE_PLUGIN_MARKET", "off")
	const pkg = "@magpie-community/opencode-zcode-auth"
	kept := map[string]any{pkg: map[string]any{"info": map[string]any{"version": "0.4.0", "weekly": 321}, "at": time.Now().Add(-48 * time.Hour)}}
	b, _ := json.Marshal(kept)
	if err := os.MkdirAll(filepath.Join(cache, "magpie"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "magpie", "plugin-npm.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	plugin.ReloadInfo()
	t.Cleanup(plugin.ReloadInfo)
	mux := http.NewServeMux()
	pluginRoutes(mux, nil)
	get := func(path string, v any) {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}

	var ls struct {
		Listings []struct {
			Package string
			NPM     *plugin.NPM
		}
	}
	start := time.Now()
	get("/api/plugins/listings", &ls)
	if took := time.Since(start); took > time.Second {
		t.Errorf("listings took %v", took)
	}
	if len(ls.Listings) < 10 {
		t.Fatalf("%d listed", len(ls.Listings))
	}
	for _, l := range ls.Listings {
		switch {
		case l.Package == pkg && (l.NPM == nil || l.NPM.Version != "0.4.0" || l.NPM.Weekly != 321):
			t.Errorf("%s: %+v, want what npm said last", l.Package, l.NPM)
		case l.Package != pkg && l.NPM != nil:
			t.Errorf("%s: %+v before npm was asked", l.Package, l.NPM)
		}
	}

	// no package names, nothing asked
	var n struct{ NPM map[string]plugin.NPM }
	get("/api/plugins/npm?names=not%20a%20name,..%2Fx,", &n)
	if n.NPM == nil || len(n.NPM) != 0 {
		t.Errorf("npm for no packages: %+v", n.NPM)
	}
}

package provider

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// Lemon on Discord: can a plugin give its provider its own icon? The auth
// hook's icon (or package.json's magpie.icon) is a data:image URI, kept
// at once as a picture given by hand is, or an https URL, fetched in the
// background through FetchIcon's checks; it comes before the market's,
// and anything else is refused.
func TestPluginOwnIcon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("MAGPIE_PLUGIN_MARKET", "off")

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	var fetched atomic.Int32
	was := fetchPluginIcon
	t.Cleanup(func() { fetchPluginIcon = was })
	fetchPluginIcon = func(_ context.Context, u string) (string, error) {
		fetched.Add(1)
		if u != "https://lemon.example/icon.png" {
			t.Errorf("fetched %q", u)
		}
		return StoreIcon(png)
	}

	// a data URI: kept at once, over the market's icon for its provider
	data := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	ic := PluginIcon(plugin.Provider{ID: "google", Spec: "lemon-plugin", Icon: data})
	if !strings.HasPrefix(ic, "file:") {
		t.Fatalf("data URI icon = %q", ic)
	}
	if f := IconFile(strings.TrimPrefix(ic, "file:")); f == "" {
		t.Fatalf("%q isn't a stored picture", ic)
	}
	// none given: the market's, as before
	if ic := PluginIcon(plugin.Provider{ID: "google", Spec: "lemon-plugin"}); ic != "gemini-color" {
		t.Errorf("no icon given = %q", ic)
	}

	// an https URL: the market's (none here) until it is fetched, then its
	pp := plugin.Provider{ID: "lemon", Spec: "lemon-plugin", Icon: "https://lemon.example/icon.png"}
	if ic := PluginIcon(pp); ic != "" {
		t.Errorf("before the fetch = %q", ic)
	}
	var got string
	for i := 0; i < 100 && !strings.HasPrefix(got, "file:"); i++ {
		time.Sleep(20 * time.Millisecond)
		got = PluginIcon(pp)
	}
	if !strings.HasPrefix(got, "file:") || fetched.Load() != 1 {
		t.Fatalf("after the fetch = %q, fetched %d times", got, fetched.Load())
	}

	// refused: not https, not a picture, a private host, a picture over 1 MB
	big := "data:image/png;base64," + base64.StdEncoding.EncodeToString(append(png, make([]byte, MaxIcon)...))
	for _, bad := range []string{"http://lemon.example/icon.png", "javascript:alert(1)", "data:text/html,<b>x</b>", "https://127.0.0.1/icon.png", "https://localhost/icon.png", "file:///etc/passwd", big} {
		if ic := PluginIcon(plugin.Provider{ID: "lemon", Spec: "lemon-plugin", Icon: bad}); ic != "" {
			t.Errorf("%.40q taken as %q", bad, ic)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if fetched.Load() != 1 {
		t.Errorf("fetched %d times", fetched.Load())
	}

	// kept across a restart, and not pruned as a picture no provider has
	plugIcons.Lock()
	plugIcons.from = ""
	plugIcons.Unlock()
	old := time.Now().Add(-2 * time.Hour)
	for _, name := range keptPluginIcons() {
		os.Chtimes(IconFile(name), old, old)
	}
	pruneIcons(file{})
	if ic := PluginIcon(pp); ic != got {
		t.Errorf("after a restart and a prune = %q, want %q", ic, got)
	}
	if fetched.Load() != 1 {
		t.Errorf("fetched again: %d", fetched.Load())
	}
}

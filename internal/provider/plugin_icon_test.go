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
	t.Setenv("USERPROFILE", home)
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

// yetone: 非官方插件显示不出来 logo. A repository's https picture not fetched
// before is waited for, so the GitHub list's first answer has it; one slower
// than the wait shows the next time, and the wait ends when it does.
func TestRepoIconsWaitForFirstFetch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	slow := make(chan struct{})
	// the fetch left waiting ends with the test
	t.Cleanup(func() {
		close(slow)
		for i := 0; i < 200; i++ {
			plugIcons.Lock()
			n := len(plugIcons.busy)
			plugIcons.Unlock()
			if n == 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("the slow fetch didn't end")
	})
	was := fetchPluginIcon
	t.Cleanup(func() { fetchPluginIcon = was })
	fetchPluginIcon = func(ctx context.Context, u string) (string, error) {
		if strings.Contains(u, "slow") {
			select {
			case <-slow:
			case <-ctx.Done():
			}
			return "", ctx.Err()
		}
		time.Sleep(100 * time.Millisecond)
		return StoreIcon(png)
	}

	data := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	said := []string{"https://gaccode.example/favicon.png", "", data}
	got := RepoIcons(said, 5*time.Second)
	if !strings.HasPrefix(got[0], "file:") || got[1] != "" || !strings.HasPrefix(got[2], "file:") {
		t.Fatalf("first answer = %q", got)
	}

	// The slow fetch is held until the test ends, so RepoIcons coming back at
	// all is its wait ending it (#1617); the floor is that it did wait, which
	// a busy machine only lengthens. The deadline only turns a wait past the
	// 300ms into a FAIL, and is short of the fetch's own 30s.
	type answer struct {
		icon string
		took time.Duration
	}
	answered := make(chan answer, 1)
	go func() {
		start := time.Now()
		got := RepoIcons([]string{"https://slow.example/icon.png"}, 300*time.Millisecond)
		answered <- answer{got[0], time.Since(start)}
	}()
	select {
	case a := <-answered:
		if a.icon != "" {
			t.Fatalf("a fetch never finished = %q", a.icon)
		}
		if a.took < 250*time.Millisecond {
			t.Fatalf("waited %v for a 300ms wait", a.took)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RepoIcons waited past its 300ms for the held fetch")
	}
}

// /api/providers asks for every plugin provider's icon on each answer: a
// kept icon found on disk isn't looked for again within iconSeenFor, and
// one removed is given up (and fetched again) once that has passed.
func TestKeptIconNotLookedForEachAsk(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("MAGPIE_PLUGIN_MARKET", "off")

	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	data := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	pp := plugin.Provider{ID: "lemon", Spec: "lemon-plugin", Icon: data}
	ic := PluginIcon(pp)
	if !strings.HasPrefix(ic, "file:") {
		t.Fatalf("icon = %q", ic)
	}
	if again := PluginIcon(pp); again != ic {
		t.Fatalf("asked again = %q, want %q", again, ic)
	}
	f := IconFile(strings.TrimPrefix(ic, "file:"))
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	// within the minute: the file isn't looked for, so nothing finds it
	// gone and stores the picture again
	if got := PluginIcon(pp); got != ic {
		t.Errorf("removed within iconSeenFor = %q, want %q", got, ic)
	}
	if _, err := os.Stat(f); err == nil {
		t.Errorf("the file was looked for within iconSeenFor and stored again")
	}
	// past it: looked for, gone, and the data URI stored again
	plugIcons.Lock()
	for k := range plugIcons.seen {
		plugIcons.seen[k] = time.Now().Add(-iconSeenFor - time.Second)
	}
	plugIcons.tried = map[string]time.Time{}
	plugIcons.Unlock()
	if got := PluginIcon(pp); !strings.HasPrefix(got, "file:") {
		t.Errorf("past iconSeenFor = %q", got)
	} else if _, err := os.Stat(IconFile(strings.TrimPrefix(got, "file:"))); err != nil {
		t.Errorf("stored again: %v", err)
	}
}

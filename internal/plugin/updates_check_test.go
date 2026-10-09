package plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Check for updates asks npm now, whatever it said within the hour: each
// plugin says whether npm has a newer version, and why it couldn't be
// checked when npm didn't say; someone else's update found waits for the
// reader (the dot on Plugins), and nothing is installed.
func TestCheckNow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	const (
		ours    = "@magpie-community/opencode-zed-auth"
		theirs  = "opencode-gemini-auth"
		current = "@magpie-community/opencode-kiro-auth"
		gone    = "opencode-gone-auth"
		limited = "opencode-busy-auth"
		broken  = "opencode-broken-auth"
	)
	answers := map[string]string{ours: "0.1.4", theirs: "1.0.0", current: "0.2.0"}
	fakeNPM(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/downloads/") {
			w.Write([]byte(`{"downloads":1}`))
			return
		}
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), "/latest")
		switch name {
		case limited:
			http.Error(w, "slow down", http.StatusTooManyRequests)
		case broken:
			http.Error(w, "oops", http.StatusInternalServerError)
		case gone:
			http.NotFound(w, r)
		default:
			w.Write([]byte(`{"version":"` + answers[name] + `"}`))
		}
	})
	folder := filepath.Join(dir, "myplugin")
	if err := save(List{Plugins: []Entry{{Spec: ours}, {Spec: theirs + "@latest"}, {Spec: current}, {Spec: gone}, {Spec: limited}, {Spec: broken}, {Spec: "github:me/opencode-x-auth"}, {Spec: folder}}}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{ours, theirs, gone, limited, broken} {
		installedAt(t, p, "0.1.2")
	}
	installedAt(t, current, "0.2.0")
	// what npm said within the hour, before the new versions came out
	npmMu.Lock()
	for _, p := range []string{ours, theirs} {
		npmCached()[p] = npmEntry{NPM{Version: "0.1.2"}, time.Now().Add(-time.Minute)}
	}
	npmMu.Unlock()
	was := installNewest
	t.Cleanup(func() { installNewest = was })
	installNewest = func(_ context.Context, pkg, _ string) error { t.Errorf("a check installed %s", pkg); return nil }

	c := CheckNow(context.Background())
	got := map[string]string{}
	for _, p := range c.Plugins {
		got[p.Package] = p.Status + " " + p.Why + " " + p.Version + ">" + p.Latest
	}
	want := map[string]string{
		ours:                        "update  0.1.2>0.1.4",
		theirs:                      "update  0.1.2>1.0.0",
		current:                     "current  0.2.0>0.2.0",
		gone:                        "unknown missing 0.1.2>",
		limited:                     "unknown limited 0.1.2>",
		broken:                      "unknown registry 0.1.2>",
		"github:me/opencode-x-auth": "git  >",
		folder:                      "folder  >",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q", k, got[k], v)
		}
	}
	if c.At.IsZero() || len(c.Plugins) != len(want) {
		t.Fatalf("checked %+v", c)
	}
	for _, p := range c.Plugins {
		if p.Auto != (p.Package == ours || p.Package == current) {
			t.Errorf("%s: Auto %v", p.Package, p.Auto)
		}
	}
	// what npm said now is what the page reads, not what it said before
	if v := InfoCached([]string{ours})[ours].Version; v != "0.1.4" {
		t.Errorf("kept %s, want 0.1.4", v)
	}
	// someone else's update waits for the reader; the community's comes
	// by itself
	if p := PendingUpdates(); len(p.Waiting) != 1 || p.Waiting[0].Package != theirs || p.Waiting[0].Latest != "1.0.0" || p.Checked.IsZero() {
		t.Fatalf("waiting %+v", p)
	}
}

// npm not reached: every plugin says it couldn't be checked, offline, and
// what waited for the reader before still waits.
func TestCheckNowOffline(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	fakeNPM(t, func(http.ResponseWriter, *http.Request) {})
	down := httptest.NewServer(http.NotFoundHandler())
	npmRegistry = down.URL
	down.Close()
	const theirs = "opencode-gemini-auth"
	if err := save(List{Plugins: []Entry{{Spec: theirs}}}); err != nil {
		t.Fatal(err)
	}
	installedAt(t, theirs, "0.1.2")
	if err := os.MkdirAll(filepath.Dir(updatesPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(updatesPath(), []byte(`{"waiting":[{"spec":"`+theirs+`","package":"`+theirs+`","version":"0.1.2","latest":"0.2.0"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c := CheckNow(context.Background())
	if len(c.Plugins) != 1 || c.Plugins[0].Status != "unknown" || c.Plugins[0].Why != "offline" || c.Plugins[0].Error == "" {
		t.Fatalf("offline: %+v", c.Plugins)
	}
	if p := PendingUpdates(); len(p.Waiting) != 1 || p.Waiting[0].Latest != "0.2.0" {
		t.Fatalf("waiting %+v", p.Waiting)
	}
}

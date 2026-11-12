package gui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/update"
)

// "红点都快焊死了" (certhon520 and others on X): with a release every few
// minutes, a downloaded update was always there and the header's pill was
// always lit. /api/update says quiet for a version not behind for long,
// and not quiet for an urgent one.
func TestUpdateQuietUntilBehindAWhile(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("APPDATA", filepath.Join(h, "AppData"))
	was := Version
	Version = "0.1.100"
	t.Cleanup(func() { Version = was })
	now := time.Now()
	for _, c := range []struct {
		name  string
		b     update.Behind
		state string
		quiet bool
	}{
		{"just found", update.Behind{Base: "0.1.100", Since: now.Add(-time.Hour)}, "ready", true},
		{"behind for days", update.Behind{Base: "0.1.100", Since: now.Add(-update.Quiet - time.Minute)}, "ready", false},
		{"urgent at once", update.Behind{Base: "0.1.100", Since: now, Urgent: "0.1.104"}, "ready", false},
		{"found, held download", update.Behind{Base: "0.1.100", Since: now}, "found", true},
		{"up to date says nothing", update.Behind{Base: "0.1.100", Since: now}, "latest", false},
	} {
		u := &updater{state: c.state, behind: c.b, latest: &update.Release{Version: "0.1.105"}}
		if got := u.json().Quiet; got != c.quiet {
			t.Errorf("%s: quiet %v, want %v", c.name, got, c.quiet)
		}
	}
}

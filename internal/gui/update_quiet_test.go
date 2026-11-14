package gui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/update"
)

// /api/update says quiet only while the pill's dismissal holds ("红点都快
// 焊死了", certhon520 and others on X: its x hid it only until the next
// release, minutes later), and never for an urgent release. A version
// that is only behind, a download waiting or one found, is not quiet: the
// pill shows (visamzh and normalcoder_ on X, 11-14: it never came while a
// quiet counted from the first newer version started over on each update).
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
		{"downloaded, waiting", update.Behind{Base: "0.1.100"}, "ready", false},
		{"found, downloads off", update.Behind{Base: "0.1.100"}, "found", false},
		{"out, no download here", update.Behind{Base: "0.1.100"}, "available", false},
		{"dismissed an hour ago", update.Behind{Base: "0.1.100", Hushed: now.Add(-time.Hour)}, "ready", true},
		{"dismissed, found", update.Behind{Base: "0.1.100", Hushed: now.Add(-time.Hour)}, "found", true},
		{"dismissed days ago", update.Behind{Base: "0.1.100", Hushed: now.Add(-update.Quiet - time.Minute)}, "ready", false},
		{"urgent through a dismissal", update.Behind{Base: "0.1.100", Hushed: now, Urgent: "0.1.104"}, "ready", false},
		{"up to date says nothing", update.Behind{Base: "0.1.100", Hushed: now}, "latest", false},
	} {
		u := &updater{state: c.state, behind: c.b, latest: &update.Release{Version: "0.1.105"}}
		if got := u.json().Quiet; got != c.quiet {
			t.Errorf("%s: quiet %v, want %v", c.name, got, c.quiet)
		}
	}
}

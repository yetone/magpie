package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// A fresh install of Stable's version stays on Stable (fishmagic80 on X):
// usemagpie.ai's Stable download, 0.1.11, put in where magpie never ran,
// takes Settings' Update channel Stable before its first check, so that
// check finds nothing newer and 0.1.12 isn't offered. The newest's
// download, an install magpie ran in before, a version that is both
// Stable's and the newest, and a feed that can't be read leave the
// channel on every version.
func TestFreshStableInstallStaysOnStable(t *testing.T) {
	stableV, newest := "0.1.11", "0.1.12"
	var down atomic.Bool
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		if down.Load() {
			http.Error(w, "down", 502)
			return
		}
		v := newest
		if r.URL.Query().Get("channel") == "stable" {
			v = stableV
		}
		json.NewEncoder(w).Encode(update.Release{Version: v})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	oldV := Version
	defer func() { Version = oldV }()
	o := updates
	defer func() { updates = o }()
	defer freshInstall.Store(false)

	// start is a first run of version in a new magpie folder, or (used)
	// one that held settings before this run; then the first check
	run := func(t *testing.T, version string, used bool) updateJSON {
		t.Helper()
		sandboxHome(t)
		Version = version
		if used {
			s := settings.Load()
			s.Theme = "dark"
			if err := settings.Save(s); err != nil {
				t.Fatal(err)
			}
			old := started.Add(-time.Hour)
			if err := os.Chtimes(filepath.Join(settings.Dir(), "settings.json"), old, old); err != nil {
				t.Fatal(err)
			}
		}
		(&whatsNew{}).start()
		updates = &updater{}
		updates.check()
		return updates.json()
	}

	t.Run("the Stable download", func(t *testing.T) {
		j := run(t, "0.1.11", false)
		if got := settings.Load().UpdateChannel; got != settings.UpdateStable {
			t.Fatalf("channel %q", got)
		}
		if j.State != "latest" || j.Latest == newest {
			t.Fatalf("offered the newest: %+v", j)
		}
		if freshInstall.Load() {
			t.Fatal("still asks at the next check")
		}
	})
	t.Run("the newest's download", func(t *testing.T) {
		run(t, "0.1.12", false)
		if got := settings.Load().UpdateChannel; got != "" {
			t.Fatalf("channel %q", got)
		}
	})
	t.Run("magpie ran here before", func(t *testing.T) {
		before := asked.Load()
		run(t, "0.1.11", true)
		if got := settings.Load().UpdateChannel; got != "" {
			t.Fatalf("channel %q", got)
		}
		if n := asked.Load() - before; n != 1 {
			t.Fatalf("the feed was asked %d times, not once for the check itself", n)
		}
	})
	t.Run("Stable's is the newest too", func(t *testing.T) {
		stableV = newest
		defer func() { stableV = "0.1.11" }()
		run(t, newest, false)
		if got := settings.Load().UpdateChannel; got != "" {
			t.Fatalf("channel %q", got)
		}
	})
	t.Run("the feed down is unknown, asked again", func(t *testing.T) {
		down.Store(true)
		run(t, "0.1.11", false)
		if got := settings.Load().UpdateChannel; got != "" {
			t.Fatalf("channel %q on a failed read", got)
		}
		if !freshInstall.Load() {
			t.Fatal("a failed read settled it")
		}
		down.Store(false)
		updates.check()
		if got := settings.Load().UpdateChannel; got != settings.UpdateStable {
			t.Fatalf("the next check: channel %q", got)
		}
	})
}

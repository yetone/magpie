package gui

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/update"
)

// following runs u.followReplaced on short ticks as a magpie of version v,
// ended with the test.
func following(t *testing.T, u *updater, v string) {
	t.Helper()
	oldTick, oldVersion := replacedTick, Version
	replacedTick, Version = 5*time.Millisecond, v
	ctx, cancel := context.WithCancel(context.Background())
	var done sync.WaitGroup
	done.Add(1)
	go func() {
		defer done.Done()
		u.followReplaced(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		done.Wait()
		replacedTick, Version = oldTick, oldVersion
	})
}

// startedFrom is an updater for the binary at a new path, as this process
// started from it, whose restart counts.
func startedFrom(t *testing.T, restarts *atomic.Int32) (*updater, string) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "magpie")
	if err := os.WriteFile(exe, []byte("v0.1.1"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := &updater{state: "ready", exe: exe, latest: &update.Release{Version: "v0.1.2"}}
	u.self, _ = os.Stat(exe)
	u.autoRestart = func() bool { restarts.Add(1); return true }
	return u, exe
}

// stays waits most for a count that must stay what it is.
func stays(most time.Duration, n *atomic.Int32, want int32) bool {
	for end := time.Now().Add(most); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if n.Load() != want {
			return false
		}
	}
	return true
}

// visamzh on X: the app and magpie web open together, each with its own
// updater over the same program. One restarted to update and put the new
// version in; the other ran on as the old one, its Update pill still
// offering the version already in. Now the other sees what is on disk
// change and restarts into it, as for magpie update's ask, once. On the
// old code nothing looks, and nothing restarts.
func TestAnotherMagpiesUpdateRestartsThisOne(t *testing.T) {
	var restarts atomic.Int32
	u, exe := startedFrom(t, &restarts)
	following(t, u, "v0.1.1")
	if !stays(100*time.Millisecond, &restarts, 0) {
		t.Fatal("restarted with nothing put in")
	}
	// the window's restart to update puts the new version in
	if err := os.WriteFile(exe, []byte("v0.1.2 is longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	eventually(t, "magpie web never restarted into the version the app put in", func() bool { return restarts.Load() == 1 })
	if !stays(100*time.Millisecond, &restarts, 1) {
		t.Fatalf("restarted %d times for one update", restarts.Load())
	}
}

// What this process put in itself is the restart it is making already: a
// second, from looking at the disk, would start the new version twice.
func TestOwnUpdateIsNotFollowedAgain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("InstallBinaryAside starts the download to check it there; the click below covers the flag")
	}
	var restarts atomic.Int32
	u, exe := startedFrom(t, &restarts)
	staged := filepath.Join(filepath.Dir(exe), "magpie.new")
	if err := os.WriteFile(staged, []byte("v0.1.2 is longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	u.staged = staged
	following(t, u, "v0.1.1")
	if !u.install(true) {
		t.Fatalf("the install failed: %s", u.err)
	}
	if !u.replaced() {
		t.Fatal("the install left the binary as it was")
	}
	if !stays(150*time.Millisecond, &restarts, 0) {
		t.Fatal("restarted again for its own update")
	}
}

// A build of its own (make dev, go build) is rebuilt in place, which isn't
// an update to restart into.
func TestOwnBuildRebuiltIsNotFollowed(t *testing.T) {
	var restarts atomic.Int32
	u, exe := startedFrom(t, &restarts)
	following(t, u, "dev")
	if err := os.WriteFile(exe, []byte("rebuilt, and longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !stays(150*time.Millisecond, &restarts, 0) {
		t.Fatal("a dev build restarted for its rebuild")
	}
}

// With the gateway busy the restart waits for it, and then goes into what
// the other put in whatever the feed says: here it can't be reached, with
// nothing downloaded by this one. On the old code the wait ended with
// nothing to restart into.
func TestAnotherMagpiesUpdateWaitsForTheGatewayThenRestarts(t *testing.T) {
	var restarts atomic.Int32
	u, exe := startedFrom(t, &restarts)
	busy := &fakeBusy{}
	busy.set(gateway.Busy{Requests: 1, Last: time.Now()})
	quick(t, busy, u)
	waitRecheck = (*updater).recheck // the feed asked, and out of reach
	following(t, u, "v0.1.1")
	if err := os.WriteFile(exe, []byte("v0.1.2 is longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	eventually(t, "never waited for the gateway", func() bool { w, _ := u.waitingFor(); return w })
	if restarts.Load() != 0 {
		t.Fatal("restarted with a request in flight")
	}
	busy.set(gateway.Busy{})
	eventually(t, "never restarted once the gateway was idle", func() bool { return restarts.Load() == 1 })
}

// A click on the old one's Update, the other's version in already, is its
// restart: looking at the disk doesn't make a second.
func TestUpdateClickedOverAnothersIsNotFollowedAgain(t *testing.T) {
	var restarts atomic.Int32
	u, exe := startedFrom(t, &restarts)
	if err := os.WriteFile(exe, []byte("v0.1.2 is longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !u.install(true) {
		t.Fatal("the other's version wasn't taken as put in")
	}
	following(t, u, "v0.1.1")
	if !stays(150*time.Millisecond, &restarts, 0) {
		t.Fatal("restarted again beside the click's restart")
	}
}

// boot.js names the version that served the page, which a browser's page
// compares with /api/update's current to reload once another magpie, here
// magpie web restarted into the app's update, answers in its place.
func TestBootNamesTheVersionThatServedThePage(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", h+"/.config")
	t.Setenv("XDG_CACHE_HOME", h+"/.cache")
	oldVersion := Version
	Version = "0.1.777"
	defer func() { Version = oldVersion }()
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/boot.js", nil))
	if !strings.Contains(rec.Body.String(), `"version":"0.1.777"`) {
		t.Fatalf("boot.js doesn't name the version: %s", rec.Body)
	}
}

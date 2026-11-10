package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// #2096: with Settings' AutoRestart on, a downloaded update is restarted
// into by itself, but only once the gateway has nothing in flight; off, or
// with automatic updates off, it waits for a click as before.
func TestAutoRestartWaitsForIdle(t *testing.T) {
	busy := &fakeBusy{}
	busy.set(gateway.Busy{Requests: 1})
	u := &updater{state: "ready", latest: &update.Release{Version: "0.1.999"}}
	quick(t, busy, u)
	var restarts atomic.Int32
	u.autoRestart = func() bool { restarts.Add(1); return true }

	for _, s := range []settings.Settings{{}, {AutoRestart: true, NoAutoUpdate: true}} {
		u.autoTick(s)
		if w, _ := u.waitingFor(); w {
			t.Fatalf("%+v: a restart waits", s)
		}
	}
	on := settings.Settings{AutoRestart: true}
	u.autoTick(on)
	if w, _ := u.waitingFor(); !w {
		t.Fatal("AutoRestart on, update ready: no restart waits")
	}
	time.Sleep(150 * time.Millisecond)
	if restarts.Load() != 0 {
		t.Fatal("restarted with a request in flight")
	}
	u.autoTick(on) // a tick while waiting doesn't start a second wait
	busy.set(gateway.Busy{Last: time.Now()})
	eventually(t, "never restarted once idle", func() bool { return restarts.Load() == 1 })
	time.Sleep(50 * time.Millisecond)
	if n := restarts.Load(); n != 1 {
		t.Fatalf("restarted %d times", n)
	}
}

// Nothing downloaded, nothing to restart into: no wait.
func TestAutoRestartNeedsAReadyUpdate(t *testing.T) {
	busy := &fakeBusy{}
	for _, state := range []string{"latest", "downloading", "checking", "error", "available"} {
		u := &updater{state: state, latest: &update.Release{Version: "0.1.999"}}
		quick(t, busy, u)
		u.autoRestart = func() bool { t.Errorf("%s: restarted", state); return true }
		u.autoTick(settings.Settings{AutoRestart: true})
		if w, _ := u.waitingFor(); w {
			t.Errorf("%s: a restart waits", state)
		}
	}
}

// The update card's Cancel calls the restart off for that version: the
// next tick doesn't wait for it again, a newer one is restarted into.
func TestAutoRestartCalledOff(t *testing.T) {
	busy := &fakeBusy{}
	busy.set(gateway.Busy{Requests: 1})
	old := updates
	updates = &updater{state: "ready", latest: &update.Release{Version: "0.1.999"}}
	t.Cleanup(func() { updates = old })
	quick(t, busy, updates)
	var restarts atomic.Int32
	updates.autoRestart = func() bool { restarts.Add(1); return true }
	on := settings.Settings{AutoRestart: true}
	updates.autoTick(on)
	mux := http.NewServeMux()
	updateRoutes(mux, &quitCount{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/update/install", strings.NewReader(`{"when":"cancel"}`)))
	var j updateJSON
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &j) != nil || j.Waiting {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	busy.set(gateway.Busy{})
	updates.autoTick(on)
	if w, _ := updates.waitingFor(); w {
		t.Fatal("waits again for the version called off")
	}
	updates.mu.Lock()
	updates.latest = &update.Release{Version: "0.1.1000"}
	updates.mu.Unlock()
	updates.autoTick(on)
	eventually(t, "a newer version wasn't restarted into", func() bool { return restarts.Load() == 1 })
}

// BESTLJP0412 on X: a restart by itself came while the user was adding a
// provider in the window, the gateway idle. It waits while a page tells of
// input, longer with a dialog open, and comes once that has stopped; a
// restart the user asked for doesn't wait for it.
func TestAutoRestartWaitsWhileTheWindowIsUsed(t *testing.T) {
	busy := &fakeBusy{}
	busy.set(gateway.Busy{Last: time.Now().Add(-time.Hour)})
	old := updates
	updates = &updater{state: "ready", latest: &update.Release{Version: "0.1.999"}}
	t.Cleanup(func() { updates = old })
	quick(t, busy, updates)
	oldUsing, oldOpen := usingFor, openFor
	usingFor, openFor = 100*time.Millisecond, time.Minute
	t.Cleanup(func() { usingFor, openFor = oldUsing, oldOpen })
	var restarts atomic.Int32
	updates.autoRestart = func() bool { restarts.Add(1); return true }
	mux := http.NewServeMux()
	updateRoutes(mux, &quitCount{})
	using := func(open bool) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/update/using", strings.NewReader(fmt.Sprintf(`{"open":%t}`, open))))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("using: %d %s", rec.Code, rec.Body)
		}
	}

	using(true) // typing in the Add provider dialog
	updates.autoTick(settings.Settings{AutoRestart: true})
	if w, _ := updates.waitingFor(); !w {
		t.Fatal("no restart waits")
	}
	time.Sleep(300 * time.Millisecond) // three times usingFor: the dialog holds it
	if n := restarts.Load(); n != 0 {
		t.Fatal("restarted while the user had a dialog open")
	}
	if j := updates.json(); !j.Using || !j.Waiting {
		t.Fatalf("the wait doesn't say it is for the user: %+v", j)
	}
	using(false) // the dialog closed
	eventually(t, "never restarted once the window was left", func() bool { return restarts.Load() == 1 })

	// a restart the user asked for doesn't wait for them
	using(true)
	updates.waitIdle(func() bool { restarts.Add(1); return true }, true)
	eventually(t, "a restart asked for waited for the window", func() bool { return restarts.Load() == 2 })
}

// magpie update run in a terminal (an agent's) restarts the app as
// AutoRestart does, so it waits for the user's window too.
func TestTerminalUpdateWaitsWhileTheWindowIsUsed(t *testing.T) {
	busy := &fakeBusy{}
	busy.set(gateway.Busy{Last: time.Now().Add(-time.Hour)})
	exe := filepath.Join(t.TempDir(), "magpie")
	if err := os.WriteFile(exe, []byte("v0.1.1"), 0o755); err != nil {
		t.Fatal(err)
	}
	u := &updater{state: "latest", exe: exe}
	u.self, _ = os.Stat(exe)
	quick(t, busy, u)
	oldUsing := usingFor
	usingFor = 150 * time.Millisecond
	t.Cleanup(func() { usingFor = oldUsing })
	var restarts atomic.Int32
	u.autoRestart = func() bool { restarts.Add(1); return true }
	if err := os.WriteFile(exe, []byte("v0.1.2 is longer"), 0o755); err != nil {
		t.Fatal(err)
	}

	noteUse(false, time.Now())
	if r := u.restartBeside("v0.1.2"); r.State != "waiting" {
		t.Fatalf("restarted with the window in use: %+v", r)
	}
	eventually(t, "never restarted once the window was left", func() bool { return restarts.Load() == 1 })
}

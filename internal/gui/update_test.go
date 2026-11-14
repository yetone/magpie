package gui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// A version downloaded and waiting for a restart gives way to a newer one
// out since when a check is asked for (Settings' Check). A restart doesn't
// ask, with downloads on a click too: it goes into the one waiting
// (TestRestartPutsInTheClickedDownload).
func TestUpdateStagedGivesWayToNewer(t *testing.T) {
	sandboxHome(t)
	body := []byte("new magpie")
	sum := sha256.Sum256(body)
	var latest atomic.Value
	latest.Store("0.1.11")
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			downloads.Add(1)
			w.Write(body)
			return
		}
		json.NewEncoder(w).Encode(update.Release{Version: latest.Load().(string), Assets: map[string]update.Asset{
			update.BinaryAsset(): {URL: "http://" + r.Host + "/asset", SHA256: hex.EncodeToString(sum[:])},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	old := Version
	Version = "0.1.10"
	defer func() { Version = old }()
	exe, err := update.Executable()
	if err != nil {
		t.Fatal(err)
	}
	u := &updater{exe: exe}
	u.self, _ = os.Stat(exe)
	defer os.Remove(exe + ".new")

	u.check()
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" || downloads.Load() != 1 {
		t.Fatalf("%+v, %d downloads", j, downloads.Load())
	}
	// nothing newer: it stays as it is, not downloaded again
	u.check()
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" || downloads.Load() != 1 {
		t.Fatalf("%+v, %d downloads", j, downloads.Load())
	}
	// the feed down: still ready to restart into what it has
	t.Setenv("MAGPIE_UPDATE_FEED", "http://127.0.0.1:1/")
	u.check()
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" {
		t.Fatalf("%+v", j)
	}
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	// 0.1.13 out: the restart's recheck leaves 0.1.11, with downloads on
	// a click too; a check asked for, downloads automatic, takes 0.1.13
	latest.Store("0.1.13")
	u.recheck(true)
	downloadsOnClick(t)
	u.recheck(true)
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" || downloads.Load() != 1 || u.staged == "" {
		t.Fatalf("the restart's recheck: %+v, %d downloads", j, downloads.Load())
	}
	s := settings.Load()
	s.NoAutoDownload = false
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	u.check()
	for u.json().State == "downloading" || u.json().State == "checking" {
		time.Sleep(10 * time.Millisecond)
	}
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.13" || downloads.Load() != 2 || u.staged == "" {
		t.Fatalf("%+v, %d downloads", j, downloads.Load())
	}
}

// The clock's check, with a version downloaded and waiting, downloads a
// newer one out since in its place: with downloads automatic, a version
// magpie knows is out is downloaded at once, not when Update is clicked
// (canlee886 on X). The feed asked again before a restart leaves the one
// waiting, which the restart goes into at once; with downloads on a click
// the clock fetches nothing more (iiiiiimone on X).
func TestUpdateClockFetchesNewerThanWaiting(t *testing.T) {
	sandboxHome(t)
	body := []byte("new magpie")
	sum := sha256.Sum256(body)
	var latest atomic.Value
	latest.Store("0.1.11")
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			downloads.Add(1)
			w.Write(body)
			return
		}
		json.NewEncoder(w).Encode(update.Release{Version: latest.Load().(string), Assets: map[string]update.Asset{
			update.BinaryAsset(): {URL: "http://" + r.Host + "/asset", SHA256: hex.EncodeToString(sum[:])},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	old := Version
	Version = "0.1.10"
	defer func() { Version = old }()
	exe, err := update.Executable()
	if err != nil {
		t.Fatal(err)
	}
	u := &updater{exe: exe}
	u.self, _ = os.Stat(exe)
	defer os.Remove(exe + ".new")

	// nothing waiting: the clock's check downloads what it finds
	u.checkByClock()
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" || downloads.Load() != 1 {
		t.Fatalf("%+v, %d downloads", j, downloads.Load())
	}
	// the same version again: nothing more is downloaded
	u.checkByClock()
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" || downloads.Load() != 1 {
		t.Fatalf("same version: %+v, %d downloads", j, downloads.Load())
	}
	// newer ones out, check after check: each is downloaded as it is found
	for i, v := range []string{"0.1.12", "0.1.13"} {
		latest.Store(v)
		u.checkByClock()
		if j := u.json(); j.State != "ready" || j.Latest != v || downloads.Load() != int32(i+2) || u.staged == "" {
			t.Fatalf("%s out: %+v, %d downloads", v, j, downloads.Load())
		}
	}
	// the restart's recheck: the one waiting is what it goes into
	latest.Store("0.1.14")
	u.recheck(true)
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.13" || downloads.Load() != 3 {
		t.Fatalf("recheck: %+v, %d downloads", j, downloads.Load())
	}
	// downloads on a click: the clock leaves the one waiting
	downloadsOnClick(t)
	u.checkByClock()
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.13" || downloads.Load() != 3 || u.staged == "" {
		t.Fatalf("on a click: %+v, %d downloads", j, downloads.Load())
	}
}

// A version found and not downloaded is fetched by the minute loop, not
// left for a click: once downloads are automatic again (found), and half an
// hour after a download failed (error with retry), not six hours later.
func TestUpdateFetchDue(t *testing.T) {
	now := time.Now()
	auto := settings.Settings{}
	for _, c := range []struct {
		name  string
		s     settings.Settings
		state string
		retry bool
		last  time.Time
		want  bool
	}{
		{"found", auto, "found", false, now, true},
		{"failed just now", auto, "error", true, now, false},
		{"failed half an hour ago", auto, "error", true, now.Add(-retryAfter), true},
		{"error with no retry", auto, "error", false, now.Add(-time.Hour), false},
		{"ready", auto, "ready", false, now.Add(-time.Hour), false},
		{"latest", auto, "latest", false, now.Add(-time.Hour), false},
		{"downloads on a click", settings.Settings{NoAutoDownload: true}, "found", false, now, false},
		{"never asks", settings.Settings{NoAutoUpdate: true}, "found", false, now, false},
	} {
		if got := fetchDue(c.s, c.state, c.retry, c.last, now); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// A check that comes while a download is under way doesn't start a second
// download beside it; once that one is in, the feed is asked again and a
// newer version found then is downloaded in its place.
func TestUpdateCheckDuringDownloadTakesNewer(t *testing.T) {
	sandboxHome(t)
	body := []byte("new magpie")
	sum := sha256.Sum256(body)
	var latest atomic.Value
	latest.Store("0.1.11")
	var downloads, inFlight, most atomic.Int32
	hold := make(chan struct{})
	started := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
			}
			if downloads.Add(1) == 1 {
				started <- struct{}{}
				<-hold
			}
			w.Write(body)
			return
		}
		json.NewEncoder(w).Encode(update.Release{Version: latest.Load().(string), Assets: map[string]update.Asset{
			update.BinaryAsset(): {URL: "http://" + r.Host + "/asset", SHA256: hex.EncodeToString(sum[:])},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	old := Version
	Version = "0.1.10"
	defer func() { Version = old }()
	exe, err := update.Executable()
	if err != nil {
		t.Fatal(err)
	}
	u := &updater{exe: exe}
	u.self, _ = os.Stat(exe)
	defer os.Remove(exe + ".new")

	go u.checkByClock()
	<-started
	if j := u.json(); j.State != "downloading" {
		t.Fatalf("first download: %+v", j)
	}
	// 0.1.12 comes out mid-download, and a check finds it
	latest.Store("0.1.12")
	u.check()
	if j := u.json(); j.State != "downloading" || downloads.Load() != 1 {
		t.Fatalf("check mid-download: %+v, %d downloads", j, downloads.Load())
	}
	close(hold)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if j := u.json(); j.State == "ready" && j.Latest == "0.1.12" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never took 0.1.12: %+v, %d downloads", u.json(), downloads.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	settled(t, u)
	if downloads.Load() != 2 || most.Load() != 1 {
		t.Fatalf("%d downloads, %d at once", downloads.Load(), most.Load())
	}
}

// A restart waiting for the gateway to go idle goes into the copy it is
// waiting with: a check meanwhile doesn't swap it for a download.
func TestUpdateWaitingRestartKeepsItsDownload(t *testing.T) {
	sandboxHome(t)
	body := []byte("new magpie")
	sum := sha256.Sum256(body)
	var latest atomic.Value
	latest.Store("0.1.11")
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			downloads.Add(1)
			w.Write(body)
			return
		}
		json.NewEncoder(w).Encode(update.Release{Version: latest.Load().(string), Assets: map[string]update.Asset{
			update.BinaryAsset(): {URL: "http://" + r.Host + "/asset", SHA256: hex.EncodeToString(sum[:])},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	old := Version
	Version = "0.1.10"
	defer func() { Version = old }()
	exe, err := update.Executable()
	if err != nil {
		t.Fatal(err)
	}
	u := &updater{exe: exe}
	u.self, _ = os.Stat(exe)
	defer os.Remove(exe + ".new")

	u.checkByClock()
	if j := u.json(); j.State != "ready" || downloads.Load() != 1 {
		t.Fatalf("%+v, %d downloads", j, downloads.Load())
	}
	u.mu.Lock()
	u.waiting = true
	u.mu.Unlock()
	latest.Store("0.1.12")
	u.checkByClock()
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" || downloads.Load() != 1 || u.staged == "" {
		t.Fatalf("waiting: %+v, %d downloads", j, downloads.Load())
	}
}

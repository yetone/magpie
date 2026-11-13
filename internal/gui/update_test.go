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

	"github.com/yetone/magpie/internal/update"
)

// A version downloaded and waiting for a restart gives way to a newer one
// out since, so the restart goes straight to the latest, with downloads on
// a click (with them automatic it is restarted into as it is:
// TestRestartTakesTheDownloadWaiting).
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
	// 0.1.13 out: that is the one it restarts into
	latest.Store("0.1.13")
	downloadsOnClick(t)
	u.recheck(true)
	for u.json().State == "downloading" || u.json().State == "checking" {
		time.Sleep(10 * time.Millisecond)
	}
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.13" || downloads.Load() != 2 || u.staged == "" {
		t.Fatalf("%+v, %d downloads", j, downloads.Load())
	}
}

// The clock's check, with a version downloaded and waiting, doesn't
// download a newer one out since: magpie releases many times a day, and
// each check downloaded the whole app again on the user's proxy
// (iiiiiimone on X). Nor does the feed asked again before a restart, with
// downloads automatic (canlee886 on X). A check asked for still fetches
// the newest.
func TestUpdateClockLeavesWaitingDownload(t *testing.T) {
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
	// newer ones out, check after check: the one waiting stays
	for _, v := range []string{"0.1.12", "0.1.13", "0.1.14"} {
		latest.Store(v)
		u.checkByClock()
		if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" || downloads.Load() != 1 || u.staged == "" {
			t.Fatalf("%s out: %+v, %d downloads", v, j, downloads.Load())
		}
	}
	// the restart's recheck: the one waiting is what it goes into
	u.recheck(true)
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.11" || downloads.Load() != 1 {
		t.Fatalf("recheck: %+v, %d downloads", j, downloads.Load())
	}
	// a check asked for takes the newest
	latest.Store("0.1.15")
	u.check()
	if j := u.json(); j.State != "ready" || j.Latest != "0.1.15" || downloads.Load() != 2 {
		t.Fatalf("check: %+v, %d downloads", j, downloads.Load())
	}
}

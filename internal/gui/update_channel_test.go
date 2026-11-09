package gui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// Settings' Update channel (ssharpcc on X). Every version found 0.1.12
// and downloaded it; Stable's version is 0.1.11. Turning Stable on asks
// the feed again on it at once, and what was downloaded from every
// version is dropped for Stable's, never restarted into. A save from a
// page that doesn't send the channel keeps it.
func TestUpdateChannelSwitch(t *testing.T) {
	sandboxHome(t)
	body := []byte("new magpie")
	sum := sha256.Sum256(body)
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			downloads.Add(1)
			w.Write(body)
			return
		}
		v := "0.1.12"
		if r.URL.Query().Get("channel") == "stable" {
			v = "0.1.11"
		}
		json.NewEncoder(w).Encode(update.Release{Version: v, Notes: "- faster", Assets: map[string]update.Asset{
			update.BinaryAsset(): {URL: "http://" + r.Host + "/asset", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	oldV := Version
	Version = "0.1.10"
	defer func() { Version = oldV }()
	exe, err := update.Executable()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(exe + ".new")

	o := updates
	u := &updater{exe: exe}
	u.self, _ = os.Stat(exe)
	updates = u
	defer func() { updates = o }()
	settle := func() updateJSON {
		t.Helper()
		for range 1000 {
			if j := u.json(); j.State != "checking" && j.State != "downloading" {
				return j
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("still %s", u.json().State)
		return updateJSON{}
	}
	save := func(in string) {
		t.Helper()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(in)))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", in, rec.Code, rec.Body)
		}
	}

	// every version: 0.1.12, downloaded
	u.check()
	if j := settle(); j.State != "ready" || j.Latest != "0.1.12" || downloads.Load() != 1 {
		t.Fatalf("every version: %+v, %d downloads", j, downloads.Load())
	}

	// Stable: asked again at once, 0.1.12's download dropped for 0.1.11's
	save(`{"theme":"light","updateChannel":"stable"}`)
	if settings.Load().UpdateChannel != settings.UpdateStable {
		t.Fatal("stable wasn't saved")
	}
	for range 500 {
		if downloads.Load() == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if j := settle(); j.State != "ready" || j.Latest != "0.1.11" || downloads.Load() != 2 {
		t.Fatalf("on stable: %+v, %d downloads", j, downloads.Load())
	}
	u.mu.Lock()
	staged, latest := u.staged, u.latest.Version
	u.mu.Unlock()
	if staged == "" || latest != "0.1.11" {
		t.Fatalf("staged %q for %s", staged, latest)
	}

	// a save without the channel keeps it
	save(`{"theme":"dark"}`)
	if got := settings.Load().UpdateChannel; got != settings.UpdateStable {
		t.Fatalf("a save without updateChannel set it to %q", got)
	}
	// a check on Stable keeps the download
	u.check()
	if j := settle(); j.State != "ready" || j.Latest != "0.1.11" || downloads.Load() != 2 {
		t.Fatalf("checked again on stable: %+v, %d downloads", j, downloads.Load())
	}
	// a value the channel doesn't have is refused
	rec := httptest.NewRecorder()
	Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("POST", "/api/settings", strings.NewReader(`{"updateChannel":"beta"}`)))
	if rec.Code == 200 || settings.Load().UpdateChannel != settings.UpdateStable {
		t.Fatalf("beta: %d, saved %q", rec.Code, settings.Load().UpdateChannel)
	}
}

// What's new's notes are kept by language and channel: Stable shows every
// release since the one installed, every version at most 40, and the list
// asked on one isn't the other's.
func TestWhatsNewKeyedByChannel(t *testing.T) {
	sandboxHome(t)
	if got := notesKey("zh"); got != "zh" {
		t.Fatalf("every version: %q", got)
	}
	s := settings.Load()
	s.UpdateChannel = settings.UpdateStable
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if got := notesKey("zh"); got != "zh|stable" {
		t.Fatalf("stable: %q", got)
	}
	t.Setenv("MAGPIE_NOTES_FEED", "http://127.0.0.1:1/") // never the real one
	oldV := Version
	Version = "0.1.100"
	defer func() { Version = oldV }()
	var list []update.Note
	for i := range 100 {
		list = append(list, update.Note{Version: fmt.Sprintf("0.1.%d", 100-i), Notes: "- x"})
	}
	waiting := updateJSON{State: "available", Latest: "0.1.200", Notes: "- the next one"}
	if got := withWaiting(t.Context(), list, waiting, "en"); len(got) != 101 || got[0].Version != "0.1.200" {
		t.Fatalf("stable keeps %d of 101", len(got))
	}
	s.UpdateChannel = ""
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if got := withWaiting(t.Context(), list, waiting, "en"); len(got) != update.MaxNotes {
		t.Fatalf("every version keeps %d", len(got))
	}
}

// The feed behind on the same channel (littleHong on Discord). Short of
// GitHub's API the site answers from GitHub's latest redirect or the
// last answer it kept, which can be older than the version the app found
// and downloaded. Restart to update asks the feed first; such an answer
// dropped 0.1.12's download, so nothing was restarted into and the app
// stayed on 0.1.10. The version found stands, download and all.
func TestUpdateFeedBehindKeepsDownload(t *testing.T) {
	sandboxHome(t)
	body := []byte("new magpie")
	sum := sha256.Sum256(body)
	var downloads atomic.Int32
	var behind atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/asset" {
			downloads.Add(1)
			w.Write(body)
			return
		}
		v := "0.1.12"
		if behind.Load() {
			v = "0.1.11"
		}
		json.NewEncoder(w).Encode(update.Release{Version: v, Assets: map[string]update.Asset{
			update.BinaryAsset(): {URL: "http://" + r.Host + "/asset", Size: int64(len(body)), SHA256: hex.EncodeToString(sum[:])},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	oldV := Version
	Version = "0.1.10"
	defer func() { Version = oldV }()
	exe, err := update.Executable()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(exe + ".new")

	o := updates
	u := &updater{exe: exe}
	u.self, _ = os.Stat(exe)
	updates = u
	defer func() { updates = o }()
	settle := func() updateJSON {
		t.Helper()
		for range 1000 {
			if j := u.json(); j.State != "checking" && j.State != "downloading" {
				return j
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("still %s", u.json().State)
		return updateJSON{}
	}

	u.check()
	if j := settle(); j.State != "ready" || j.Latest != "0.1.12" || downloads.Load() != 1 {
		t.Fatalf("found: %+v, %d downloads", j, downloads.Load())
	}
	// Restart to update asks the feed again, and it is a release behind
	behind.Store(true)
	u.recheck(true)
	j := settle()
	u.mu.Lock()
	staged := u.staged
	u.mu.Unlock()
	if j.State != "ready" || j.Latest != "0.1.12" || staged == "" || downloads.Load() != 1 {
		t.Fatalf("feed behind: %+v, staged %q, %d downloads", j, staged, downloads.Load())
	}
}

// Stable's "up to date" names the newest release out (ylspcm on X). The
// app from the site, installed by hand, is newer than Stable's version,
// so every check said up to date while newer releases were out, and it
// read as the check being wrong. On Stable, with nothing newer there, the
// Version row is told the channel and the newest version on every version;
// on every version, and on Stable once this one is the newest, nothing.
func TestStableUpToDateNamesNewest(t *testing.T) {
	sandboxHome(t)
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		v := "0.1.13"
		if r.URL.Query().Get("channel") == "stable" {
			v = "0.1.11"
		}
		json.NewEncoder(w).Encode(update.Release{Version: v})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", srv.URL)
	oldV := Version
	defer func() { Version = oldV }()
	o := updates
	defer func() { updates = o }()
	channel := func(c string) {
		t.Helper()
		s := settings.Load()
		s.UpdateChannel = c
		if err := settings.Save(s); err != nil {
			t.Fatal(err)
		}
	}
	// what the Version row reads, as the page reads it
	row := func() map[string]any {
		t.Helper()
		updates = &updater{}
		updates.check()
		rec := httptest.NewRecorder()
		Handler(nil, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/api/update?fresh=1", nil))
		var j map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &j); err != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return j
	}

	channel(settings.UpdateStable)
	Version = "0.1.12" // installed from the site, ahead of Stable's 0.1.11
	if j := row(); j["state"] != "latest" || j["channel"] != "stable" || j["newest"] != "0.1.13" {
		t.Fatalf("stable, 0.1.13 out: %v", j)
	}
	Version = "0.1.13"
	if j := row(); j["state"] != "latest" || j["channel"] != "stable" || j["newest"] != nil {
		t.Fatalf("stable, the newest installed: %v", j)
	}
	channel("")
	before := asked.Load()
	if j := row(); j["state"] != "latest" || j["channel"] != nil || j["newest"] != nil || asked.Load()-before != 1 {
		t.Fatalf("every version: %v, asked %d times", j, asked.Load()-before)
	}
}

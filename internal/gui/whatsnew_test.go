package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/update"
)

// The version last run is kept in magpie's folder, and the notes since are
// shown once after an upgrade: not on a fresh install, nor offline.
func TestWhatsNewAfterUpgrade(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "magpie")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"releases": []update.Note{
			{Version: "0.1.604", Notes: "- four (#464)\n\n### Install\n\nlinks"},
			{Version: "0.1.603", Notes: "- three"},
			{Version: "0.1.600", Notes: "- zero"},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_NOTES_FEED", srv.URL)
	t.Setenv("MAGPIE_UPDATE_FEED", "http://127.0.0.1:1/")
	old := Version
	defer func() { Version = old }()
	last := func() string {
		b, _ := os.ReadFile(filepath.Join(dir, lastRunFile))
		return strings.TrimSpace(string(b))
	}
	run := func(v string) (*whatsNew, whatsNewJSON) {
		Version = v
		n := &whatsNew{}
		n.start()
		return n, n.get(context.Background(), false, "en")
	}

	// a fresh install: nothing shown, the version kept
	if _, j := run("0.1.600"); j.Show || len(j.Releases) != 0 {
		t.Fatalf("fresh install: %+v", j)
	}
	if last() != "0.1.600" {
		t.Fatalf("kept %q", last())
	}
	// the same again: nothing
	if _, j := run("0.1.600"); j.Show {
		t.Fatalf("same version: %+v", j)
	}
	// upgraded: what came since, newest first, without Install
	n, j := run("0.1.604")
	if !j.Show || len(j.Releases) != 2 || j.Releases[0].Version != "0.1.604" || j.Releases[1].Version != "0.1.603" || j.Releases[0].Notes != "- four (#464)" {
		t.Fatalf("upgrade: %+v", j)
	}
	if last() != "0.1.604" {
		t.Fatalf("kept %q", last())
	}
	// shown once; Settings still opens them
	n.seen()
	if j := n.get(context.Background(), false, "en"); j.Show || len(j.Releases) != 0 {
		t.Fatalf("after seen: %+v", j)
	}
	if j := n.get(context.Background(), true, "en"); j.Show || len(j.Releases) != 2 {
		t.Fatalf("asked again: %+v", j)
	}
	// a downgrade: nothing
	if _, j := run("0.1.603"); j.Show {
		t.Fatalf("downgrade: %+v", j)
	}
	// a build from source leaves the release it came after
	if _, j := run("dev"); j.Show || last() != "0.1.603" {
		t.Fatalf("dev: %+v, kept %q", j, last())
	}
	// offline: nothing shown, nothing in the way
	srv.Close()
	if _, j := run("0.1.604"); j.Show || len(j.Releases) != 0 {
		t.Fatalf("offline: %+v", j)
	}
}

// The waiting update's notes, shown before it is installed, leave out the
// download links.
func TestUpdateNotesWithoutInstall(t *testing.T) {
	u := &updater{state: "ready", latest: &update.Release{Version: "0.1.605", Notes: "## New Features\n\n- A thing. (#470)\n\n### Install\n\nDownload it."}}
	if j := u.json(); j.Notes != "## New Features\n\n- A thing. (#470)" {
		t.Fatalf("%q", j.Notes)
	}
}

// A magpie from before the version was kept, already in use, shows its own
// version's notes; the site without the list yet leaves the update feed's.
func TestWhatsNewFromBefore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "magpie")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{}"), 0o644)
	os.Chtimes(filepath.Join(dir, "settings.json"), started.Add(-time.Hour), started.Add(-time.Hour))
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(update.Release{Version: "0.1.604", Notes: "## Bug Fixes\n\n- four"})
	}))
	defer feed.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", feed.URL)
	t.Setenv("MAGPIE_NOTES_FEED", feed.URL+"/404")
	old := Version
	defer func() { Version = old }()
	Version = "0.1.604"
	n := &whatsNew{}
	n.start()
	if j := n.get(context.Background(), false, "en"); !j.Show || len(j.Releases) != 1 || j.Releases[0].Notes != "## Bug Fixes\n\n- four" {
		t.Fatalf("%+v", j)
	}
}

// #525: the dialog comes up by itself on the first start of an update the
// user didn't ask for from magpie; after "Restart to update" its notes wait
// in Settings, and "Don't show again today" keeps it quiet for the day.
func TestWhatsNewQuietAfterRestartToUpdate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	dir := filepath.Join(home, "magpie")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"releases": []update.Note{
			{Version: "0.1.606", Notes: "- six"},
			{Version: "0.1.605", Notes: "- five"},
			{Version: "0.1.604", Notes: "- four"},
		}})
	}))
	defer srv.Close()
	t.Setenv("MAGPIE_NOTES_FEED", srv.URL)
	t.Setenv("MAGPIE_UPDATE_FEED", "http://127.0.0.1:1/")
	oldV, oldToday := Version, today
	defer func() { Version, today = oldV, oldToday }()
	day := "2026-10-02"
	today = func() string { return day }
	run := func(v string) (*whatsNew, whatsNewJSON) {
		Version = v
		n := &whatsNew{}
		n.start()
		return n, n.get(context.Background(), false, "en")
	}
	run("0.1.604")

	// "Restart to update" into 0.1.605: the restart notes it...
	updates.mu.Lock()
	oExe, oSelf, oLatest, oStaged := updates.exe, updates.self, updates.latest, updates.staged
	updates.mu.Unlock()
	defer func() {
		updates.mu.Lock()
		updates.exe, updates.self, updates.latest, updates.staged = oExe, oSelf, oLatest, oStaged
		updates.mu.Unlock()
	}()
	exe := filepath.Join(t.TempDir(), "magpie")
	os.WriteFile(exe, []byte("old"), 0o755)
	self, _ := os.Stat(exe)
	os.WriteFile(exe, []byte("newer"), 0o755) // in already: install has nothing to swap
	updates.mu.Lock()
	updates.exe, updates.self, updates.latest = exe, self, &update.Release{Version: "0.1.605"}
	updates.mu.Unlock()
	if !restartToUpdate(true, false, "") {
		t.Fatal("restart to update failed")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, updatedInAppFile)); strings.TrimSpace(string(b)) != "0.1.605" {
		t.Fatalf("noted %q", b)
	}
	// ...and its first start shows nothing by itself, but Settings has them
	n, j := run("0.1.605")
	if j.Show || len(j.Releases) != 0 {
		t.Fatalf("after restart to update: %+v", j)
	}
	if j := n.get(context.Background(), true, "en"); j.Show || len(j.Releases) != 1 || j.Releases[0].Version != "0.1.605" {
		t.Fatalf("Settings after restart to update: %+v", j)
	}
	if _, err := os.Stat(filepath.Join(dir, updatedInAppFile)); !os.IsNotExist(err) {
		t.Fatalf("the note is kept: %v", err)
	}

	// an update installed some other way (on quitting, by hand): shown
	_, j = run("0.1.606")
	if !j.Show || len(j.Releases) != 1 {
		t.Fatalf("after an update from outside: %+v", j)
	}

	// a note for another version is no reason to keep quiet
	run("0.1.604")
	updatedInApp("0.1.605")
	if _, j := run("0.1.606"); !j.Show {
		t.Fatalf("another version's note: %+v", j)
	}

	// "Don't show again today": nothing by itself today, Settings still has them
	run("0.1.604")
	if err := setQuietToday(true); err != nil {
		t.Fatal(err)
	}
	n, j = run("0.1.606")
	if j.Show || len(j.Releases) != 0 {
		t.Fatalf("quiet today: %+v", j)
	}
	if j := n.get(context.Background(), true, "en"); len(j.Releases) != 2 {
		t.Fatalf("Settings on a quiet day: %+v", j)
	}
	// the next day it shows again
	day = "2026-10-03"
	if j := n.get(context.Background(), false, "en"); !j.Show {
		t.Fatalf("next day: %+v", j)
	}
	// unticked
	day = "2026-10-02"
	setQuietToday(false)
	if j := n.get(context.Background(), false, "en"); !j.Show {
		t.Fatalf("unticked: %+v", j)
	}
}

// The dialog's checkbox posts to whatsnew/today.
func TestWhatsNewTodayRoute(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	// one day for the post and the check, so a run across midnight agrees
	oldToday := today
	t.Cleanup(func() { today = oldToday })
	today = func() string { return "2026-09-30" }
	mux := http.NewServeMux()
	whatsNewRoutes(mux)
	post := func(body string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/whatsnew/today", strings.NewReader(body)))
		return rec.Code
	}
	if c := post(`{"on":true}`); c != http.StatusNoContent || !quietToday() {
		t.Fatalf("on: %d %v", c, quietToday())
	}
	if c := post(`{"on":false}`); c != http.StatusNoContent || quietToday() {
		t.Fatalf("off: %d %v", c, quietToday())
	}
	if c := post(`nope`); c != http.StatusBadRequest {
		t.Fatalf("bad body: %d", c)
	}
}

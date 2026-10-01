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
		return n, n.get(context.Background(), false)
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
	if j := n.get(context.Background(), false); j.Show || len(j.Releases) != 0 {
		t.Fatalf("after seen: %+v", j)
	}
	if j := n.get(context.Background(), true); j.Show || len(j.Releases) != 2 {
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
	if j := n.get(context.Background(), false); !j.Show || len(j.Releases) != 1 || j.Releases[0].Notes != "## Bug Fixes\n\n- four" {
		t.Fatalf("%+v", j)
	}
}

package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/update"
)

// Before an update is put in, the page asks what changed (Hu9956, #844):
// every release after this one up to the update's, newest first, without
// their Install sections; the site's list out of reach, the update's own
// notes; no update, none.
func TestUpdateNotesRoute(t *testing.T) {
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") != "0.1.604" || r.URL.Query().Get("upto") != "0.1.607" {
			t.Errorf("asked %s", r.URL.RawQuery)
		}
		json.NewEncoder(w).Encode(map[string]any{"releases": []update.Note{
			{Version: "0.1.607", Notes: "- seven\n\n### Install\n\nlinks"},
			{Version: "0.1.606", Notes: "- six"},
			{Version: "0.1.605", Notes: "- five"},
			{Version: "0.1.604", Notes: "- four"},
		}})
	}))
	defer feed.Close()
	t.Setenv("MAGPIE_NOTES_FEED", feed.URL)
	t.Setenv("MAGPIE_UPDATE_FEED", "http://127.0.0.1:1/")
	oldV, oldState, oldLatest, oldIn := Version, updates.state, updates.latest, updates.notesIn
	defer func() { Version, updates.state, updates.latest, updates.notesIn = oldV, oldState, oldLatest, oldIn }()
	Version = "0.1.604"
	updates.mu.Lock()
	updates.state, updates.latest, updates.notesIn = "ready", &update.Release{Version: "0.1.607", Notes: "- seven only"}, "en"
	updates.mu.Unlock()
	mux := http.NewServeMux()
	updateRoutes(mux, nil)
	get := func() (out struct {
		Releases []update.Note `json:"releases"`
		Error    string        `json:"error"`
	}) {
		rw := httptest.NewRecorder()
		mux.ServeHTTP(rw, httptest.NewRequest("GET", "/api/update/notes?lang=en", nil))
		if rw.Code != 200 {
			t.Fatalf("%d %s", rw.Code, rw.Body)
		}
		json.Unmarshal(rw.Body.Bytes(), &out)
		return out
	}
	j := get()
	var vs []string
	for _, n := range j.Releases {
		vs = append(vs, n.Version)
	}
	if len(vs) != 3 || vs[0] != "0.1.607" || vs[2] != "0.1.605" || j.Releases[0].Notes != "- seven" {
		t.Fatalf("between: %+v", j)
	}

	// the site's list out of reach: the update's own notes
	t.Setenv("MAGPIE_NOTES_FEED", "http://127.0.0.1:1/")
	if j := get(); len(j.Releases) != 1 || j.Releases[0].Version != "0.1.607" || j.Releases[0].Notes != "- seven only" {
		t.Fatalf("fallback: %+v", j)
	}

	// no update: no notes
	updates.mu.Lock()
	updates.state, updates.latest = "latest", nil
	updates.mu.Unlock()
	if j := get(); len(j.Releases) != 0 || j.Error != "" {
		t.Fatalf("none: %+v", j)
	}
}

// The dialog asks the feed again when it was last asked a while ago (#910,
// Moody-Sin: it stopped at v0.1.1020, downloaded hours before, with
// v0.1.1024 out). With downloads automatic the version downloaded is what
// the restart goes into at once (canlee886 on X), so that is the one it
// names, with the notes of every release up to it; the new version's
// first check takes v0.1.1024.
func TestUpdateNotesAskTheFeedAgain(t *testing.T) {
	sandboxHome(t)
	feedAsked := 0
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feedAsked++
		json.NewEncoder(w).Encode(update.Release{Version: "0.1.1024", Notes: "- twenty-four"})
	}))
	defer feed.Close()
	notes := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upto := r.URL.Query().Get("upto")
		all := []update.Note{{Version: "0.1.1024", Notes: "- twenty-four"}, {Version: "0.1.1020", Notes: "- twenty"}}
		if upto == "0.1.1020" {
			all = all[1:]
		}
		json.NewEncoder(w).Encode(map[string]any{"releases": all})
	}))
	defer notes.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", feed.URL)
	t.Setenv("MAGPIE_NOTES_FEED", notes.URL)
	updates.mu.Lock()
	oldState, oldStaged, oldIn, oldLatest, oldAsked, oldV := updates.state, updates.staged, updates.notesIn, updates.latest, updates.asked, Version
	Version = "0.1.1010"
	// downloaded hours ago
	updates.state, updates.staged, updates.notesIn = "ready", filepath.Join(t.TempDir(), "staged"), "en"
	updates.latest, updates.asked = &update.Release{Version: "0.1.1020", Notes: "- twenty"}, time.Now().Add(-3*time.Hour)
	updates.mu.Unlock()
	defer func() {
		updates.mu.Lock()
		updates.state, updates.staged, updates.notesIn, updates.latest, updates.asked, Version = oldState, oldStaged, oldIn, oldLatest, oldAsked, oldV
		updates.mu.Unlock()
	}()
	mux := http.NewServeMux()
	updateRoutes(mux, nil)
	get := func() (out struct {
		Latest   string        `json:"latest"`
		Releases []update.Note `json:"releases"`
	}) {
		rw := httptest.NewRecorder()
		mux.ServeHTTP(rw, httptest.NewRequest("GET", "/api/update/notes?lang=en", nil))
		json.Unmarshal(rw.Body.Bytes(), &out)
		return out
	}
	if j := get(); j.Latest != "0.1.1020" || feedAsked != 1 || len(j.Releases) != 1 || j.Releases[0].Version != "0.1.1020" {
		t.Fatalf("stale: %+v, feed asked %d", j, feedAsked)
	}

	// asked a moment ago: not again
	updates.mu.Lock()
	updates.state, updates.staged = "ready", filepath.Join(t.TempDir(), "staged")
	updates.latest, updates.asked = &update.Release{Version: "0.1.1020", Notes: "- twenty"}, time.Now()
	updates.mu.Unlock()
	feedAsked = 0
	if j := get(); j.Latest != "0.1.1020" || feedAsked != 0 || !strings.Contains(j.Releases[0].Notes, "twenty") {
		t.Fatalf("fresh: %+v, feed asked %d", j, feedAsked)
	}
}

// A release found before the site had its notes (#649, Sun1090: v0.1.1224's
// dialog said it had none) takes them from the next check that has them,
// though the version is the one already downloaded; notes held are kept.
func TestUpdateRecheckTakesNotesLate(t *testing.T) {
	notes := "## Features\n- twenty-four"
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(update.Release{Version: "0.1.1224", Notes: notes})
	}))
	defer feed.Close()
	t.Setenv("MAGPIE_UPDATE_FEED", feed.URL)
	updates.mu.Lock()
	oldState, oldStaged, oldIn, oldLatest, oldAsked, oldLang, oldV := updates.state, updates.staged, updates.notesIn, updates.latest, updates.asked, updates.lang, Version
	Version = "0.1.1223"
	updates.lang = "en"
	updates.mu.Unlock()
	defer func() {
		updates.mu.Lock()
		updates.state, updates.staged, updates.notesIn, updates.latest, updates.asked, updates.lang, Version = oldState, oldStaged, oldIn, oldLatest, oldAsked, oldLang, oldV
		updates.mu.Unlock()
	}()
	hold := func(held string) {
		updates.mu.Lock()
		updates.state, updates.staged, updates.notesIn = "ready", filepath.Join(t.TempDir(), "staged"), "en"
		updates.latest = &update.Release{Version: "0.1.1224", Notes: held, URL: "https://example.invalid/v0.1.1224"}
		updates.mu.Unlock()
	}
	// the dialog's look at a stale feed, the version downloaded the newest
	hold("")
	updates.freshen()
	if j := updates.json(); j.State != "ready" || j.Latest != "0.1.1224" || !strings.Contains(j.Notes, "twenty-four") || j.URL == "" {
		t.Fatalf("empty notes not taken from the recheck: %+v", j)
	}

	hold("- held")
	updates.freshen()
	if j := updates.json(); j.Notes != "- held" {
		t.Fatalf("held notes replaced: %+v", j)
	}
}

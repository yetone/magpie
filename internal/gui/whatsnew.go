package gui

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// whatsNew shows what changed once magpie runs a newer release than it last
// did (a Discord user: to see whether their issue was fixed). The version it
// last ran is kept in magpie's own folder; as it starts on a newer one, the
// notes of every release since are asked of the site (where the update feed
// is) the first time a window or magpie web's page asks, and shown once.
// Offline, nothing is shown. Settings can open them again. The notes are
// in the page's language, Chinese or English, where a release has them in
// it (freecss on Discord: they should follow Settings' language).
//
// The dialog comes up by itself only on the first start of a version the
// user didn't ask for from inside magpie (#525: releases come many a day,
// and a dialog after every "Restart to update" is in the way): a restart
// into an update leaves a note of the version it installs, and that version
// starting finds it and keeps quiet; the notes are still a click away in
// Settings. The dialog's "Don't show again today" keeps it quiet for the
// rest of the local day.
type whatsNew struct {
	mu      sync.Mutex
	after   string                   // the version last run, "" for the current one's notes alone
	pending bool                     // an upgrade whose notes are still to be shown
	quiet   bool                     // the upgrade was the user's own restart to update: not shown by itself
	shown   bool                     // a page has shown them
	notes   map[string][]update.Note // by language
}

var news = &whatsNew{}

// started is when this process began, to tell magpie's folder from before
// it apart from one this run just made.
var started = time.Now()

const lastRunFile = "last-version"

// updatedInAppFile holds the version a restart to update installed, for
// that version's first start to keep its notes to Settings (#525).
const updatedInAppFile = "updated-in-app"

// quietDayFile holds the local day ("2006-01-02") the dialog's "Don't show
// again today" was ticked on.
const quietDayFile = "whatsnew-quiet-day"

// today is the local day, a variable for tests.
var today = func() string { return time.Now().Format("2006-01-02") }

// updatedInApp notes that a restart to update is installing version.
func updatedInApp(version string) {
	if version == "" {
		return
	}
	dir := settings.Dir()
	if err := os.MkdirAll(dir, 0o755); err == nil {
		err = os.WriteFile(filepath.Join(dir, updatedInAppFile), []byte(version+"\n"), 0o644)
		if err != nil {
			log.Println("updated in app:", err)
		}
	}
}

// quietToday is whether "Don't show again today" was ticked today.
func quietToday() bool {
	b, err := os.ReadFile(filepath.Join(settings.Dir(), quietDayFile))
	return err == nil && strings.TrimSpace(string(b)) == today()
}

// setQuietToday ticks (or unticks) "Don't show again today".
func setQuietToday(on bool) error {
	f := filepath.Join(settings.Dir(), quietDayFile)
	if !on {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		return err
	}
	return os.WriteFile(f, []byte(today()+"\n"), 0o644)
}

// start reads the version last run and puts this one in its place.
func (n *whatsNew) start() {
	dir := settings.Dir()
	b, _ := os.ReadFile(filepath.Join(dir, lastRunFile))
	last := strings.TrimSpace(string(b))
	after, show := update.ShowNotes(last, Version, last == "" && usedBefore(dir))
	// a restart to update into this version: its notes wait in Settings
	mark := filepath.Join(dir, updatedInAppFile)
	m, err := os.ReadFile(mark)
	inApp := err == nil && strings.TrimSpace(string(m)) == Version
	if err == nil && (inApp || update.Released(Version)) {
		os.Remove(mark) // read once; another version's is stale
	}
	n.mu.Lock()
	n.after, n.pending, n.quiet = after, show, inApp
	n.mu.Unlock()
	if !update.Released(Version) || last == Version {
		return // a build from source leaves the release it came after
	}
	if err := os.MkdirAll(dir, 0o755); err == nil {
		err = os.WriteFile(filepath.Join(dir, lastRunFile), []byte(Version+"\n"), 0o644)
		if err != nil {
			log.Println("last version:", err)
		}
	}
}

// usedBefore is whether magpie's folder held its files before this run: a
// magpie from before the version was kept, not a fresh install.
func usedBefore(dir string) bool {
	for _, f := range []string{"settings.json", "providers.json"} {
		if fi, err := os.Stat(filepath.Join(dir, f)); err == nil && fi.ModTime().Before(started) {
			return true
		}
	}
	return false
}

type whatsNewJSON struct {
	Show     bool          `json:"show"`
	Current  string        `json:"current"`
	Releases []update.Note `json:"releases"`
	// Error: the notes couldn't be had (the site down, or GitHub failing
	// it), as opposed to releases that have none (#661); URL is the
	// current version's release page, where they can be read instead
	Error string `json:"error,omitempty"`
	URL   string `json:"url,omitempty"`
}

// get is what a page shows: after an upgrade, the notes since, once (a
// page showing them says so with seen); asked again (all, from Settings),
// those or the current version's alone.
func (n *whatsNew) get(ctx context.Context, all bool, lang string) whatsNewJSON {
	n.mu.Lock()
	after, pending, shown := n.after, n.pending, n.shown
	if !pending {
		after = ""
	}
	notes := n.notes[lang]
	// shown by itself: not after a restart to update, nor on a day it was
	// asked not to be
	popup := pending && !shown && !n.quiet && !quietToday()
	n.mu.Unlock()
	j := whatsNewJSON{Current: Version, Releases: []update.Note{}}
	if (!popup && !all) || !update.Released(Version) {
		return j
	}
	j.URL = update.ReleasePage(Version)
	if notes == nil {
		var err error
		notes, err = fetchNotes(ctx, after, Version, lang)
		if err != nil {
			j.Error = err.Error()
		}
		if len(notes) > 0 { // kept: they don't change while magpie runs
			n.mu.Lock()
			if n.notes == nil {
				n.notes = map[string][]update.Note{}
			}
			n.notes[lang] = notes
			n.mu.Unlock()
		}
	}
	if len(notes) > 0 {
		j.Releases = notes
	}
	j.Show = popup && len(j.Releases) > 0
	return j
}

func (n *whatsNew) seen() {
	n.mu.Lock()
	n.shown = true
	n.mu.Unlock()
}

// fetchNotes asks the site for the notes since after; a site without the
// list yet, or one that can't be reached, leaves the update feed, whose
// newest release has its notes, for when that is the current one. The
// error says the notes couldn't be had, which the page tells apart from
// releases without notes (#661): the list failed and the feed had none.
func fetchNotes(ctx context.Context, after, upto, lang string) ([]update.Note, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	notes, failed := update.NotesBetween(ctx, after, upto, lang)
	if failed == nil && len(notes) > 0 {
		return notes, nil
	}
	if failed != nil {
		log.Println("release notes:", failed)
	}
	rel, err := update.LatestIn(ctx, lang)
	if err != nil {
		return nil, failed
	}
	notes = update.Between([]update.Note{{Version: rel.Version, Notes: rel.Notes, URL: rel.URL}}, after, upto)
	if len(notes) == 0 {
		return nil, failed
	}
	return notes, nil
}

// pageLang is the language a page asks in, its own (?lang=, zh or en), or
// without one the app's, as the tray menu has it, in the languages release
// notes come in.
func pageLang(r *http.Request) string {
	if l := askedLang(r); l != "" {
		return l
	}
	return notesLang(trayLang(settings.Load().Lang, systemLang))
}

// askedLang is the language a page names (?lang=): zh for any Chinese, en
// for anything else, "" for none.
func askedLang(r *http.Request) string {
	switch l := strings.ToLower(r.URL.Query().Get("lang")); {
	case strings.HasPrefix(l, "zh"):
		return "zh"
	case l != "":
		return "en"
	}
	return ""
}

func whatsNewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/whatsnew", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, news.get(r.Context(), r.URL.Query().Get("all") != "", pageLang(r)))
	})
	mux.HandleFunc("POST /api/whatsnew/seen", func(rw http.ResponseWriter, r *http.Request) {
		news.seen()
		rw.WriteHeader(http.StatusNoContent)
	})
	// today: the dialog's "Don't show again today", ticked or unticked
	mux.HandleFunc("POST /api/whatsnew/today", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			On bool `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		if err := setQuietToday(in.On); err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		rw.WriteHeader(http.StatusNoContent)
	})
}

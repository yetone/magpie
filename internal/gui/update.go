package gui

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// updater keeps the app current. It asks the feed at start-up and every six
// hours, or as often as Settings says, or only when asked (#472); when a newer release is out and the app may replace itself — the
// bundle on a Mac, the binary elsewhere — the new one is downloaded
// straight away, so all that is left is a restart. Quitting installs it too, and the next launch is
// the new version. Where the app's folder isn't the user's to change, the
// restart asks for the administrator's password; only an app that can't be
// replaced at all (run from its disk image, say) is sent to the release page.
type updater struct {
	mu      sync.Mutex
	state   string // checking | latest | downloading | ready | available | source | error
	latest  *update.Release
	err     string
	bundle  string      // the .app to replace, "" when not in one or stuck
	stuck   string      // why the .app can't be replaced where it is (update.Stuck)
	exe     string      // off the Mac: the binary to replace, "" when not writable
	self    os.FileInfo // exe as this process started from it
	retry   bool        // error: the download failed, and may be tried again
	staged  string
	asked   time.Time // when the feed was last asked, by the clock or a click
	done    int64     // downloading: bytes so far, of total (0 when unknown)
	total   int64
	onReady func(version string)
	// the waiting update's notes follow the pages' language (freecss on
	// Discord): the one they last asked in, else the app's
	lang     string    // the pages' language, "" before one asked
	notesIn  string    // the language latest's notes are in
	relangAt time.Time // when they were last asked for in another
	// A restart asked for while the gateway is busy waits until it is idle
	// (#577): waiting since waitFrom, then whenIdle restarts, unless it ran
	// waitMost first (gaveUp). onWait tells the tray what it waits on.
	waiting  bool
	waitFrom time.Time
	waitGen  int
	gaveUp   bool
	whenIdle func() bool
	onWait   func()
	loops    sync.WaitGroup // the waits going on
}

type updateJSON struct {
	State   string `json:"state"`
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
	Notes   string `json:"notes,omitempty"`
	URL     string `json:"url,omitempty"`
	Stuck   string `json:"stuck,omitempty"` // available: why it can't update itself
	Retry   bool   `json:"retry,omitempty"` // error: a click downloads it again
	Error   string `json:"error,omitempty"`
	Done    int64  `json:"done,omitempty"` // downloading: bytes so far
	Total   int64  `json:"total,omitempty"`
	// ready: what the gateway this process serves has in flight, which a
	// restart would cut short; Waiting, the restart waits for it to end;
	// GaveUp, the last wait ran out with it still busy
	Busy    *gateway.Busy `json:"busy,omitempty"`
	Waiting bool          `json:"waiting,omitempty"`
	GaveUp  bool          `json:"gaveUp,omitempty"`
}

var updates = &updater{}

// updateDue is whether the feed is asked by itself now, the last time it
// was asked at last: never with automatic updates off, otherwise once the
// settings' interval is up. The settings are read again each minute, so
// one changed (in Settings or the CLI) counts from the last check.
func updateDue(s settings.Settings, last, now time.Time) bool {
	return !s.NoAutoUpdate && now.Sub(last) >= time.Duration(s.UpdateEvery)*time.Minute
}

func (u *updater) start() {
	if b := update.Bundle(); b != "" {
		if u.stuck = update.Stuck(b); u.stuck == "" {
			u.bundle = b
		}
	} else if runtime.GOOS != "darwin" {
		if exe, err := update.Executable(); err == nil && (update.Writable(filepath.Dir(exe)) || update.CanElevate()) {
			u.exe = exe
			u.self, _ = os.Stat(exe)
			update.RemoveOld(exe)      // what the last updates on Windows moved aside
			update.RemoveStaleNew(exe) // what a magpie left running downloaded again
		}
	}
	go func() {
		time.Sleep(5 * time.Second) // let the app settle first
		for {
			u.mu.Lock()
			last := u.asked
			u.mu.Unlock()
			if updateDue(settings.Load(), last, time.Now()) {
				u.check()
			}
			time.Sleep(time.Minute)
		}
	}()
}

// check asks the feed and, when it can, stages the new version.
func (u *updater) check() {
	if u.begin() {
		u.run()
	}
}

// recheck asks the feed again before a restart into what was downloaded,
// so a newer version out since is the one restarted into. It waits for
// the feed a little while; a newer version is then downloading.
func (u *updater) recheck() {
	if u.begin() {
		go u.run()
	}
	for range 50 {
		u.mu.Lock()
		s := u.state
		u.mu.Unlock()
		if s != "checking" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// begin marks a check as under way, unless one is. One with a version
// already downloaded checks too: a newer one out since takes its place,
// so the restart goes straight to the latest.
func (u *updater) begin() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.state == "checking" || u.state == "downloading" {
		return false
	}
	u.state, u.err, u.retry, u.asked = "checking", "", false, time.Now()
	return true
}

func (u *updater) run() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	u.mu.Lock()
	lang := u.lang
	u.mu.Unlock()
	if lang == "" {
		lang = notesLang(trayLang(settings.Load().Lang, systemLang))
	}
	rel, err := update.LatestIn(ctx, lang)
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.staged != "" && u.latest != nil && (err != nil || !update.Newer(rel.Version, u.latest.Version)) {
		u.state = "ready" // what was downloaded is still the latest
		return
	}
	if err != nil {
		u.state, u.err = "error", err.Error()
		return
	}
	u.latest, u.notesIn = rel, lang
	switch {
	case !update.Released(Version):
		u.state = "source"
		return
	case !update.Newer(rel.Version, Version):
		u.state = "latest"
		return
	case u.bundle == "" && u.exe == "":
		u.state = "available" // the user fetches it from the release page
		return
	case u.replaced():
		// another magpie put the update in already: a restart is all
		u.state = "ready"
		if u.onReady != nil {
			go u.onReady(rel.Version)
		}
		return
	}
	// an older download is replaced by this one: until it is in, there is
	// nothing to install
	u.state, u.done, u.total, u.staged = "downloading", 0, 0, ""
	u.mu.Unlock()
	ctx = update.WithProgress(ctx, func(done, total int64) {
		u.mu.Lock()
		u.done, u.total = done, total
		u.mu.Unlock()
	})
	var staged string
	if u.bundle != "" {
		staged, err = update.Stage(ctx, rel, u.bundle)
	} else {
		staged, err = update.StageBinary(ctx, rel)
	}
	u.mu.Lock()
	if err != nil {
		log.Println("update:", err)
		u.state, u.err, u.retry = "error", err.Error(), true
		return
	}
	u.state, u.staged = "ready", staged
	if u.onReady != nil {
		go u.onReady(rel.Version)
	}
}

// install swaps the staged version in; it reports whether it did. Asked
// (the restart), it may put up the password prompt; on quitting it doesn't,
// and a folder that needs one waits for the next restart. A failure keeps
// the staged version, so the restart can be tried again.
func (u *updater) install(ask bool) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.replaced() {
		// another magpie updated the binary after this one started: it is
		// in, and swapping this one's download in would move it aside (the
		// download is left: it may be the other's, staged at the same name)
		u.staged = ""
		return true
	}
	if u.staged == "" {
		return false
	}
	var err error
	if u.bundle != "" {
		err = update.Install(u.staged, u.bundle)
		if ask && update.NeedsAdmin(err) {
			err = update.InstallAsAdmin(u.staged, u.bundle)
		}
	} else {
		err = update.InstallBinary(u.staged, u.exe)
		if ask && update.NeedsAdmin(err) && update.CanElevate() {
			err = update.InstallBinaryAsAdmin(u.staged, u.exe)
		}
	}
	u.err = ""
	if err != nil {
		if !errors.Is(err, update.ErrCanceled) {
			log.Println("update:", err)
			u.err = err.Error()
		}
		return false
	}
	u.staged = ""
	return true
}

// replaced is whether the binary was updated by another magpie since this
// one started, which runs on from where it was moved aside.
func (u *updater) replaced() bool {
	return u.exe != "" && update.Replaced(u.exe, u.self)
}

func (u *updater) json() updateJSON { return u.jsonIn("") }

// jsonIn is the state as a page in lang ("" when it doesn't say) shows it.
// Notes held in another language are asked for again in lang, at most
// once a minute; the page's next look has them.
func (u *updater) jsonIn(lang string) updateJSON {
	busy := gatewayBusy()
	u.mu.Lock()
	defer u.mu.Unlock()
	if lang != "" {
		u.lang = lang
		if u.latest != nil && u.notesIn != lang && u.state != "checking" && time.Since(u.relangAt) >= time.Minute {
			u.relangAt = time.Now()
			go u.relang(u.latest.Version, lang)
		}
	}
	j := updateJSON{State: u.state, Current: Version, Error: u.err, Retry: u.retry}
	if u.state == "checking" && u.staged != "" {
		j.State = "ready" // what was downloaded can still be restarted into
	}
	if u.state == "available" {
		j.Stuck = u.stuck
	}
	if u.state == "downloading" {
		j.Done, j.Total = u.done, u.total
	}
	if j.State == "ready" || u.waiting {
		j.Waiting, j.GaveUp = u.waiting, u.gaveUp && !u.waiting
		if busy.Any() {
			j.Busy = &busy
		}
	}
	if u.latest != nil {
		// the notes without their download links: magpie downloads it itself
		j.Latest, j.Notes, j.URL = u.latest.Version, update.StripInstall(u.latest.Notes), u.latest.URL
	}
	return j
}

// inLang has the next checks ask for the notes in lang ("" leaves it).
func (u *updater) inLang(lang string) {
	if lang == "" {
		return
	}
	u.mu.Lock()
	u.lang = lang
	u.mu.Unlock()
}

// relang asks the feed again for version's notes in lang, the pages'
// language not being the one they were asked in.
func (u *updater) relang(version, lang string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rel, err := update.LatestIn(ctx, lang)
	if err != nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.latest == nil || u.latest.Version != version || rel.Version != version {
		return // a check since has its own
	}
	c := *u.latest // the download may still be reading the one held
	c.Notes = rel.Notes
	u.latest, u.notesIn = &c, lang
}

func updateRoutes(mux *http.ServeMux, w Windows) {
	mux.HandleFunc("GET /api/update", func(rw http.ResponseWriter, r *http.Request) {
		writeJSON(rw, updates.jsonIn(askedLang(r)))
	})
	mux.HandleFunc("POST /api/update/check", func(rw http.ResponseWriter, r *http.Request) {
		lang := askedLang(r)
		updates.inLang(lang) // the check's notes are in the page's language
		updates.check()
		writeJSON(rw, updates.jsonIn(lang))
	})
	// install restarts into the staged version; after a failed download it
	// downloads it again, and the page restarts once it's in. Only an app
	// that can't replace itself is sent to the release page.
	// The window's page names the tab it is on, to come back to it.
	mux.HandleFunc("POST /api/update/install", func(rw http.ResponseWriter, r *http.Request) {
		var in struct {
			View string `json:"view"`
			// When: "now" restarts whatever is in flight, "cancel" stops
			// a restart waiting; else a busy gateway is waited for (#577)
			When string `json:"when"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.When == "cancel" {
			updates.cancelWait()
			writeJSON(rw, updates.json())
			return
		}
		if updates.json().State == "ready" {
			if in.When != "now" && !updates.idle() {
				updates.waitIdle(func() bool {
					if restartToUpdate(isWeb(w), in.View != "" || mainShown(w), in.View) {
						go w.Quit()
						return true
					}
					return false
				})
				writeJSON(rw, updates.json())
				return
			}
			updates.cancelWait()
			updates.recheck()
		}
		switch j := updates.json(); {
		case j.State == "ready":
			if restartToUpdate(isWeb(w), in.View != "" || mainShown(w), in.View) {
				rw.WriteHeader(http.StatusNoContent)
				go w.Quit()
				return
			}
		case j.State == "error" && j.Retry:
			if updates.begin() {
				go updates.run()
			}
		case j.State == "available" && j.URL != "":
			w.OpenURL(j.URL)
		}
		writeJSON(rw, updates.json())
	})
}

// restartToUpdate installs the staged version and arranges for it to open
// once this process is gone; the caller then quits. magpie web runs on as
// the new version in its own place (Web.Wait) instead of opening the app.
// Off the Mac, the new one opens its window on view when window is set, and
// only the tray icon when not; the Mac's always opens its window.
func restartToUpdate(web, window bool, view string) bool {
	bundle, exe := updates.bundle, updates.exe
	updates.mu.Lock()
	version := ""
	if updates.latest != nil {
		version = updates.latest.Version
	}
	updates.mu.Unlock()
	if !updates.install(true) {
		return false
	}
	updatedInApp(version) // its first start leaves the notes to Settings (#525)
	if web {
		webReexec.Store(true)
		return true
	}
	var err error
	if bundle != "" {
		err = update.Relaunch(bundle)
	} else {
		err = update.RelaunchBinary(exe, window, view)
	}
	if err != nil {
		log.Println("update:", err)
	}
	return true
}

// mainShown says whether the app's window is up, for a restart asked for
// from the panel to bring it back.
func mainShown(w Windows) bool {
	s, ok := w.(interface{ MainShown() bool })
	return ok && s.MainShown()
}

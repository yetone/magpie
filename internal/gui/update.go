package gui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// updater keeps the app current. It asks the feed at start-up and every six
// hours, or as often as Settings says, or only when asked (#472); when a newer release is out and the app may replace itself — the
// bundle on a Mac, the binary elsewhere — the new one is downloaded
// straight away, so all that is left is a restart; with Settings'
// NoAutoDownload it is only said to be out, and downloaded on a click
// (#2296). Quitting installs it too, and the next launch is
// the new version. Where the app's folder isn't the user's to change, the
// restart asks for the administrator's password; only an app that can't be
// replaced at all (run from its disk image, say) is sent to the release page.
type updater struct {
	mu     sync.Mutex
	state  string // checking | latest | found | downloading | ready | available | source | error | blocked
	latest *update.Release
	// behind: since when a newer version has been out for this one, and
	// whether it is urgent; the pill waits for it (update.Behind)
	behind update.Behind
	err    string
	bundle string // the .app to replace, "" when not in one or stuck
	stuck  string // why the .app or binary can't be replaced where it is (update.Stuck, exeStuck)
	exe    string // not in a .app: the binary to replace, "" when not writable
	exeDir string // not in a .app: the binary's folder, named when it is stuck there
	// stuck "package": the package manager that installed the binary,
	// which updates it instead (update.PackagedBy)
	packaged *update.Packaged
	self     os.FileInfo // exe (in a .app: bundleExe) as this process started from it
	retry    bool        // error: the download failed, and may be tried again
	mirror   string      // error: the mirror the failed download came through (#893)
	network  bool        // error: the network broke the download off (update.Network)
	staged   string
	// stable: latest was found on Settings' Stable channel; newest, the
	// newest release on every version when that is newer than this one
	// and Stable has nothing newer (ylspcm on X)
	stable bool
	newest string
	aside  string // Windows: where install moved the running exe, to put back
	// in a .app: its binary, where this process started from
	bundleExe string
	// a version this computer wouldn't start (Smart App Control, #894),
	// which the clock's checks don't download again; byClock, the check
	// under way is the clock's
	blocked *update.Blocked
	byClock bool
	// fetch: the check under way is a click on Download update, which
	// downloads what it finds whatever Settings' NoAutoDownload says; with
	// it on, any other check stops at found (#2296)
	fetch bool
	// keep: the check under way leaves a version downloaded and waiting
	// as it is, as the clock's does (freshen)
	keep bool
	// forRestart: the check under way is a restart's (recheck); what it
	// downloads is staged at restartStaged, and a restart up to
	// freshAfter later goes into it without asking the feed again
	// (elis_pina on X)
	forRestart    bool
	restartStaged time.Time
	asked         time.Time // when the feed was last asked, by the clock or a click
	done          int64     // downloading: bytes so far, of total (0 when unknown)
	total         int64
	step          string     // downloading: the step under way (update.StepDownload, …)
	rate          []progMark // downloading: the last few seconds' progress, for its speed
	onReady       func(version string)
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
	// drainMost, with a wait `magpie update` armed: past it the restart
	// goes ahead whatever is in flight (restartDrain); 0 waits as long as
	// waitMost
	drainMost time.Duration
	waitGen   int
	gaveUp    bool
	whenIdle  func() bool
	onWait    func()
	loops     sync.WaitGroup // the waits going on
	// With Settings' AutoRestart, the clock has autoRestart run (the app's
	// or magpie web's restart) once a downloaded update is ready and the
	// gateway idle (#2096); autoSkip is the version whose wait the user
	// called off, which isn't restarted into by itself again.
	autoRestart func() bool
	autoSkip    string
	waitAsked   bool // the restart waiting was a click's
	// installed: this process put an update in (install), and is leaving
	// for it; followed: it is restarting into one another magpie put in
	// (restartBeside). Either way what is on disk is no longer another's
	// for followReplaced to restart into.
	installed bool
	followed  bool
}

type updateJSON struct {
	State   string `json:"state"`
	Current string `json:"current"`
	Latest  string `json:"latest,omitempty"`
	Notes   string `json:"notes,omitempty"`
	URL     string `json:"url,omitempty"`
	Stuck   string `json:"stuck,omitempty"` // available: why it can't update itself
	// available, stuck "not-writable": the folder magpie may not write to
	StuckDir string `json:"stuckDir,omitempty"`
	// available, stuck "package": the package manager that installed
	// magpie and updates it (pacman's magpie-bin from the AUR, say), and
	// the command that does
	Packaged *packagedJSON `json:"packaged,omitempty"`
	Retry    bool          `json:"retry,omitempty"` // error: a click downloads it again
	// error: the download failed through this mirror (Settings' UpdateMirror),
	// and GitHub itself may do (#893)
	Mirror string `json:"mirror,omitempty"`
	// error: the network broke the download off, tried again already; the
	// row says so in the app's language instead of the raw error
	Network bool   `json:"network,omitempty"`
	Error   string `json:"error,omitempty"`
	// Channel: "stable" when the answer is Settings' Stable channel's;
	// latest on it, Newest is the newest version out on every version,
	// when newer than this one: Stable's "up to date" isn't the newest
	Channel string `json:"channel,omitempty"`
	Newest  string `json:"newest,omitempty"`
	Done    int64  `json:"done,omitempty"` // downloading: bytes so far
	// downloading: the step under way, "" before the first
	// (update.StepDownload, StepRetry, StepVerify, StepUnpack,
	// StepSignature), and how fast the bytes came over the last seconds,
	// per second, left out while too little is known
	Step  string `json:"step,omitempty"`
	Speed int64  `json:"speed,omitempty"`
	// blocked: the version this computer wouldn't start, and why (#894)
	Blocked *update.Blocked `json:"blocked,omitempty"`

	// Quiet: a newer version is out but this one has not been behind long
	// enough, and none is urgent, so the header's pill stays out (the
	// Version row and Restart to update still offer it)
	Quiet bool  `json:"quiet,omitempty"`
	Total int64 `json:"total,omitempty"`
	// found: the size of the file Download update would fetch, in bytes
	// (0 when the feed doesn't say)
	Size int64 `json:"size,omitempty"`
	// ready: what the gateway this process serves has in flight, which a
	// restart would cut short; Waiting, the restart waits for it to end;
	// GaveUp, the last wait ran out with it still busy
	Busy    *gateway.Busy `json:"busy,omitempty"`
	Waiting bool          `json:"waiting,omitempty"`
	GaveUp  bool          `json:"gaveUp,omitempty"`
	// Handover, the restart hands the gateway to the new version
	// (handover.go): Busy is then the Claude subscription's turns alone,
	// every other request finishing as the new version takes the next
	Handover bool `json:"handover,omitempty"`
	// Using, a restart by itself waits for the user to stop using a
	// window (inUseAt)
	Using bool `json:"using,omitempty"`
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
	u.behind = update.ReadBehind(Version)
	u.place(runtime.GOOS, update.GUI)
	gateway.SetRestarter(u.restartBesideAsked) // magpie update in a terminal
	gateway.SetForceRestarter(u.restartBesideNow)
	gateway.SetUpdater(u.updateAsked)                   // a magpie using this one updated
	go provider.KeepRemotesUpdated(backendCtx, Version) // and this one's remotes
	go u.followReplaced(backendCtx)                     // the app's or magpie web's update (visamzh on X)
	if u.bundle != "" {
		go update.ForgetOldStaging(u.bundle) // #3965
	}
	go func() {
		time.Sleep(5 * time.Second) // let the app settle first
		// until this one has handed the gateway to the new version: the
		// copy an update left finishing its requests checks for, and
		// restarts into, nothing (#4938)
		for backendCtx.Err() == nil {
			u.mu.Lock()
			last := u.asked
			u.mu.Unlock()
			s := settings.Load()
			if updateDue(s, last, time.Now()) {
				u.checkByClock()
			}
			u.autoTick(s)
			time.Sleep(time.Minute)
		}
	}()
}

// bundleOf and executable are update.Bundle and update.Executable; tests
// stand in for them.
var (
	bundleOf   = update.Bundle
	executable = update.Executable
)

// place takes what an update replaces on goos: the .app the binary is in,
// else the binary itself. On a Mac that is the terminal build too (gui
// false: magpie-cli-darwin, or `magpie web` from it), which has no .app to
// be in and which `magpie update` replaces the same way; before, its page
// only ever offered the release page. The app's own binary copied out of
// its .app isn't replaced by the terminal build.
func (u *updater) place(goos string, gui bool) {
	if b := bundleOf(); b != "" {
		if u.stuck = update.Stuck(b); u.stuck == "" {
			u.bundle = b
			// what `magpie update` replacing the .app is told by
			// (replaced)
			if exe, err := executable(); err == nil {
				u.bundleExe = exe
				u.self, _ = os.Stat(exe)
			}
		}
	} else if goos != "darwin" || !gui {
		if exe, err := executable(); err == nil {
			u.placeExe(exe)
		}
	}
}

// placeExe takes exe as the binary an update replaces, or
// says why it can't be (stuck). A binary a package manager installed is
// left to it ("package"), even where magpie may write or ask for the
// administrator's password: replacing it under the manager is what made
// the two fight (shuhai on Discord, the AUR's magpie-bin).
func (u *updater) placeExe(exe string) {
	u.exeDir = filepath.Dir(exe)
	if p := packagedBy(exe); p != nil {
		u.stuck, u.packaged = "package", p
		return
	}
	if u.stuck = exeStuck(u.exeDir); u.stuck != "" {
		return
	}
	u.exe = exe
	u.self, _ = os.Stat(exe)
	update.RemoveOld(exe)       // what the last updates on Windows moved aside
	update.RemoveStaleNew(exe)  // what a magpie left running downloaded again
	update.EnsureCompanion(exe) // Windows: the magpie.com a terminal runs, of this version (#3737)
	u.blocked = update.ReadBlocked(Version)
}

// exeStuck says why the binary in dir can't be replaced, or "" when it
// can: "not-writable" when magpie may not write to dir and this system
// can't ask for the administrator's password. On Windows a magpie.exe kept
// at C:\ updated only by opening the release page, with nothing saying why
// (#1277). "container" is that in a container (the Docker image's /magpie,
// run as nonroot): there the new image is pulled, and "move it to a folder
// you can write to" was the wrong advice.
func exeStuck(dir string) string {
	if update.Writable(dir) || canElevate() {
		return ""
	}
	if inContainer() {
		return "container"
	}
	return "not-writable"
}

// canElevate is update.CanElevate, inContainer gateway.InContainer; tests
// stand in for them.
var (
	canElevate  = update.CanElevate
	inContainer = gateway.InContainer
	packagedBy  = update.PackagedBy
)

type packagedJSON struct {
	Manager string `json:"manager"` // as its users call it: pacman, apt, rpm, Homebrew, Nix
	Package string `json:"package,omitempty"`
	Command string `json:"command,omitempty"` // what updates it, "" when no one command does
}

// check asks the feed and, when it can, stages the new version.
func (u *updater) check() {
	if u.begin() {
		u.run()
	}
}

// checkByClock is check, by the clock rather than a click: a version this
// computer wouldn't start is left as it is.
func (u *updater) checkByClock() {
	if u.begin() {
		u.mu.Lock()
		u.byClock = true
		u.mu.Unlock()
		u.run()
	}
}

// download is a click on Download update (or the header's Update) with a
// newer version found but not downloaded, Settings' NoAutoDownload on
// (#2296): a check that downloads what it finds. It reports whether one
// started.
func (u *updater) download() bool {
	if !u.begin() {
		return false
	}
	u.mu.Lock()
	u.fetch = true
	u.mu.Unlock()
	go u.run()
	return true
}

// recheck asks the feed again before a restart into what was downloaded,
// with Settings' NoAutoDownload on, so a newer version out since is the
// one restarted into. It waits for the feed a little while (recheckTicks
// of recheckTick); a newer version is then downloading. asked is a click
// on Restart to update, which asks for the newest: a newer version out
// since is downloaded and restarted into, not the one the last check
// found (coeo91 on Discord).
//
// With downloads automatic (the default) a version downloaded and waiting
// is restarted into as it is, and the feed isn't asked: magpie releases
// many times a day, so the click found a newer one nearly every time and
// downloaded it whole while the user waited, which made the download done
// ahead of time no use (canlee886 on X). The new version's first check,
// seconds after it starts, downloads whatever is newer by then.
//
// A version this asking itself downloaded is restarted into as it is too,
// for freshAfter. The header's Update restarts once its download is in,
// and that restart asked the feed again; magpie had released again while
// a slow download came in, so it downloaded once more, every time, and
// never restarted (elis_pina on X, on Windows).
func (u *updater) recheck(asked bool) {
	if u.keepsStaged(settings.Load()) {
		return
	}
	u.askFor(asked, false, true)
}

// ask asks the feed and waits for it a little while (recheckTicks of
// recheckTick). fetch is a click's: what is found is downloaded whatever
// Settings' NoAutoDownload says. keep leaves a version downloaded and
// waiting as it is, as the clock's check does: a newer one found is not
// downloaded in its place.
func (u *updater) ask(fetch, keep bool) { u.askFor(fetch, keep, false) }

// askFor is ask; forRestart, a restart's (recheck).
func (u *updater) askFor(fetch, keep, forRestart bool) {
	if u.begin() {
		u.mu.Lock()
		u.fetch, u.keep, u.forRestart = fetch, keep, forRestart
		u.mu.Unlock()
		go u.run()
	}
	for range recheckTicks {
		if !u.asking() {
			return
		}
		time.Sleep(recheckTick)
	}
}

// keepsStaged is whether a restart goes into the version downloaded and
// waiting without asking the feed again: one is ready, and downloads are
// automatic or a restart's asking downloaded it under freshAfter ago
// (recheck).
func (u *updater) keepsStaged(s settings.Settings) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.state != "ready" || u.staged == "" {
		return false
	}
	return !s.NoAutoDownload || !u.restartStaged.IsZero() && time.Since(u.restartStaged) < freshAfter
}

// askedOver is whether the feed was last asked over d ago.
func (u *updater) askedOver(d time.Duration) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return time.Since(u.asked) >= d
}

// recheckTicks of recheckTick is how long recheck waits for the feed;
// tests shorten it.
var (
	recheckTicks = 50
	recheckTick  = 200 * time.Millisecond
)

// asking is whether the feed is being asked. The state then reads
// "ready" while an older download waits (jsonIn), which a restart doesn't
// go into: the answer may name a newer one.
func (u *updater) asking() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.state == "checking"
}

// freshAfter is how long the feed's answer stays fresh for the dialog
// that shows the update before it is put in.
const freshAfter = 5 * time.Minute

// freshen asks the feed again, as recheck does, when a version downloaded
// waits and the feed was last asked over freshAfter ago (#910). With
// downloads automatic recheck leaves the one waiting, which is what the
// dialog then names and the restart goes into (canlee886 on X).
func (u *updater) freshen() {
	u.mu.Lock()
	stale := u.state == "ready" && u.staged != "" && time.Since(u.asked) >= freshAfter
	u.mu.Unlock()
	if stale {
		// the dialog asks for nothing to be downloaded; with downloads
		// automatic it names the one waiting, which the restart goes
		// into, and takes the notes the feed has for it now (#649)
		u.ask(false, !settings.Load().NoAutoDownload)
	}
}

// An answer of "latest" is about the moment the feed was asked, and magpie
// releases many times a day: Settings' Version row said 已是最新 on
// v0.1.1402 with v0.1.1517 out (RickyWei on Discord, Windows), the answer
// of a check made while 1402 was the newest and never made again (automatic
// checks off, or once a day). So it is shown only while younger than
// freshAfter. When the Version row is drawn (GET /api/update?fresh=1) an
// older one is asked again, with automatic updates on; with them off the
// row says so, and its Check asks.

// latestStale is whether the state is a "latest" older than freshAfter.
// The caller holds u.mu.
func (u *updater) latestStale() bool {
	return u.state == "latest" && time.Since(u.asked) >= freshAfter
}

// refreshLatest asks the feed again when the Version row is drawn over a
// stale "latest", unless automatic updates are off. It doesn't wait for
// the answer: the row shows checking, and reads again.
func (u *updater) refreshLatest(s settings.Settings) {
	if s.NoAutoUpdate {
		return
	}
	u.mu.Lock()
	stale := u.latestStale()
	u.mu.Unlock()
	if stale && u.begin() {
		go u.run()
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
	u.state, u.err, u.retry, u.mirror, u.network, u.asked, u.byClock, u.fetch, u.keep, u.forRestart = "checking", "", false, "", false, time.Now(), false, false, false, false
	return true
}

// checkWait bounds asking the feed. The download after it has no total
// bound: a slow one still moving goes on, and one that stops sending is
// ended by update's stall timer, tried again a bounded number of times
// (#5572: an 83 MB download under ~140 KB/s failed at 10 minutes part
// way). A var for tests.
var checkWait = 10 * time.Minute

func (u *updater) run() {
	ctx := context.Background()
	cctx, cancel := context.WithTimeout(ctx, checkWait)
	defer cancel()
	s := settings.Load()
	u.mu.Lock()
	lang := u.lang
	// Settings' NoAutoDownload: what is found is only said, unless this
	// is a click on Download update
	hold := s.NoAutoDownload && !u.fetch
	u.mu.Unlock()
	if lang == "" {
		lang = notesLang(trayLang(s.Lang, systemLang))
	}
	stable := s.UpdateChannel == settings.UpdateStable
	rel, err := update.LatestIn(cctx, lang)
	// Stable has nothing newer than this one: the newest on every version
	// is asked too, which the version row names. A bare "Up to date" on
	// Stable, with newer releases out, read as the check being wrong, and
	// the app was installed again by hand each time (ylspcm on X). Asked
	// before u.mu, which the pages' reads need.
	newest := ""
	if stable && err == nil && !update.Newer(rel.Version, Version) {
		if every, e := update.LatestEvery(cctx, lang); e == nil && update.Newer(every.Version, Version) {
			newest = every.Version
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.newest = newest
	if err == nil && u.latest != nil && update.Newer(u.latest.Version, rel.Version) {
		if stable != u.stable {
			// an older version than the one found before, on another
			// channel: Settings' Update channel went to Stable since
			// (ssharpcc on X). What was downloaded from every version
			// isn't put in, and the version found is the feed's again, as
			// if never found.
			u.staged, u.latest = "", nil
		} else {
			// on the same channel it is the feed behind: the site, short
			// of GitHub's API, answers from GitHub's latest redirect or
			// the last answer it kept, which can be a release or more
			// old. Restart to update asks the feed first (recheck), and
			// such an answer dropped the download it was about to put
			// in: no restart, and the app stayed on the version it was
			// (littleHong on Discord, v0.1.2037 offered v0.1.2038). The
			// version found stands, and so does its download.
			rel, lang = u.latest, u.notesIn
		}
	}
	// a version downloaded on a click stays the one restarted into while
	// downloads wait for a click: a newer one out since isn't fetched.
	// Nor is one by the clock's check: magpie releases many times a day,
	// so each check found a newer one and downloaded it whole (40 to 95
	// MB) in place of the one waiting, every check until a restart, on
	// the user's proxy too (iiiiiimone on X). With downloads automatic a
	// restart goes into the one waiting and the new version's first check
	// fetches anything newer (recheck, keep, canlee886 on X); with them on
	// a click, a restart asks the feed first and fetches the newest then.
	// Quitting puts in the one waiting.
	if u.staged != "" && u.latest != nil && (err != nil || !update.Newer(rel.Version, u.latest.Version) || hold || u.byClock || u.keep) {
		u.state = "ready" // what was downloaded is still the latest
		// the feed found it before it had its notes (the site, kept from
		// GitHub's API, reads them from the releases feed, which can lag
		// a new release by minutes): the notes it has now are taken (#649,
		// Sun1090: v0.1.1224's dialog said it had none)
		if err == nil && rel.Version == u.latest.Version && strings.TrimSpace(u.latest.Notes) == "" && strings.TrimSpace(rel.Notes) != "" {
			c := *u.latest // the install may still be reading the one held
			c.Notes = rel.Notes
			u.latest, u.notesIn = &c, lang
		}
		return
	}
	if err != nil {
		u.state, u.err = "error", err.Error()
		return
	}
	u.latest, u.notesIn, u.stable = rel, lang, stable
	if update.Released(Version) && update.Newer(rel.Version, Version) {
		u.behind = update.NoteBehind(Version, rel.Version, update.UrgentIn(rel.Notes), time.Now())
		if u.behind.Urgent == "" {
			// an urgent release between this one and the latest: its
			// notes aren't the latest's
			go u.urgentBetween(rel.Version, lang)
		}
	}
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
	case u.byClock && u.blocked != nil && u.blocked.Version == rel.Version:
		// this computer didn't start it (#894): not downloaded again by
		// itself; a check asked for tries it once more
		u.state = "blocked"
		return
	case u.replaced():
		// another magpie put the update in already: a restart is all
		u.state = "ready"
		if u.onReady != nil {
			go u.onReady(rel.Version)
		}
		return
	case hold:
		// downloads wait for a click (#2296): the version row and the
		// header's Update say it is out, and offer it
		u.state = "found"
		return
	}
	// an older download is replaced by this one: until it is in, there is
	// nothing to install
	u.state, u.done, u.total, u.staged, u.step, u.rate, u.restartStaged = "downloading", 0, 0, "", "", nil, time.Time{}
	forRestart := u.forRestart
	u.mu.Unlock()
	ctx = update.WithProgress(ctx, func(done, total int64) {
		u.mu.Lock()
		u.done, u.total = done, total
		u.rate = marked(u.rate, done, time.Now())
		u.mu.Unlock()
	})
	ctx = update.WithStep(ctx, func(step string) {
		u.mu.Lock()
		u.step = step
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
		u.state, u.err, u.retry, u.mirror, u.network = "error", err.Error(), true, update.MirrorOf(err), update.Network(err)
		return
	}
	u.state, u.staged = "ready", staged
	if forRestart {
		u.restartStaged = time.Now()
	}
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
		u.staged, u.installed = "", true
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
		u.aside, err = update.InstallBinaryAside(u.staged, u.exe)
		if ask && update.NeedsAdmin(err) && update.CanElevate() {
			err = update.InstallBinaryAsAdmin(u.staged, u.exe)
		}
	}
	u.err = ""
	if errors.As(err, new(*update.BlockedError)) {
		u.staged = "" // removed: this computer won't run it
		u.wontStart(err)
		return false
	}
	if err != nil {
		if !errors.Is(err, update.ErrCanceled) {
			log.Println("update:", err)
			u.err = err.Error()
		}
		return false
	}
	u.staged, u.installed = "", true
	return true
}

// wontStart notes that the version downloaded didn't start here: it is
// not downloaded again by the clock, and the version row says why. Called
// with u.mu held.
func (u *updater) wontStart(err error) {
	log.Println("update:", err)
	v := ""
	if u.latest != nil {
		v = u.latest.Version
	}
	b := update.NoteBlocked(v, err)
	u.blocked, u.state, u.aside = &b, "blocked", ""
	u.installed = false // this one runs on, back in its place
	u.err = err.Error()
}

// assetSize is the size of rel's file this app downloads to update
// itself, 0 when the feed doesn't say.
func (u *updater) assetSize(rel *update.Release) int64 {
	if u.bundle != "" {
		return rel.Assets[update.AppAsset()].Size
	}
	return rel.Assets[update.BinaryAsset()].Size
}

// replaced is whether the binary, or the .app, was updated by another
// magpie since this one started (`magpie update` in a terminal), which
// runs on from where it was moved aside.
func (u *updater) replaced() bool {
	switch {
	case u.exe != "":
		return update.Replaced(u.exe, u.self)
	case u.bundle != "" && u.bundleExe != "":
		return update.Replaced(u.bundleExe, u.self)
	}
	return false
}

func (u *updater) json() updateJSON { return u.jsonIn("") }

// jsonIn is the state as a page in lang ("" when it doesn't say) shows it.
// Notes held in another language are asked for again in lang, at most
// once a minute; the page's next look has them.
func (u *updater) jsonIn(lang string) updateJSON {
	busy, handover := restartBusy(), handsOver()
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
	if u.stable && u.latest != nil {
		j.Channel = settings.UpdateStable
		if u.state == "latest" {
			j.Newest = u.newest
		}
	}
	if u.latestStale() {
		j.State = "" // not asked lately: no claim to be up to date
	}
	if u.state == "error" {
		j.Mirror, j.Network = u.mirror, u.network
	}
	if u.state == "checking" && u.staged != "" {
		j.State = "ready" // what was downloaded can still be restarted into
	}
	if u.state == "available" {
		j.Stuck = u.stuck
		if u.stuck == "not-writable" {
			j.StuckDir = u.exeDir
		}
		if u.stuck == "package" && u.packaged != nil {
			j.Packaged = &packagedJSON{Manager: u.packaged.Name(), Package: u.packaged.Package, Command: u.packaged.Command()}
		}
	}
	if u.state == "downloading" {
		j.Done, j.Total, j.Step = u.done, u.total, u.step
		if u.step == update.StepDownload {
			j.Speed = speedOf(u.rate, u.done, time.Now())
		}
	}
	if u.state == "blocked" {
		j.Blocked = u.blocked
	}
	switch j.State {
	case "ready", "found", "available":
		j.Quiet = !u.behind.Lit(Version, time.Now())
	}
	if u.state == "found" && u.latest != nil {
		j.Size = u.assetSize(u.latest)
	}
	if j.State == "ready" || u.waiting {
		j.Waiting, j.GaveUp = u.waiting, u.gaveUp && !u.waiting
		j.Using = u.waiting && !u.waitAsked && inUseAt(time.Now())
		j.Handover = handover
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

// urgentBetween notes it when a release after this one up to latest is
// urgent: a security fix a few releases back isn't in the latest's notes.
func (u *updater) urgentBetween(latest, lang string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	notes, err := update.NotesBetween(ctx, Version, latest, lang)
	if err != nil {
		return
	}
	for _, n := range notes {
		if update.UrgentIn(n.Notes) {
			b := update.NoteUrgent(Version, n.Version, time.Now())
			u.mu.Lock()
			u.behind = b
			u.mu.Unlock()
			return
		}
	}
}

// quietAgain starts the pill's quiet over: it was dismissed.
func (u *updater) quietAgain() {
	b := update.QuietAgain(Version, time.Now())
	u.mu.Lock()
	u.behind = b
	u.mu.Unlock()
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
	// a page's word of the user's input, which a restart by itself waits
	// for to end (inUseAt); open: a dialog is open
	mux.HandleFunc("POST /api/update/using", func(rw http.ResponseWriter, r *http.Request) {
		var b struct {
			Open bool `json:"open"`
		}
		json.NewDecoder(io.LimitReader(r.Body, 1<<10)).Decode(&b)
		noteUse(b.Open, time.Now())
		rw.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/update", func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fresh") == "1" { // the Version row
			updates.refreshLatest(settings.Load())
		}
		writeJSON(rw, updates.jsonIn(askedLang(r)))
	})
	// notes: what changed in every release after this one up to the update,
	// newest first, shown before it is put in (Hu9956, #844); with the site's
	// list out of reach, the update's own notes
	// The update waiting is the one found when the feed was last asked, up
	// to Settings' interval ago (six hours), and releases come many a day:
	// the dialog stopped at v0.1.1020 with v0.1.1024 out (#910, Moody-Sin).
	// So a feed not asked for a while is asked again first; a newer release
	// is the one the dialog names (latest) and starts downloading, and the
	// restart waits for it, as one after a recheck does.
	mux.HandleFunc("GET /api/update/notes", func(rw http.ResponseWriter, r *http.Request) {
		updates.inLang(askedLang(r))
		updates.freshen()
		j := updates.jsonIn(askedLang(r))
		out := struct {
			Latest   string        `json:"latest,omitempty"`
			Releases []update.Note `json:"releases"`
			// Total: how many releases there are since this one when
			// more than Releases holds (MostNotes keeps the newest)
			Total int    `json:"total,omitempty"`
			Error string `json:"error,omitempty"`
		}{Latest: j.Latest, Releases: []update.Note{}}
		if j.Latest != "" {
			notes, total, err := fetchNotes(r.Context(), Version, j.Latest, pageLang(r))
			if len(notes) == 0 && strings.TrimSpace(j.Notes) != "" {
				notes = []update.Note{{Version: j.Latest, Notes: j.Notes, URL: j.URL}}
			}
			if total > len(notes) {
				out.Total = total
			}
			if len(notes) > 0 {
				out.Releases = notes
			} else if err != nil {
				out.Error = err.Error()
			}
		}
		writeJSON(rw, out)
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
			updates.callOff()
			writeJSON(rw, updates.json())
			return
		}
		if updates.json().State == "ready" {
			if in.When != "now" && !updates.idle() {
				updates.waitIdle(func() bool {
					if restartFrom(w, in.View) {
						go leave(w.Quit)
						return true
					}
					return false
				}, true)
				writeJSON(rw, updates.json())
				return
			}
			updates.cancelWait()
			updates.recheck(true)
			if updates.asking() {
				// the feed hasn't answered yet: the page reads again and
				// restarts once it has, into the newest
				j := updates.json()
				j.State = "checking"
				writeJSON(rw, j)
				return
			}
		}
		switch j := updates.json(); {
		case j.State == "ready":
			if restartFrom(w, in.View) {
				rw.WriteHeader(http.StatusNoContent)
				go leave(w.Quit)
				return
			}
		case j.State == "error" && j.Retry, j.State == "found":
			// a click: the download is asked for, whatever Settings'
			// NoAutoDownload says (#2296)
			updates.download()
		case j.State == "available" && j.Stuck == "package":
			// the package manager updates it: the release page's download
			// isn't for this install
		case j.State == "available" && j.URL != "":
			// the newest's page, not the one the last check found: a check
			// over a minute old is made again first (coeo91 on Discord)
			if updates.askedOver(time.Minute) {
				updates.recheck(true)
				if j = updates.json(); j.URL == "" {
					break
				}
			}
			_ = w.OpenURL(j.URL) // nothing waits on the page: the card still shows the update
		}
		writeJSON(rw, updates.json())
	})
}

// relaunchBinary is update.RelaunchBinary; tests stand in for it.
var relaunchBinary = update.RelaunchBinary

// restartFrom is restartToUpdate asked from w's page, on view: a browser
// on the app's page restarts the app, with its window up as it was, not
// opened on the tab the browser was on.
func restartFrom(w Windows, view string) bool {
	if a, ok := w.(appWebHost); ok {
		return restartToUpdate(false, mainShown(a.app), "")
	}
	return restartToUpdate(ownWeb(w), view != "" || mainShown(w), view)
}

// restartToUpdate installs the staged version and arranges for it to open
// once this process is gone; the caller then quits. magpie web runs on as
// the new version in its own place (Web.Wait) instead of opening the app.
// Off the Mac, the new one opens its window on view when window is set, and
// only the tray icon when not; the Mac's always opens its window.
func restartToUpdate(web, window bool, view string) bool {
	return restartInto(web, window, view, false, false)
}

// restartInto is restartToUpdate; bySelf, Settings' AutoRestart restarts
// (#2096), and the Mac's new one too opens only its menu bar icon when no
// window was up, rather than putting up one the user had closed. The new
// one, by itself, also takes no focus (quiet_restart.go), and puts the
// window back minimised when it was (minimised, #3495).
func restartInto(web, window bool, view string, bySelf, minimised bool) bool {
	if pending.Load() != nil {
		return false // a handover under way restarts already
	}
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
	if bySelf && !web {
		markQuietRestart(window && minimised) // it comes back without taking focus
	} else {
		unmarkQuietRestart() // one asked for comes to the front
	}
	if web {
		webReexec.Store(true)
		return true
	}
	// on a Mac or Linux the new version takes the gateway over while this
	// one finishes its requests (handover.go)
	if bundle != "" && beginHandover(bundle, "", window || !bySelf, "", bySelf) ||
		bundle == "" && beginHandover("", exe, window, view, bySelf) {
		return true
	}
	var err error
	if bundle != "" {
		err = update.Relaunch(bundle, window || !bySelf, bySelf)
	} else {
		updates.mu.Lock()
		aside := updates.aside
		updates.mu.Unlock()
		err = relaunchBinary(exe, aside, window, view)
	}
	if errors.As(err, new(*update.BlockedError)) {
		// the new version didn't start and this one is back in its place
		// (#894): it runs on, and says why
		os.Remove(filepath.Join(settings.Dir(), updatedInAppFile))
		unmarkQuietRestart()
		updates.mu.Lock()
		updates.wontStart(err)
		updates.mu.Unlock()
		return false
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

// progMark is how many bytes a download had at a moment.
type progMark struct {
	at   time.Time
	done int64
}

// rateSpan is how far back a download's speed looks.
const rateSpan = 5 * time.Second

// marked is marks with done at now added, at most one each quarter
// second, those older than rateSpan dropped but the newest of them, and
// all of them when done went back (a host that sent the whole file again).
func marked(marks []progMark, done int64, now time.Time) []progMark {
	if n := len(marks); n > 0 && done < marks[n-1].done {
		marks = nil
	}
	if n := len(marks); n > 0 && now.Sub(marks[n-1].at) < 250*time.Millisecond {
		return marks
	}
	i := 0
	for i+1 < len(marks) && now.Sub(marks[i+1].at) >= rateSpan {
		i++
	}
	return append(marks[i:], progMark{now, done})
}

// speedOf is the bytes a second a download came at over about the last
// rateSpan, done the bytes it has now: 0 while under a second is known.
// A stalled download's falls to 0 as the seconds pass without bytes.
func speedOf(marks []progMark, done int64, now time.Time) int64 {
	for _, m := range marks {
		if d := now.Sub(m.at); d <= rateSpan+time.Second {
			if d < time.Second || done < m.done {
				return 0
			}
			return int64(float64(done-m.done) / d.Seconds())
		}
	}
	return 0
}

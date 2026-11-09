package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// The Codex app greys its Send for every model, magpie's too, while the
// ChatGPT account it is signed in to is used up (its composer's gate,
// XP in ChatGPT.app 26.1013's app-primary: authMethod "chatgpt" and
// rate_limit.allowed false). It reads authMethod from its app-server's
// account/read, which answers no account once magpie is Codex's provider
// (requires_openai_auth false), but it asks only as the app-server
// starts. So when magpie becomes Codex's provider for the used-up
// account (the agent package's AppOut), and again when it puts Codex
// back on the account after its reset, magpie ends the app's own
// app-server, the one it started with: the app starts another at once,
// reads account/read again, and sends (tried in ChatGPT.app 26.1013 on
// macOS; its window, threads and drafts stay). It does that on every
// system the app runs on, with nothing for the user to do, once no Codex
// turn is running: none through magpie, none open in Codex's sessions.
//
// The same holds for every change magpie makes to where Codex sends its
// requests: a Disconnect that takes openai_base_url out, a Connect that
// puts it in, failover, the gateway moved to another port. The app-server
// read config.toml as it started, and a Codex app left running kept
// sending to 127.0.0.1:3425 after Codex was disconnected (JiangLiuer222 on
// X), so each such change restarts it the same way.
//
// A change to the model list alone never does: the app takes an
// app-server's end as a crash and relaunches, and the list changes far
// more often than the route (an effort picked in the app, a provider
// renamed), which ended the app every few minutes (#4550). The app reads
// a new list at its next launch, or on the Agents row's Restart.
//
// The app-server is ended as a clean exit (proc.End): the app reads any
// other exit as a crash and shows it. A signed-in ChatGPT app can show its
// error page all the same; codex_app_crash.go reopens it then.

// CodexAppRoute is set by the agent package: what in Codex's config the
// app-server reads as it starts and magpie writes (its provider and base
// URL: route, compared, never parsed), and whether magpie is Codex's
// provider for its used-up ChatGPT account (out, AppOut); known false
// when Codex's config can't be read. Nothing the Codex app writes itself
// (its model or effort pick) and not the model list is in route, or a
// pick would restart it.
var CodexAppRoute func() (route string, out, known bool)

// CodexNoAccount is set by the agent package: whether the provider Codex's
// config names takes no ChatGPT account at all (a [model_providers] table
// without requires_openai_auth = true, such as magpie's with the user's
// own requires_openai_auth = false; danzi002 on Discord). Such a provider
// sends its own key, and the app-server answers account/read with no
// account for it (codex-rs ModelProviderInfo::account_state and
// resolve_provider_auth), so which ChatGPT account auth.json holds is
// nothing the app-server, the Codex app or the daemon goes by: an account
// switch restarts none of them. false when it can't be told.
var CodexNoAccount func() bool

// CodexTakeoverOff is set by the agent package: whether the user has
// turned off magpie's takeover of Codex's used-up account (AppOutOff):
// its sign-in set to Always ChatGPT, as the takeover note's Keep on
// ChatGPT does, or Codex not connected. The takeover ending while it says
// so is the user's change, and the Codex app's app-server is restarted
// for it as for a Connect, Restart the Codex app to follow off too: the
// one the takeover started answers account/read with no account, so the
// app shows none of its ChatGPT projects or chats till it restarts, which
// the user who turned the takeover off to have them back can't tell
// (Herzz____ on X, the Windows app's project list empty). Asked only as
// the takeover ends; false while it isn't set.
var CodexTakeoverOff func() bool

// codexNoAccount is CodexNoAccount, false while it isn't set.
func codexNoAccount() bool {
	return CodexNoAccount != nil && CodexNoAccount()
}

// An app-server ended is not yet one started in its place. The app starts
// another only once it has read the end of the old one's stdout and seen
// it exit (its StdioConnection closes on both, ChatGPT.exe 26.1021's
// app.asar). On Windows every process the app-server started inherits
// that stdout (Rust's Command creates children with bInheritHandles), so
// an MCP server or tool command left running after it holds the pipe
// open: the app never sees it close, starts no other, and every send says
// "Codex app-server process is not available" until Codex is quit and
// opened again (蛋仔 on Discord, 10-30; reproduced on the Windows box
// with codex 0.164's app-server and an MCP server that outlived it: its
// stdout ended only when the MCP server did). So once the ones ended are
// gone, magpie waits codexAppBackWait for a new one; when none comes
// while the app still runs, it has the agent package reopen the app
// (CodexAppReopen), and says it is stuck when that can't be done.
//
// CodexAppOpen, set by the agent package, says whether the Codex desktop
// app runs here; known false when that can't be told. CodexAppReopen
// quits it and opens it again, erring where magpie doesn't (macOS, where
// a reopen opens a new window: the user does it).
var (
	CodexAppOpen   func(ctx context.Context) (open, known bool)
	CodexAppReopen func(ctx context.Context) error
	// codexAppBackWait is how long after the ended app-servers are gone a
	// new one has to start: the app starts one within ~1.5s of the old
	// one's end, hidden in the tray too (its own logs on the Windows box,
	// 10-27 to 10-29)
	codexAppBackWait = 30 * time.Second
)

// codexAppGotNone is what codexAppStep does when no app-server started in
// place of the ones it ended: nothing when the app was quit meanwhile (it
// starts one as it opens), else reopen it. stuck says it runs with none
// and wasn't reopened: the user quits it and opens it again.
func codexAppGotNone(ctx context.Context) (stuck bool) {
	if open, known := codexAppOpenNow(ctx); known && !open {
		return false
	}
	if CodexAppReopen == nil {
		return true
	}
	if err := CodexAppReopen(ctx); err != nil {
		log.Printf("codex: the Codex app started no app-server in place of the one magpie ended; reopening it: %v", err)
		return true
	}
	log.Printf("codex: the Codex app started no app-server in place of the one magpie ended; reopened it")
	return false
}

// codexAppOpenNow is CodexAppOpen, unknown while it isn't set.
func codexAppOpenNow(ctx context.Context) (open, known bool) {
	if CodexAppOpen == nil {
		return false, false
	}
	return CodexAppOpen(ctx)
}

// codexAppEvery is how often KeepCodexAppOnItsProvider looks; codexAppIdle
// how long after Codex's last request through magpie a turn counts ended
// (a tool runs between two of a turn's requests); codexAppTries how often
// one app-server is ended before magpie says it can't.
var (
	codexAppEvery = 3 * time.Second
	codexAppIdle  = 10 * time.Second
	codexAppTries = 3
)

// codexTurnWindow is how long a session file written to last counts as a
// turn maybe still running when it has one started and not ended: a turn
// whose app-server died leaves one open for good.
const codexTurnWindow = 30 * time.Minute

// codexTurnStuckAfter is how long a turn left open on an app-server whose
// account was used up has to have written nothing to Codex's sessions,
// with no Codex request through magpie either, before it counts stuck and
// the app-server is restarted past it. Such a turn can't go on: its next
// request is refused on that account, and when quota ran out mid-turn
// the app sat on it and greyed every thread until restarted (蛋仔 on
// Discord). A tool still running in it loses only a reply that account
// would refuse.
var codexTurnStuckAfter = 2 * time.Minute

// codexTurnSlack is how much earlier than the app-servers' start a turn's
// timestamp may be and still count as theirs: the two clocks are read by
// different programs.
const codexTurnSlack = 2 * time.Second

// codexAppListWait bounds one listing of the app-servers and their homes:
// at a load of 200 a 10s bound ran out on the owner's Mac (the change was
// taken up at the next look, 9s later).
const codexAppListWait = 30 * time.Second

// codexAppServersOn are the pids of the Codex app's own app-servers that
// run on the Codex home home; vars for tests.
var (
	codexAppServersOn = func(ctx context.Context, home string) ([]int, error) {
		ps, err := listProcesses(ctx)
		if err != nil {
			return nil, err
		}
		var out []int
		for _, p := range ps {
			if isCodexAppOwnServer(p.Args) && codexAppHomeIs(ctx, p.PID, home) {
				out = append(out, p.PID)
			}
		}
		return out, nil
	}
	endCodexAppServer  = proc.End
	codexTurnRunning   = func(home string, now, since time.Time) bool { return codexTurnOpen(home, now, since) }
	codexSessionsQuiet = func(home string, now time.Time, d time.Duration) bool { return codexSessionsQuietFor(home, now, d) }
	codexServerStart   = proc.Started
)

// codexServersSince is when the oldest of the app-servers pids started: a
// turn started before it can't be running in any of them. It is zero,
// "unknown", when one's start can't be read, and every open turn counts.
func codexServersSince(pids []int) time.Time {
	var since time.Time
	for _, p := range pids {
		t, err := codexServerStart(p)
		if err != nil || t.IsZero() {
			return time.Time{}
		}
		if since.IsZero() || t.Before(since) {
			since = t
		}
	}
	return since
}

// isCodexAppOwnServer is whether a command line is the Codex app's own
// app-server, which it talks to over stdio: one another program starts
// from the app's bundled codex names where it listens (`app-server
// --listen stdio://`, Miyi Helper on macOS), and is that program's to end.
func isCodexAppOwnServer(args string) bool {
	if !isCodexAppServer(args) {
		return false
	}
	_, rest := splitExe(args)
	for _, f := range strings.Fields(rest) {
		if f == "--listen" || strings.HasPrefix(f, "--listen=") {
			return false
		}
	}
	return true
}

// codexAppHomeIs is whether app-server pid runs on Codex home home: its
// CODEX_HOME, else its HOME's .codex. A system that doesn't tell
// (Windows) counts as yes: one Codex home is the rule there.
func codexAppHomeIs(ctx context.Context, pid int, home string) bool {
	env, err := proc.EnvOf(ctx, pid, "CODEX_HOME", "HOME")
	if errors.Is(err, proc.ErrNoEnv) {
		return true
	}
	if err != nil {
		return false
	}
	h := env["CODEX_HOME"]
	if h == "" && env["HOME"] != "" {
		h = filepath.Join(env["HOME"], ".codex")
	}
	return h != "" && sameDir(h, home)
}

// sameDir is whether a and b name one folder, through links too.
func sameDir(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// codexApp is what KeepCodexAppOnItsProvider knows.
var codexApp struct {
	sync.Mutex
	seen  bool
	route string
	// out: whether the route last seen was magpie as Codex's provider for
	// its used-up account (CodexAppRoute's out)
	out bool
	// stale: the app-servers started before Codex's provider last changed,
	// each with how often it was ended
	stale map[int]int
	// stuck: one of them outlived codexAppTries ends, or may not be ended
	stuck bool
	// switched: Codex's ChatGPT account changed since the app-servers were
	// last listed (codexAppAccountSwitched)
	switched bool
	// auto: every switch since then magpie made by itself
	// (SwitchWhenSpent), none the user made
	auto bool
	// spent: the app-servers are on an account used up, so a turn they
	// left open can't be served by them any more: Codex was switched off
	// it (SwitchWhenSpent, or by hand off one known held, noteCodexSwitch),
	// or magpie became Codex's provider for it (out)
	spent bool
	// ended: the app-servers ended and no new one seen since, and by when
	// one has to start (codexAppBackWait after the last end)
	ended  map[int]bool
	backBy time.Time
	// why: what made the app-servers in stale stale, said in the log line
	// when they are ended, so a crash a user reports can be put down to
	// an account switch, a route change or a used-up account (#4550)
	why string
	// back: the app-servers left running on magpie as Codex's provider
	// when magpie gave Codex back its ChatGPT account at the reset, with
	// Restart the Codex app to follow off (CodexAppBack), and when they
	// were last listed
	back   map[int]bool
	backAt time.Time
	// crashSince, crashBy: magpie last ended the app's app-server at
	// crashSince, and until crashBy its logs are read for the page the
	// app shows when it can't take another (codexAppCrashStep); snag:
	// the app showed that page and magpie couldn't reopen it, while the
	// app-servers in snagPids (those running then) run
	crashSince, crashBy time.Time
	snag                bool
	snagPids            []int
}

// codexAppBackEvery is how often the app-servers left on the takeover
// are listed again to see whether they went: a listing is a PowerShell
// run on Windows.
var codexAppBackEvery = 30 * time.Second

// CodexAppBack is whether the Codex app still runs on the app-server it
// started while magpie was Codex's provider for its used-up account, now
// that magpie gave Codex back its ChatGPT account and left the app
// running (Restart the Codex app to follow off, #4550). That app-server
// read account/read as it started, when Codex's provider took no ChatGPT
// account, so the app shows no account, no usage meter and none of its
// ChatGPT side until it restarts: the Agents row says so and offers the
// restart (練習^1.0beta by mail, 11-04: the panels loaded only after
// `magpie codex default` and a restart).
func CodexAppBack() bool {
	c := &codexApp
	c.Lock()
	defer c.Unlock()
	return len(c.back) > 0
}

// checkCodexAppBack forgets the app-servers left on the takeover that are
// gone: the app started another, which reads the account afresh, or was
// quit. A listing that fails keeps what was known.
func checkCodexAppBack(ctx context.Context, now time.Time, home string) {
	c := &codexApp
	c.Lock()
	due := len(c.back) > 0 && now.Sub(c.backAt) >= codexAppBackEvery
	c.Unlock()
	if !due {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, codexAppListWait)
	pids, err := codexAppServersOn(lctx, home)
	cancel()
	if err != nil {
		return
	}
	c.Lock()
	defer c.Unlock()
	c.backAt = now
	for p := range c.back {
		if !slices.Contains(pids, p) {
			delete(c.back, p)
		}
	}
}

// CodexAppStuck is whether a Codex app is left on the provider it started
// with, which magpie couldn't restart: the user quits and opens it.
func CodexAppStuck() bool {
	c := &codexApp
	c.Lock()
	defer c.Unlock()
	return c.stuck
}

// codexAppAccountSwitched has the Codex app's own app-servers, left on the
// ChatGPT account Codex was switched away from (noteCodexSwitch), ended
// as a route change's are, once no Codex turn runs: the app starts
// another, which reads auth.json and the new account afresh, so the app
// shows that account's limits and sends on it with no quit and reopen
// (zhibukaixinyao on Discord: Windows has no Restart button). One
// switched away and back before then is restarted all the same, which
// costs the app nothing it shows.
//
// They are listed at codexAppStep's next look, as a route change's are:
// every one on Codex's home, by the test a route change uses, and listed
// again at the look after when the listing fails. The pid the switch
// itself saw was the first app-server-looking process in a five-second
// listing, which was another program's (Miyi Helper's `--listen stdio://`
// one, listed ahead of the app's on the owner's Mac) or none when ps was
// slow, and then nothing was ended and the app stayed grey on the used-up
// account till the user restarted it (danzi002 on Discord).
func codexAppAccountSwitched() { codexAppSwitched(false, false) }

// codexAppSpentSwitch is codexAppAccountSwitched for a switch off an
// account that was used up: a turn the app-server left open is restarted
// past too once it has gone quiet (codexTurnStuckAfter).
func codexAppSpentSwitch() { codexAppSwitched(true, false) }

// codexAppSwitched notes a switch of Codex's account: spent, off one used
// up; byMagpie, made by magpie itself (SwitchWhenSpent). magpie's switch
// back restarts the app only with RestartApp on; its switch off a spent
// account, and one the user made among them, restart it all the same.
func codexAppSwitched(spent, byMagpie bool) {
	c := &codexApp
	c.Lock()
	defer c.Unlock()
	c.auto = byMagpie && (!c.switched || c.auto)
	c.switched, c.spent = true, c.spent || spent
}

// KeepCodexAppOnItsProvider runs codexAppStep every codexAppEvery till
// ctx ends. busy says how many of Codex's requests are in flight through
// magpie and when the last ended.
func KeepCodexAppOnItsProvider(ctx context.Context, busy func() (int, time.Time)) {
	t := time.NewTicker(codexAppEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			codexAppStep(ctx, now, busy)
		}
	}
}

// codexAppStep notes the app-servers running as Codex's route changes
// (magpie becomes Codex's provider for the used-up account, or stops being
// it; Codex is connected or disconnected), and ends each of them once no
// Codex turn is running. Started with magpie on, the app may
// have opened before: its app-servers are ended too. It says which pids it
// ended.
func codexAppStep(ctx context.Context, now time.Time, busy func() (int, time.Time)) []int {
	codexAppCrashStep(ctx, now, busy)
	if CodexAppRoute == nil {
		return nil
	}
	route, out, known := CodexAppRoute()
	if !known {
		return nil
	}
	home := codexHome()
	c := &codexApp
	c.Lock()
	switched := c.switched
	wasOut := c.seen && c.out
	c.Unlock()
	// the takeover ending now: by the user's hand (CodexTakeoverOff), or
	// by the reset, which is magpie's own change
	offByHand := wasOut && !out && CodexTakeoverOff != nil && CodexTakeoverOff()
	// an account switch is nothing to an app-server on a provider that
	// takes no ChatGPT account (CodexNoAccount): it is forgotten, and a
	// route change later restarts it all the same
	if switched && codexNoAccount() {
		log.Printf("codex: Codex's account was switched while its provider takes no ChatGPT account; the Codex app's app-server is left running")
		c.Lock()
		c.switched, c.spent = false, false
		c.Unlock()
	}
	c.Lock()
	// at the first look only a used-up account's app-servers are ended:
	// what the others read before magpie started isn't known
	changed := !c.seen && out || c.seen && c.route != route || c.switched
	// magpie becoming Codex's provider for the used-up account: the
	// app-servers are on that account, and a turn left open on it when
	// quota ran out mid-turn would hold their restart, and the app's grey
	// Send, for up to codexTurnWindow (xiaoq6285 on Discord)
	into := changed && out && (!c.seen || !c.out)
	// what changed magpie changed by itself and the app works without:
	// the switch back once the account has room, and the takeover out at
	// the reset. The ChatGPT app shows the end of its app-server as an
	// error and relaunches (#4550), so that waits for the user to restart
	// it unless they asked magpie to (RestartApp; atom0964 on Discord: both
	// of Codex's sign-in ticks off, and magpie still restarted the app to
	// switch accounts). A switch the user made, a Connect or a Disconnect
	// is theirs, and restarts it, and so does the takeover turned off by
	// hand (CodexTakeoverOff). So does whatever leaves the app-server on
	// a used-up account: the takeover in, the first look on one and a
	// switch off one. The app greys Send for every model while that
	// account's rate limit says not allowed and reads the account only as
	// its app-server starts (8b0132b3d), so nothing magpie serves would
	// reach it until a restart.
	byHand := c.switched && !c.auto || c.seen && c.route != route && c.out == out || offByHand
	needed := into || c.switched && c.spent
	// left on the takeover: the app-servers it started while magpie was
	// Codex's provider, which show no account till they restart
	leftBack := false
	if changed && !byHand && !needed && !codexRestartsApp() {
		why := "magpie switched Codex's ChatGPT account back"
		if !c.switched {
			why = "magpie gave Codex back its ChatGPT account after the reset"
			leftBack = c.out && !out
		}
		log.Printf("codex: %s; the Codex app's app-server is left running (Restart the Codex app to follow is off), and the app follows once it is restarted", why)
		c.switched, c.auto = false, false
		changed = false
	}
	if !changed {
		c.seen, c.route, c.out = true, route, out
	}
	if changed {
		// whatever restarts them now ends the takeover's too
		c.back = nil
	}
	c.Unlock()
	if leftBack {
		lctx, cancel := context.WithTimeout(ctx, codexAppListWait)
		pids, err := codexAppServersOn(lctx, home)
		cancel()
		if err == nil && len(pids) > 0 {
			c.Lock()
			c.back, c.backAt = map[int]bool{}, now
			for _, p := range pids {
				c.back[p] = true
			}
			c.Unlock()
		}
	} else {
		checkCodexAppBack(ctx, now, home)
	}
	if changed {
		lctx, cancel := context.WithTimeout(ctx, codexAppListWait)
		pids, err := codexAppServersOn(lctx, home)
		cancel()
		if err != nil {
			// unknown: the change is taken up again at the next look
			log.Printf("codex: listing the Codex app's app-servers: %v", err)
			return nil
		}
		c.Lock()
		switch {
		case c.switched:
			c.why = "Codex's ChatGPT account was switched"
		case into:
			c.why = "Codex's account is used up, so magpie serves it"
		default:
			c.why = "where Codex's config sends it changed"
		}
		c.spent = c.spent || into
		c.seen, c.route, c.out, c.switched, c.auto = true, route, out, false, false
		c.stale, c.stuck = map[int]int{}, false
		for _, p := range pids {
			c.stale[p] = 0
		}
		c.Unlock()
	}
	c.Lock()
	n := len(c.stale) + len(c.ended)
	c.Unlock()
	if n == 0 {
		return nil
	}
	lctx, cancel := context.WithTimeout(ctx, codexAppListWait)
	pids, err := codexAppServersOn(lctx, home)
	cancel()
	if err != nil {
		return nil
	}
	running := map[int]bool{}
	for _, p := range pids {
		running[p] = true
	}
	c.Lock()
	for p := range c.stale {
		if !running[p] {
			// gone: the one the app starts in its place reads the
			// provider afresh, once it starts (c.ended)
			delete(c.stale, p)
		}
	}
	none := false
	if len(c.ended) > 0 {
		// any other app-server running serves the app; one ended and
		// still running is waited for, and the wait counts from its end
		back, left := false, false
		for _, p := range pids {
			back = back || !c.ended[p]
			left = left || c.ended[p]
		}
		switch {
		case back:
			c.ended = nil
		case left:
			c.backBy = now.Add(codexAppBackWait)
		case !now.Before(c.backBy):
			c.ended, none = nil, true
		}
	}
	n = len(c.stale)
	c.Unlock()
	if none {
		if codexAppGotNone(ctx) {
			c.Lock()
			c.stuck = true
			c.Unlock()
		}
		return nil
	}
	if n == 0 {
		c.Lock()
		if len(c.ended) == 0 {
			c.spent = false
		}
		c.Unlock()
		return nil
	}
	inFlight, last := busy()
	if inFlight > 0 || now.Sub(last) < codexAppIdle {
		return nil
	}
	c.Lock()
	stale := make([]int, 0, len(c.stale))
	for p := range c.stale {
		stale = append(stale, p)
	}
	spent := c.spent
	c.Unlock()
	if codexTurnRunning(home, now, codexServersSince(stale)) {
		if !spent || now.Sub(last) < codexTurnStuckAfter || !codexSessionsQuiet(home, now, codexTurnStuckAfter) {
			return nil
		}
		log.Printf("codex: a Codex turn left open on the used-up account has done nothing for %s; restarting the Codex app's app-server past it", codexTurnStuckAfter)
	}
	var ended []int
	c.Lock()
	defer c.Unlock()
	for p, tries := range c.stale {
		if tries >= codexAppTries {
			c.stuck = true
			continue
		}
		c.stale[p] = tries + 1
		if err := endCodexAppServer(p); err != nil {
			log.Printf("codex: restarting the Codex app's app-server %d on its provider: %v", p, err)
			if errors.Is(err, proc.ErrDenied) {
				c.stale[p], c.stuck = codexAppTries, true
			}
			continue
		}
		ended = append(ended, p)
		if c.ended == nil {
			c.ended = map[int]bool{}
		}
		c.ended[p], c.backBy = true, now.Add(codexAppBackWait)
	}
	if len(ended) > 0 {
		log.Printf("codex: ended the Codex app's app-server %v for the app to start another: %s", ended, c.why)
		watchCodexAppCrashLocked(now)
	}
	return ended
}

// codexTurnOpen is whether a Codex session under home has a turn started
// and not ended (task_started after its last task_complete or
// turn_aborted) in a file written to within codexTurnWindow of now: the
// app-server running it would cut it short. A turn started before since,
// when the app-servers about to be ended started, ran in an app-server
// gone since (force quit, crashed): a SIGKILLed one writes no
// turn_aborted, and opening that thread again in the app writes
// thread_settings_applied, so the file stays in the window as long as
// the user looks at it. Counting it kept a switched account from ever
// reaching the app (蛋仔 on Discord, 10-29 and 10-30: "点了会话再切换账号
// 就重启不了"). A zero since counts every open turn.
func codexTurnOpen(home string, now, since time.Time) bool {
	open := false
	_ = filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || open {
			if open {
				return fs.SkipAll
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := sessionFileInfo(path)
		if err != nil || now.Sub(info.ModTime()) > codexTurnWindow {
			return nil
		}
		open = sessionTurnOpen(path, since)
		return nil
	})
	return open
}

// codexSessionsQuietFor is whether no Codex session file under home was
// written in the last d. A sessions folder that can't be read counts as
// written: a turn cut short costs more than a restart a little later.
func codexSessionsQuietFor(home string, now time.Time, d time.Duration) bool {
	quiet := true
	err := filepath.WalkDir(filepath.Join(home, "sessions"), func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			return nil
		}
		info, err := sessionFileInfo(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if now.Sub(info.ModTime()) < d {
			quiet = false
			return fs.SkipAll
		}
		return nil
	})
	return quiet && err == nil
}

// sessionFileInfo is the session file at path's own time and size, never
// the folder listing's (a fs.DirEntry's Info): Windows stamps a file in
// its folder only as it is closed, and Codex keeps a session's file open
// while the app-server runs it, so the listing has the time it was
// opened (os.ReadDir on the Windows box: a file held open and written
// to an hour after its stamp still listed the stamp, at its old size).
// A turn running in a thread opened more than codexTurnWindow before
// then counted as no turn, and its app-server was ended under it.
func sessionFileInfo(path string) (fs.FileInfo, error) { return os.Lstat(path) }

// sessionTurnTail is how much of a session file's end sessionTurnOpen reads.
const sessionTurnTail = 512 << 10

// sessionTurnOpen is whether the session file at path ends inside a turn
// started at since or later. A file that can't be read, or a turn whose
// start can't be read, counts as open: a turn cut short costs more than
// an app greyed a few seconds more.
func sessionTurnOpen(path string, since time.Time) bool {
	f, err := os.Open(path)
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > sessionTurnTail {
		if _, err := f.Seek(info.Size()-sessionTurnTail, io.SeekStart); err != nil {
			return true
		}
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return true
	}
	started := bytes.LastIndex(b, []byte(`"type":"task_started"`))
	ended := max(bytes.LastIndex(b, []byte(`"type":"task_complete"`)), bytes.LastIndex(b, []byte(`"type":"turn_aborted"`)))
	if started <= ended {
		return false
	}
	if since.IsZero() {
		return true
	}
	at, ok := sessionLineTime(b, started)
	return !ok || !at.Before(since.Add(-codexTurnSlack))
}

// sessionLineTime is the "timestamp" of the session file line holding
// offset i of b, which Codex writes on every rollout line.
func sessionLineTime(b []byte, i int) (time.Time, bool) {
	line := b[bytes.LastIndexByte(b[:i], '\n')+1:]
	if j := bytes.IndexByte(line, '\n'); j >= 0 {
		line = line[:j]
	}
	var l struct {
		Timestamp time.Time `json:"timestamp"`
	}
	if json.Unmarshal(line, &l) != nil || l.Timestamp.IsZero() {
		return time.Time{}, false
	}
	return l.Timestamp, true
}

// codexAppGoneWait is how long RestartCodexApp waits for the app-servers
// it ended to be gone; a var for tests.
var codexAppGoneWait = 10 * time.Second

// RestartCodexApp restarts the app-server the Codex desktop app at bundle
// (/Applications/ChatGPT.app) runs for itself, as codexAppStep does: it is
// ended as a clean exit, and the app starts another, which reads Codex's
// config and model list afresh, while the app's window, threads and drafts
// stay as they are. The Agents row's Restart quit the app and opened it
// again, which opened a new window (yoshi037070 on Discord, 10-27). An
// app-server another program runs from the bundle (--listen) is left
// alone. It waits until the ones ended are gone and the app has started
// another (codexAppBackWait), and errs when none ran or none started.
func RestartCodexApp(ctx context.Context, bundle string) error {
	ps, err := listProcesses(ctx)
	if err != nil {
		return err
	}
	in := strings.TrimSuffix(bundle, "/") + "/Contents/"
	var ended []int
	for _, p := range ps {
		if !strings.HasPrefix(strings.TrimSpace(p.Args), in) || !isCodexAppOwnServer(p.Args) {
			continue
		}
		if err := endCodexAppServer(p.PID); err != nil {
			return err
		}
		ended = append(ended, p.PID)
		watchCodexAppCrash(time.Now())
	}
	if len(ended) == 0 {
		return errors.New("the Codex app's app-server isn't running")
	}
	gone, back, err := codexAppBack(ctx, func(ctx context.Context) ([]int, error) {
		ps, err := listProcesses(ctx)
		var pids []int
		for _, p := range ps {
			if strings.HasPrefix(strings.TrimSpace(p.Args), in) && isCodexAppOwnServer(p.Args) {
				pids = append(pids, p.PID)
			}
		}
		return pids, err
	}, ended)
	switch {
	case err != nil:
		return err
	case !gone:
		return errors.New("the Codex app's app-server didn't end")
	case !back:
		return errors.New("the Codex app started no new app-server: quit it and open it again")
	}
	return nil
}

// codexAppPoll is how often codexAppBack lists the app-servers.
var codexAppPoll = 200 * time.Millisecond

// codexAppBack waits for the app-servers ended to be gone, for
// codexAppGoneWait, and then for another of list's to run, for
// codexAppBackWait: gone and back say which came. A listing that fails
// is tried again.
func codexAppBack(ctx context.Context, list func(context.Context) ([]int, error), ended []int) (gone, back bool, err error) {
	end := time.Now().Add(codexAppGoneWait)
	for {
		pids, err := list(ctx)
		if err == nil {
			left := slices.ContainsFunc(pids, func(p int) bool { return slices.Contains(ended, p) })
			if !gone && !left {
				gone, end = true, time.Now().Add(codexAppBackWait)
			}
			if slices.ContainsFunc(pids, func(p int) bool { return !slices.Contains(ended, p) }) && !left {
				return true, true, nil
			}
		}
		if time.Now().After(end) {
			return gone, false, nil
		}
		select {
		case <-ctx.Done():
			return gone, false, ctx.Err()
		case <-time.After(codexAppPoll):
		}
	}
}

// RestartCodexAppWhenIdle ends the Codex app's own app-servers once, for a magpie
// that won't be running to watch them (magpie uninstall): the app starts
// a new one, which reads Codex's config as it is now. It waits up to wait
// for a running Codex turn to end, and cuts none short: left says an
// app-server was still on the old config when it gave up, or couldn't be
// ended, or that the app started none in its place and couldn't be
// reopened (codexAppGotNone), so the user quits and opens the app.
func RestartCodexAppWhenIdle(ctx context.Context, wait time.Duration) (ended int, left bool, err error) {
	home := codexHome()
	lctx, cancel := context.WithTimeout(ctx, codexAppListWait)
	pids, err := codexAppServersOn(lctx, home)
	cancel()
	if err != nil || len(pids) == 0 {
		return 0, false, err
	}
	until := time.Now().Add(wait)
	since := codexServersSince(pids)
	for codexTurnRunning(home, time.Now(), since) {
		if time.Now().After(until) {
			return 0, true, nil
		}
		select {
		case <-ctx.Done():
			return 0, true, ctx.Err()
		case <-time.After(codexAppEvery):
		}
	}
	var gone []int
	for _, p := range pids {
		if err := endCodexAppServer(p); err != nil {
			log.Printf("codex: restarting the Codex app's app-server %d: %v", p, err)
			left = true
			continue
		}
		gone = append(gone, p)
	}
	if len(gone) > 0 {
		went, back, err := codexAppBack(ctx, func(ctx context.Context) ([]int, error) {
			lctx, cancel := context.WithTimeout(ctx, codexAppListWait)
			defer cancel()
			return codexAppServersOn(lctx, home)
		}, gone)
		switch {
		case err != nil:
			return len(gone), true, err
		case !went:
			left = true
		case !back:
			left = left || codexAppGotNone(ctx)
		}
	}
	return len(gone), left, nil
}

// What RestartSwitchedCodexApp found when it couldn't restart the app's
// app-server; the agent package says each in the reader's language.
var (
	ErrCodexAppGone    = errors.New("the Codex app isn't running")
	ErrCodexAppNoEnd   = errors.New("the Codex app's app-server couldn't be ended")
	ErrCodexAppNotBack = errors.New("the Codex app didn't start another app-server")
)

// RestartSwitchedCodexApp is the Restart on Codex's accounts' "the Codex
// app is still signed in as" notice (nb5p_hQ on X): it restarts the app's
// own app-servers now, as codexAppStep does once no Codex turn runs, so
// the app reads the account Codex was switched to; the user said yes to
// cutting a running turn short. Each is ended as a clean exit, which the
// app answers by starting another (its window, threads and drafts stay;
// quitting the app and opening it again opened a new window, yoshi037070);
// it is never killed, as the app shows a kill as a crash. It waits until
// the ended ones are gone and a new one runs, and errs when either doesn't
// happen in time: a listing that fails is unknown, never success.
func RestartSwitchedCodexApp(ctx context.Context) error {
	home := codexHome()
	list := func() ([]int, error) {
		lctx, cancel := context.WithTimeout(ctx, codexAppListWait)
		defer cancel()
		return codexAppServersOn(lctx, home)
	}
	pids, err := list()
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		DismissCodexApp()
		return ErrCodexAppGone
	}
	for _, p := range pids {
		if err := endCodexAppServer(p); err != nil {
			log.Printf("codex: restarting the Codex app's app-server %d for the account switched to: %v", p, err)
			return ErrCodexAppNoEnd
		}
		watchCodexAppCrash(time.Now())
	}
	// waitFor lists the app-servers until ok holds of them, for at most limit
	waitFor := func(limit time.Duration, ok func([]int) bool) (bool, error) {
		end := time.Now().Add(limit)
		for {
			now, err := list()
			if err == nil && ok(now) {
				return true, nil
			}
			if time.Now().After(end) {
				return false, err
			}
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	gone, err := waitFor(codexAppGoneWait, func(now []int) bool {
		return !slices.ContainsFunc(now, func(p int) bool { return slices.Contains(pids, p) })
	})
	if !gone {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		return ErrCodexAppNoEnd
	}
	back, err := waitFor(codexAppBackWait, func(now []int) bool { return len(now) > 0 })
	if !back {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return err
		}
		// the Windows app whose old app-server's stdout is held open
		// starts none till it is quit and opened again (codexAppGotNone)
		if open, known := codexAppOpenNow(ctx); known && !open {
			return ErrCodexAppGone
		}
		if codexAppGotNone(ctx) {
			return ErrCodexAppNotBack
		}
	}
	DismissCodexApp()
	codexApp.Lock()
	codexApp.back = nil // the new one reads the account (CodexAppBack)
	codexApp.Unlock()
	return nil
}

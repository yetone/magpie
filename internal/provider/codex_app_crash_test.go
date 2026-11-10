package provider

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// crashStub has the Codex app's logs say it showed its error page at at,
// from the end on, and counts the relaunches.
type crashStub struct {
	page     bool
	at       time.Time
	sinces   []time.Time
	relaunch int
	err      error
	unknown  bool
}

func stubCodexCrash(t *testing.T) *crashStub {
	t.Helper()
	c := &crashStub{}
	CodexAppCrashed = func(since time.Time) (time.Time, bool, bool) {
		c.sinces = append(c.sinces, since)
		if c.unknown {
			return time.Time{}, false, false
		}
		if c.page && !c.at.Before(since) {
			return c.at, true, true
		}
		return time.Time{}, false, true
	}
	CodexAppRelaunch = func(context.Context) error {
		c.relaunch++
		return c.err
	}
	return c
}

// ysm119 on Discord: each restart magpie made of the Codex app's
// app-server left a signed-in ChatGPT app on its "ChatGPT hit a snag"
// page, which only quitting and opening it again ends (#4550). magpie
// reads the app's logs after its end and reopens the app once, when no
// turn has started since; it does nothing when the app logged no page.
func TestCodexAppReopenedWhenItShowsItsErrorPage(t *testing.T) {
	signIn(t)
	s := stubCodexApp(t)
	c := stubCodexCrash(t)
	now := time.Now()
	idle := func() (int, time.Time) { return 0, now.Add(-time.Minute) }
	step := func() []int { return codexAppStep(context.Background(), now, idle) }

	s.running = []int{60}
	step()
	if len(c.sinces) > 0 {
		t.Fatalf("the logs read with nothing ended: %v", c.sinces)
	}
	s.on = true
	if got := step(); !slices.Equal(got, []int{60}) {
		t.Fatalf("ended %v, want [60]", got)
	}
	ended := now
	// no page: nothing done, and the watch ends with its window
	now = now.Add(3 * time.Second)
	step()
	if c.relaunch != 0 || len(c.sinces) != 1 || !c.sinces[0].Equal(ended) {
		t.Fatalf("no page: relaunched %d, logs read since %v", c.relaunch, c.sinces)
	}
	now = ended.Add(codexAppCrashWait)
	step()
	n := len(c.sinces)
	now = now.Add(3 * time.Second)
	step()
	if len(c.sinces) != n {
		t.Fatal("the logs still read after the watch's window")
	}

	// the page, logged two seconds after the end: reopened once
	s.on = false
	ended = now
	if got := step(); !slices.Equal(got, []int{100}) {
		t.Fatalf("ended %v, want [100]", got)
	}
	c.page, c.at = true, ended.Add(2*time.Second)
	now = now.Add(3 * time.Second)
	step()
	if c.relaunch != 1 || CodexAppSnagged() {
		t.Fatalf("the error page: relaunched %d, snag %v", c.relaunch, CodexAppSnagged())
	}
	now = now.Add(3 * time.Second)
	step()
	if c.relaunch != 1 {
		t.Fatalf("relaunched %d times for one page", c.relaunch)
	}
}

// A page logged before magpie's end is none of magpie's: the app showed
// it for its own reasons, and the user has it in front of them.
func TestCodexAppErrorPageBeforeTheEndIsLeft(t *testing.T) {
	signIn(t)
	s := stubCodexApp(t)
	c := stubCodexCrash(t)
	now := time.Now()
	idle := func() (int, time.Time) { return 0, now.Add(-time.Minute) }
	s.running, s.on = []int{60}, true
	c.page, c.at = true, now.Add(-time.Minute)
	codexAppStep(context.Background(), now, idle)
	now = now.Add(3 * time.Second)
	codexAppStep(context.Background(), now, idle)
	if c.relaunch != 0 {
		t.Fatalf("relaunched %d for a page before the end", c.relaunch)
	}
}

// The app that came back by itself (its Try again) or is in use is never
// quit: a Codex turn open since the page, or a request through magpie
// since or in flight, leaves it running.
func TestCodexAppInUseAfterItsErrorPageIsLeft(t *testing.T) {
	for _, tc := range []struct {
		name string
		turn bool
		busy func(at time.Time) (int, time.Time)
	}{
		{name: "turn open", turn: true},
		{name: "request in flight", busy: func(at time.Time) (int, time.Time) { return 1, at.Add(-time.Minute) }},
		{name: "request since", busy: func(at time.Time) (int, time.Time) { return 0, at.Add(time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signIn(t)
			s := stubCodexApp(t)
			c := stubCodexCrash(t)
			now := time.Now()
			idle := func() (int, time.Time) { return 0, now.Add(-time.Minute) }
			s.running, s.on = []int{60}, true
			codexAppStep(context.Background(), now, idle)
			c.page, c.at = true, now.Add(time.Second)
			var since []time.Time
			codexTurnRunning = func(_ string, _, from time.Time) bool {
				since = append(since, from)
				return tc.turn
			}
			busy := idle
			if tc.busy != nil {
				busy = func() (int, time.Time) { return tc.busy(c.at) }
			}
			now = now.Add(3 * time.Second)
			codexAppStep(context.Background(), now, busy)
			if c.relaunch != 0 {
				t.Fatalf("relaunched an app in use")
			}
			if tc.turn && (len(since) == 0 || !since[0].Equal(c.at)) {
				t.Fatalf("turns read since %v, want the page's time %v", since, c.at)
			}
		})
	}
}

// Where the app can't be reopened (Linux, a quit it didn't take) the
// Agents row says to quit it and open it again, until the app-servers
// that ran at the page are gone or the app is closed.
func TestCodexAppSnagSaidTillTheAppRestarts(t *testing.T) {
	signIn(t)
	s := stubCodexApp(t)
	c := stubCodexCrash(t)
	c.err = errors.New("still open")
	now := time.Now()
	idle := func() (int, time.Time) { return 0, now.Add(-time.Minute) }
	step := func() { codexAppStep(context.Background(), now, idle) }
	s.running, s.on = []int{60}, true
	step()
	c.page, c.at = true, now.Add(time.Second)
	now = now.Add(3 * time.Second)
	step()
	if c.relaunch != 1 || !CodexAppSnagged() {
		t.Fatalf("relaunched %d, snag %v", c.relaunch, CodexAppSnagged())
	}
	// a listing that fails is unknown: still said
	s.listErr = errors.New("ps")
	now = now.Add(3 * time.Second)
	step()
	if !CodexAppSnagged() {
		t.Fatal("a failed listing cleared the snag")
	}
	s.listErr = nil
	step()
	if !CodexAppSnagged() || c.relaunch != 1 {
		t.Fatalf("the same app-server running: snag %v, relaunched %d", CodexAppSnagged(), c.relaunch)
	}
	s.running = []int{200} // quit and opened by the user
	now = now.Add(3 * time.Second)
	step()
	if CodexAppSnagged() {
		t.Fatal("still said after the app started again")
	}

	// closed: no longer said
	codexApp.Lock()
	codexApp.snag, codexApp.snagPids = true, []int{200}
	codexApp.Unlock()
	CodexAppOpen = func(context.Context) (bool, bool) { return false, true }
	step()
	if CodexAppSnagged() {
		t.Fatal("still said with the app closed")
	}
}

// The Agents row's Restart and the switch notice's Restart end the
// app-server themselves: the logs are read after them too.
func TestCodexAppRestartsWatchForTheErrorPage(t *testing.T) {
	signIn(t)
	s := stubCodexApp(t)
	c := stubCodexCrash(t)
	oldWait, oldGone := codexAppBackWait, codexAppGoneWait
	t.Cleanup(func() { codexAppBackWait, codexAppGoneWait = oldWait, oldGone })
	codexAppBackWait, codexAppGoneWait = time.Second, time.Second
	s.running = []int{60}
	before := time.Now()
	if err := RestartSwitchedCodexApp(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.page, c.at = true, time.Now()
	now := time.Now().Add(3 * time.Second)
	codexAppStep(context.Background(), now, func() (int, time.Time) { return 0, before.Add(-time.Minute) })
	if c.relaunch != 1 {
		t.Fatalf("the switch notice's Restart: relaunched %d", c.relaunch)
	}
	if len(c.sinces) == 0 || c.sinces[0].Before(before) {
		t.Fatalf("logs read since %v, want from the end on", c.sinces)
	}
}

// A watch whose logs can't be read is kept to its window and never
// reopens the app.
func TestCodexAppUnreadLogsReopenNothing(t *testing.T) {
	signIn(t)
	s := stubCodexApp(t)
	c := stubCodexCrash(t)
	c.unknown = true
	now := time.Now()
	idle := func() (int, time.Time) { return 0, now.Add(-time.Minute) }
	s.running, s.on = []int{60}, true
	codexAppStep(context.Background(), now, idle)
	for range 3 {
		now = now.Add(codexAppCrashWait)
		codexAppStep(context.Background(), now, idle)
	}
	if c.relaunch != 0 || CodexAppSnagged() {
		t.Fatalf("unread logs: relaunched %d, snag %v", c.relaunch, CodexAppSnagged())
	}
}

// ysm119's case: the Agents row's Restart (RestartCodexApp) ends the app's
// own app-server, and the page the app shows after it is read and the app
// reopened.
func TestCodexAppRowRestartWatchesForTheErrorPage(t *testing.T) {
	signIn(t)
	stubCodexApp(t)
	c := stubCodexCrash(t)
	const bundle = "/Applications/ChatGPT.app"
	ps := []proc.Process{{PID: 2, Args: bundle + "/Contents/Resources/codex app-server --analytics-default-enabled"}}
	oldList, oldEnd, oldPoll := listProcesses, endCodexAppServer, codexAppPoll
	t.Cleanup(func() { listProcesses, endCodexAppServer, codexAppPoll = oldList, oldEnd, oldPoll })
	codexAppPoll = time.Millisecond
	listProcesses = func(context.Context) ([]proc.Process, error) { return slices.Clone(ps), nil }
	endCodexAppServer = func(pid int) error { ps[0].PID = 102; return nil }
	before := time.Now()
	if err := RestartCodexApp(context.Background(), bundle); err != nil {
		t.Fatal(err)
	}
	c.page, c.at = true, time.Now()
	codexAppStep(context.Background(), time.Now().Add(3*time.Second), func() (int, time.Time) { return 0, before.Add(-time.Minute) })
	if c.relaunch != 1 {
		t.Fatalf("the Agents row's Restart: relaunched %d", c.relaunch)
	}
}

// Michael25651150 on X: "额度用尽时会强制重启 codex 客户端". With the
// user's "Restart the Codex app" off (as it ships), quota running out
// still restarts the app's app-server, which the takeover needs, but the
// app showing its error page after it is never quit and opened again
// unasked: the Agents row says to, and the app's Try again or the row's
// Restart is the user's. Nor is a Windows app that started no app-server.
func TestCodexAppNotQuitUnasked(t *testing.T) {
	signIn(t)
	s := stubCodexApp(t)
	c := stubCodexCrash(t)
	codexRestartsApp = func() bool { return false }
	now := time.Now()
	idle := func() (int, time.Time) { return 0, now.Add(-time.Minute) }
	s.running = []int{60}
	codexAppStep(context.Background(), now, idle)
	s.on = true // the account used up: magpie serves it
	if got := codexAppStep(context.Background(), now, idle); !slices.Equal(got, []int{60}) {
		t.Fatalf("ended %v, want [60]: the takeover needs the app-server's restart", got)
	}
	c.page, c.at = true, now.Add(2*time.Second)
	now = now.Add(3 * time.Second)
	codexAppStep(context.Background(), now, idle)
	if c.relaunch != 0 || !CodexAppSnagged() {
		t.Fatalf("relaunched %d, snag %v: want left to the user, said on the row", c.relaunch, CodexAppSnagged())
	}
	reopened := 0
	CodexAppReopen = func(context.Context) error { reopened++; return nil }
	if stuck := codexAppGotNone(context.Background()); !stuck || reopened != 0 {
		t.Fatalf("no app-server: reopened %d, stuck %v", reopened, stuck)
	}
}

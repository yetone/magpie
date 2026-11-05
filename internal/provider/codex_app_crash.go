package provider

import (
	"context"
	"errors"
	"log"
	"slices"
	"time"
)

// A signed-in ChatGPT app (26.1021 and on) can't always take the
// app-server magpie ended: the one it starts in its place answers its
// handshake, and then the app's own Hosted AppServerManager finds the
// account it had cached cleared with the old connection ("Hosted
// AppServerManager identity is not ready"), logs fatal_error_broadcasted
// and shows "ChatGPT hit a snag / Please try again" over the whole window
// till it is quit and opened again (ysm119 on Discord, #4550: sljdxde,
// wang9262, Indekkusu545). That is in the app, out of magpie's reach; the
// app-server's own exit is clean (code 0, which it counts expected).
//
// So for codexAppCrashWait after magpie ends an app-server, its logs are
// read for that line (CodexAppCrashed, the agent package's: the app's own
// log folder, a line stamped after the end). When the app shows the page
// and no Codex turn has started since nor a request gone through magpie
// (the page has no composer: one that ran means it came back), magpie
// quits the app and opens it again, once (CodexAppRelaunch: ⌘Q's quit on
// macOS, never forced; the Agents row's Restart on Windows). Where that
// can't be done, or the app didn't quit, the Agents row says to (snag,
// CodexAppSnagged) until the app-servers seen then are gone.

// CodexAppCrashed and CodexAppRelaunch are set by the agent package.
// CodexAppCrashed says whether the Codex app logged the page it shows
// when it can't go on (fatal_error_broadcasted) at or after since, and
// when; known false when its logs can't be read. CodexAppRelaunch quits
// the app and opens it again, erring where it can't.
var (
	CodexAppCrashed  func(since time.Time) (at time.Time, crashed, known bool)
	CodexAppRelaunch func(ctx context.Context) error
	// codexAppCrashWait is how long after an end the logs are read: the
	// app logs the page within seconds of the new app-server's start
	codexAppCrashWait = 60 * time.Second
)

// watchCodexAppCrash starts reading the app's logs for the page, from
// now, or carries a watch already running on to now's window.
func watchCodexAppCrash(now time.Time) {
	c := &codexApp
	c.Lock()
	defer c.Unlock()
	watchCodexAppCrashLocked(now)
}

func watchCodexAppCrashLocked(now time.Time) {
	c := &codexApp
	if c.crashBy.IsZero() || c.crashSince.After(now) {
		c.crashSince = now
	}
	c.crashBy = now.Add(codexAppCrashWait)
}

// CodexAppSnagged is whether the Codex app showed the page it shows when
// it can't go on after magpie restarted its app-server, and magpie
// couldn't reopen it: the user quits it and opens it again.
func CodexAppSnagged() bool {
	c := &codexApp
	c.Lock()
	defer c.Unlock()
	return c.snag
}

// codexAppCrashStep is codexAppStep's look at the app after an end: it
// reads the logs while a watch runs, reopens the app that shows the page,
// and forgets a snag once the app-servers running at it are gone.
func codexAppCrashStep(ctx context.Context, now time.Time, busy func() (int, time.Time)) {
	c := &codexApp
	c.Lock()
	since, by, snag, snagPids := c.crashSince, c.crashBy, c.snag, slices.Clone(c.snagPids)
	c.Unlock()
	home := codexHome()
	if snag {
		if open, known := codexAppOpenNow(ctx); known && !open {
			codexAppUnsnag()
			return
		}
		lctx, cancel := context.WithTimeout(ctx, codexAppListWait)
		pids, err := codexAppServersOn(lctx, home)
		cancel()
		// a listing that fails is unknown: the snag stays. With none
		// running at it, one that starts is the app's new start
		if err == nil && (len(snagPids) > 0 || len(pids) > 0) &&
			!slices.ContainsFunc(pids, func(p int) bool { return slices.Contains(snagPids, p) }) {
			codexAppUnsnag()
		}
		if by.IsZero() {
			return
		}
	}
	if by.IsZero() || CodexAppCrashed == nil {
		return
	}
	at, crashed, _ := CodexAppCrashed(since)
	if !crashed {
		// a page not seen in the window, or logs that can't be read, end
		// the watch: an unread log never reopens the app
		if !now.Before(by) {
			c.Lock()
			if c.crashBy.Equal(by) {
				c.crashSince, c.crashBy = time.Time{}, time.Time{}
			}
			c.Unlock()
		}
		return
	}
	c.Lock()
	c.crashSince, c.crashBy = time.Time{}, time.Time{}
	c.Unlock()
	// the page has no composer: a request or a turn since means the app
	// came back (its Try again) and is in use, never cut short
	if busy != nil {
		if n, last := busy(); n > 0 || last.After(at) {
			log.Printf("codex: the Codex app showed its error page after magpie restarted its app-server, and has sent since; left as it is")
			return
		}
	}
	if codexTurnRunning(home, now, at) {
		log.Printf("codex: the Codex app showed its error page after magpie restarted its app-server, and a Codex turn runs since; left as it is")
		return
	}
	lctx, cancel := context.WithTimeout(ctx, codexAppListWait)
	pids, _ := codexAppServersOn(lctx, home)
	cancel()
	err := errNoRelaunch
	if CodexAppRelaunch != nil {
		err = CodexAppRelaunch(ctx)
	}
	if err == nil {
		log.Printf("codex: the Codex app showed its error page after magpie restarted its app-server; quit it and opened it again")
		return
	}
	log.Printf("codex: the Codex app showed its error page after magpie restarted its app-server; reopening it: %v", err)
	c.Lock()
	c.snag, c.snagPids = true, pids
	c.Unlock()
}

func codexAppUnsnag() {
	c := &codexApp
	c.Lock()
	c.snag, c.snagPids = false, nil
	c.Unlock()
}

var errNoRelaunch = errors.New("magpie doesn't reopen the Codex app here")

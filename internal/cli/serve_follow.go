package cli

import (
	"context"
	"log"
	"os"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/update"
)

// `magpie serve` follows an update put in where it runs from. An update
// is put in by another magpie: `magpie web` or the app updating itself
// from the same program, or `magpie update` in a terminal. None of them
// restarted the gateway `magpie serve` keeps, as the service `magpie
// daemon install` writes, a systemd user unit or a launch agent (acezan on
// Discord: `magpie web` as a system unit updated itself, and the user
// unit's serve ran on for good from "magpie.real (deleted)", the page
// saying an older magpie served the gateway; #4335 on a Mac, where the
// old bundle is deleted and TCC turns the process away from Desktop,
// Documents and Downloads). The updater can't reliably restart it: a
// system unit doesn't reach the user's systemctl, and the app may not be
// running at all. So serve looks itself, as the app and magpie web do
// (gui's followReplaced): once the program it started from is another
// file, it waits until the gateway is idle, then runs the new program in
// its own place, the same process under the same service manager or in
// the same terminal. A build of one's own (not a release) never does,
// since a rebuild in place would restart it.

// serveFollowTick is how often serve looks at its program on disk;
// serveIdleSettle is how long nothing has ended before it counts as idle
// (the gap of a tool chain, as gui's idleSettle); serveWaitMost is how
// long it waits for that before it restarts once nothing is in flight,
// however recent. Vars for the tests.
var (
	serveFollowTick = 5 * time.Second
	serveIdleSettle = 10 * time.Second
	serveWaitMost   = time.Hour
)

// serveReexec runs the new program in serve's place; a var for the tests.
var serveReexec = update.Reexec

// serveFollow watches exe, the program serve started from (started is its
// os.Stat then), and calls stop once it has been replaced and busy says
// the gateway is idle. Restart reports the program to run in this one's
// place once stop has been called for it, "" otherwise.
type serveFollow struct {
	exe      string
	started  os.FileInfo
	busy     func() gateway.Busy
	released bool
	stop     func()

	mu      sync.Mutex
	due     time.Time // when the replaced program was first seen
	stopped bool
}

// newServeFollow follows the program this process runs, for s; nil when
// it can't be told (no path, or no file there).
func newServeFollow(s *gateway.Server, stop func()) *serveFollow {
	exe, err := update.Executable()
	if err != nil {
		return nil
	}
	st, err := os.Stat(exe)
	if err != nil {
		return nil
	}
	return &serveFollow{exe: exe, started: st, busy: s.Busy, released: update.Released(version), stop: stop}
}

// run looks every serveFollowTick until ctx ends or serve is stopped.
func (f *serveFollow) run(ctx context.Context) {
	if f == nil || !f.released {
		return
	}
	t := time.NewTicker(serveFollowTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if f.check(time.Now()) {
			return
		}
	}
}

// check stops serve when the program was replaced and the gateway is idle
// at now; it reports whether it did.
func (f *serveFollow) check(now time.Time) bool {
	if !update.Replaced(f.exe, f.started) {
		return false
	}
	f.mu.Lock()
	if f.due.IsZero() {
		f.due = now
		log.Printf("magpie serve: %s was updated; restarting into it once the gateway is idle", f.exe)
	}
	waited := now.Sub(f.due)
	f.mu.Unlock()
	b := f.busy()
	if b.Any() || now.Sub(b.Last) < serveIdleSettle && waited < serveWaitMost {
		return false
	}
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
	f.stop()
	return true
}

// restartTo is the program to run in serve's place, "" when serve wasn't
// stopped for an update.
func (f *serveFollow) restartTo() string {
	if f == nil {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.stopped {
		return ""
	}
	return f.exe
}

// answer is what `magpie update`'s ask (gateway.RestartPath) is told: serve
// restarts into what it put in, at once when the gateway is idle (after
// the answer is out), else once it is, as on its own next look; "current"
// when nothing was put in where serve runs from, "unsupported" for a build
// of one's own, restarted the way it was started.
func (f *serveFollow) answer(string) gateway.RestartReply {
	switch {
	case f == nil || !f.released:
		return gateway.RestartReply{State: "unsupported"}
	case !update.Replaced(f.exe, f.started):
		return gateway.RestartReply{State: "current"}
	}
	b := f.busy()
	if !b.Any() && time.Since(b.Last) >= serveIdleSettle {
		go f.check(time.Now())
		return gateway.RestartReply{State: "restarting"}
	}
	return gateway.RestartReply{State: "waiting", Busy: &b}
}

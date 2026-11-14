package gui

import (
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

// A restart to update ends the gateway this process serves, and with it
// whatever the agents have in flight through it (#577: an agent's reply
// streaming, a Claude Code turn waiting on its caller's tool results). On
// a Mac or Linux it hands the gateway over instead (handover.go), and only
// the Claude subscription's turns would be cut short (restartBusy). So a
// restart asked for while those are busy waits until they are idle:
// nothing in flight, no turn waiting on tools, and nothing ended in the
// last idleSettle — the gap of a tool chain, an agent running a tool
// between one request and the next. It waits waitMost at most; then the
// update stays downloaded, put in on quitting or at the next restart, and
// a newer version out meanwhile is the one restarted into. "Restart now"
// restarts at once, whatever is in flight.
var (
	idleSettle = 10 * time.Second
	waitMost   = time.Hour
	waitTick   = time.Second
	// restartDrain is how long a restart `magpie update` asked for waits
	// for what is in flight; then it restarts anyway. Without a bound a
	// long stream kept the old version running for good (ljlwlcz on X, on
	// Windows, where nothing is handed over).
	restartDrain = 10 * time.Minute
)

// A restart by itself (Settings' AutoRestart) also waits while the user
// is using one of magpie's own windows (BESTLJP0412 on X: the window
// closed and came back mid-way through adding a provider). Each page
// tells the app of the user's input (`POST /api/update/using`, a press, a
// key, typing or the wheel, at most every 20s), and whether a dialog is
// open (#modal); the app counts it in use for usingFor after that input,
// or openFor with a dialog open, which may hold a form half filled in.
// A restart the user asked for (a click, `magpie update`) doesn't wait
// for it: they are the one using magpie.
var (
	usingFor = 2 * time.Minute
	openFor  = 15 * time.Minute
)

// usedAt is when a page last told of input (Unix nanoseconds), usedOpen
// whether a dialog was open then.
var (
	usedAt   atomic.Int64
	usedOpen atomic.Bool
)

// noteUse is a page's word of the user's input at now.
func noteUse(open bool, now time.Time) {
	usedOpen.Store(open)
	usedAt.Store(now.UnixNano())
}

// inUseAt is whether the user was using a window lately enough at now.
func inUseAt(now time.Time) bool {
	at := usedAt.Load()
	if at == 0 {
		return false
	}
	gap, most := now.Sub(time.Unix(0, at)), usingFor
	if usedOpen.Load() {
		most = openFor
	}
	return gap < most
}

// gatewayBusy is what the gateway this process serves has in flight;
// nothing when another magpie serves it, which a restart here leaves be.
var gatewayBusy = func() gateway.Busy {
	if gw := served.Load(); gw != nil {
		return gw.Busy()
	}
	return gateway.Busy{}
}

// waitRecheck is updater.recheck before the restart, asked when a click
// armed the wait: the feed is asked only with nothing downloaded waiting.
var waitRecheck = (*updater).recheck

// idleAt is whether b leaves nothing for a restart at now to cut short.
func idleAt(b gateway.Busy, now time.Time) bool {
	return !b.Any() && now.Sub(b.Last) >= idleSettle
}

// idle is whether the gateway is idle now.
func (u *updater) idle() bool { return idleAt(restartBusy(), time.Now()) }

// waitIdle has restart run once the gateway is idle; a wait under way
// takes the newer restart (the window as it is now). asked is a click's
// (Restart to update), not Settings' AutoRestart's.
func (u *updater) waitIdle(restart func() bool, asked bool) {
	u.mu.Lock()
	u.whenIdle, u.gaveUp, u.waitAsked = restart, false, asked
	if !u.waiting {
		u.drainMost = 0
	}
	if u.waiting {
		u.mu.Unlock()
		return
	}
	u.waiting, u.waitFrom = true, time.Now()
	u.waitGen++
	gen := u.waitGen
	u.mu.Unlock()
	u.told()
	u.loops.Add(1)
	go func() {
		defer u.loops.Done()
		u.waitFor(gen)
	}()
}

// cancelWait stops a restart waiting.
func (u *updater) cancelWait() {
	u.mu.Lock()
	was := u.waiting
	u.waiting, u.whenIdle = false, nil
	u.waitGen++
	u.mu.Unlock()
	if was {
		u.told()
	}
}

// waitingFor is whether a restart waits, and on what.
func (u *updater) waitingFor() (bool, gateway.Busy) {
	u.mu.Lock()
	w := u.waiting
	u.mu.Unlock()
	if !w {
		return false, gateway.Busy{}
	}
	return true, restartBusy()
}

func (u *updater) told() {
	u.mu.Lock()
	f := u.onWait
	u.mu.Unlock()
	if f != nil {
		go f() // off the caller's thread: the tray's relabel runs on the main one
	}
}

// waitFor restarts once the gateway is idle and the version downloaded is
// the latest, unless the wait is called off (gen no longer its) or runs out.
func (u *updater) waitFor(gen int) {
	var last gateway.Busy
	for {
		time.Sleep(waitTick)
		u.mu.Lock()
		if !u.waiting || u.waitGen != gen {
			u.mu.Unlock()
			return
		}
		if time.Since(u.waitFrom) >= waitMost {
			u.waiting, u.gaveUp, u.whenIdle = false, true, nil
			u.mu.Unlock()
			u.told()
			return
		}
		drained := u.drainMost > 0 && time.Since(u.waitFrom) >= u.drainMost
		if drained {
			// the bound `magpie update` was told: what is still in
			// flight is cut now, as a restart does (the gateway's own
			// shutdown lets a stream end its turn first)
			restart := u.whenIdle
			u.waiting, u.whenIdle = false, nil
			u.mu.Unlock()
			u.told()
			if restart != nil {
				restart()
			}
			return
		}
		u.mu.Unlock()
		b := restartBusy()
		if b.Requests != last.Requests || b.Tools != last.Tools {
			last = b
			u.told()
		}
		if !idleAt(b, time.Now()) {
			continue
		}
		u.mu.Lock()
		asked := u.waitAsked
		u.mu.Unlock()
		if !asked && inUseAt(time.Now()) {
			continue
		}
		u.mu.Lock()
		// another magpie put the update in (restartBeside): that is what
		// the restart goes into, whatever the feed answers now
		in := u.replaced()
		u.mu.Unlock()
		switch st := u.json().State; {
		case in:
		case st == "ready":
			// the version downloaded and waiting is gone into as it is;
			// with none (another magpie put it in), the feed is asked
			// (recheck)
			waitRecheck(u, asked)
			if u.asking() || u.json().State != "ready" {
				continue
			}
		case st == "checking", st == "downloading":
			continue
		default:
			// nothing left to restart into (a newer one failed to download)
			u.cancelWait()
			return
		}
		u.mu.Lock()
		if !u.waiting || u.waitGen != gen {
			u.mu.Unlock()
			return
		}
		restart := u.whenIdle
		u.waiting, u.whenIdle = false, nil
		u.mu.Unlock()
		u.told()
		if restart != nil {
			restart()
		}
		return
	}
}

// autoTick, with Settings' AutoRestart on (and automatic updates), has a
// downloaded update restarted into once the gateway is idle (#2096). The
// wait is waitIdle's, so a restart never cuts a request short, nor comes
// while the user is using a window (inUseAt); one that
// ran out (gaveUp) is waited for again on the next tick, and one the user
// called off isn't, for that version.
func (u *updater) autoTick(s settings.Settings) {
	if s.NoAutoUpdate || !s.AutoRestart {
		return
	}
	u.mu.Lock()
	restart, waiting, skip := u.autoRestart, u.waiting, u.autoSkip
	u.mu.Unlock()
	if restart == nil || waiting {
		return
	}
	if j := u.json(); j.State != "ready" || j.Latest == "" || j.Latest == skip {
		return
	}
	u.waitIdle(restart, false)
}

// restartAsked is the tray's Restart to update, restart doing it. With the
// gateway busy it waits for it to be idle (#577), and a click while it
// waits restarts at once. It goes through recheck as the page's restart
// does: the version downloaded and waiting is restarted into as it is,
// and only with none is the feed asked first (coeo91 on Discord).
func (u *updater) restartAsked(restart func() bool) {
	if waiting, _ := u.waitingFor(); !waiting && !u.idle() {
		u.waitIdle(restart, true)
		return
	}
	u.cancelWait()
	u.recheck(true)
	if u.asking() || u.json().State != "ready" {
		u.waitIdle(restart, true)
		return
	}
	restart()
}

// callOff is the user's Cancel of a restart waiting: the wait stops, and
// AutoRestart leaves the version it was for alone.
func (u *updater) callOff() {
	u.mu.Lock()
	if u.waiting && u.latest != nil {
		u.autoSkip = u.latest.Version
	}
	u.mu.Unlock()
	u.cancelWait()
}

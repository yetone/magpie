package gui

import (
	"context"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/update"
)

// restartBeside answers the gateway's RestartPath: `magpie update`, run in
// a terminal (gaubeebangeel on X: an agent this gateway serves, at 4am),
// put version in where this magpie runs from. Before, the app ran on as
// the old version until someone quit and reopened it, which cut what the
// agents had in flight. It now restarts into it as Settings' AutoRestart
// does: at once when the restart cuts nothing short (on a Mac or Linux it
// hands the gateway over, so the agent's own requests go on), else once
// that is done (waitIdle). A magpie that wasn't the one replaced (another
// copy) stays as it is.
func (u *updater) restartBeside(version string) gateway.RestartReply {
	return u.restartBesideWith(version, false, false)
}

// restartBesideAsked is restartBeside for the ask of `magpie update`: a
// wait is bounded by restartDrain, where the app's own look at the disk
// (followReplaced) waits as long as Settings' AutoRestart does.
func (u *updater) restartBesideAsked(version string) gateway.RestartReply {
	return u.restartBesideWith(version, false, true)
}

// restartBesideNow is restartBeside for `magpie update --force`: it
// restarts at once, whatever is in flight and whether or not a window is
// in use.
func (u *updater) restartBesideNow(version string) gateway.RestartReply {
	return u.restartBesideWith(version, true, false)
}

func (u *updater) restartBesideWith(version string, force, bound bool) gateway.RestartReply {
	u.mu.Lock()
	restart := u.autoRestart
	in := u.replaced()
	if in {
		u.followed = true
		// what the terminal put in is the version restarted into, as if
		// this one had downloaded it: the feed isn't asked first
		if version != "" && (u.latest == nil || update.Newer(version, u.latest.Version)) {
			u.latest, u.notesIn = &update.Release{Version: version}, ""
		}
		if u.state != "checking" && u.state != "downloading" {
			u.state = "ready"
		}
	}
	u.mu.Unlock()
	switch {
	case !in:
		return gateway.RestartReply{State: "current"}
	case restart == nil:
		return gateway.RestartReply{State: "unsupported"}
	}
	reply := gateway.RestartReply{Handover: handsOver()}
	// nor while the user is using a window (inUseAt): the terminal's
	// command isn't theirs in the window
	if force {
		u.cancelWait()
		reply.State, reply.Forced = "restarting", true
		go restart()
		return reply
	}
	if waiting, _ := u.waitingFor(); !waiting && u.idle() && !inUseAt(time.Now()) {
		reply.State = "restarting"
		go restart() // after the answer: the restart lets this request finish
		return reply
	}
	u.waitIdle(restart, false)
	if bound {
		u.mu.Lock()
		u.drainMost = restartDrain
		u.mu.Unlock()
		reply.Drain = int(restartDrain / time.Second)
	}
	b := restartBusy()
	reply.State, reply.Busy = "waiting", &b
	return reply
}

// replacedTick is how often followReplaced looks at what is on disk; tests
// shorten it.
var replacedTick = 5 * time.Second

// followReplaced restarts this magpie into an update another magpie put in
// where it runs from, as restartBeside does for magpie update's ask
// (visamzh on X). The app and magpie web run from the same program (the
// terminal's magpie is the app's, linked), and each had its own updater:
// one restarting to update put the new version in, and the other ran on
// as the old one, its Update still offering the version already in, its
// page never reloaded. Neither tells the other, and the other may serve
// no gateway to be asked on, so each looks for itself, a stat every
// replacedTick, until ctx ends (the copy an update left draining looks no
// more, #4938). Only once for a change, so a wait the user called off
// isn't started again; never for what this one put in itself (installed),
// nor in a build of its own, which a rebuild in place would restart.
func (u *updater) followReplaced(ctx context.Context) {
	t := time.NewTicker(replacedTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !update.Released(Version) {
			continue
		}
		u.mu.Lock()
		due := u.autoRestart != nil && !u.installed && !u.followed && u.replaced()
		u.mu.Unlock()
		if due {
			u.restartBeside("")
		}
	}
}

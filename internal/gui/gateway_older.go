package gui

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/daemon"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/update"
)

// A magpie of another version keeping the gateway's port sends every
// agent's request its own way, while this one writes the agents' settings
// (matrix0487 on Discord: the app wrote pi a model its older gateway
// didn't know, and pi said 404 "add a provider in magpie first"). Before,
// this magpie only logged it and waited for that one to quit, and only
// the Gateway page said so. Now:
//
//   - an older copy of the app (it says it serves as "app" or "tray", a
//     released version both) is this app's own predecessor: this one
//     takes the gateway over from it once it has no request in flight
//     (gateway.ServedBusy), quitting it as the Gateway page's button does
//     (quitMagpieOnPort: only a magpie of this user's, by its program),
//     so no request is cut off; looked at again every gatewayWatch;
//   - any other (magpie serve, a service, magpie web, a TUI, a newer
//     magpie, a build of its own) is someone's to quit or update: it is
//     left running and the Agents page says so, with what to do
//     (gatewayOther, in /api/state).

// gatewaySkew is a magpie of another version serving the gateway, as the
// page is told it.
type gatewaySkew struct {
	Version string `json:"version"` // the other's
	Mine    string `json:"mine"`    // this magpie's
	Serves  string `json:"serves,omitempty"`
	Pid     int    `json:"pid,omitempty"`
	// Older: it is older than this one; Newer: newer; neither, a build
	// the two can't be ordered by (one of its own)
	Older bool `json:"older,omitempty"`
	Newer bool `json:"newer,omitempty"`
	// Replacing: this app takes the gateway over from it once it is idle;
	// Busy is what it has in flight meanwhile, -1 when it doesn't say
	Replacing bool `json:"replacing,omitempty"`
	Busy      int  `json:"busy,omitempty"`
	// Stuck: why quitting it didn't go (gatewayFixJSON's Reason); this
	// one stops trying, and the page offers the button instead
	Stuck string `json:"stuck,omitempty"`
}

// gatewayOther is the magpie of another version serving the gateway, nil
// while none does (this one serves it, or one of the same version).
var gatewayOther atomic.Pointer[gatewaySkew]

// replacedOlder counts the older apps this one has quit to serve the
// gateway; past replaceMost something starts them again (a login item
// pointing at an old copy), and they are left to the user.
var replacedOlder atomic.Int32

const replaceMost = 3

// bare is a version without the v a tag has.
func bare(v string) string { return strings.TrimPrefix(v, "v") }

// skewOf is how the magpie o serving the gateway differs from this one,
// nil when it doesn't (the same version, or one that doesn't say).
func skewOf(o gateway.Served) *gatewaySkew {
	mine := gateway.Version
	if o.Version == "" || bare(o.Version) == bare(mine) || !update.Released(mine) {
		// a build of this magpie's own (make dev) beside the installed
		// one is its developer's to know about
		return nil
	}
	return &gatewaySkew{
		Version: bare(o.Version), Mine: bare(mine), Serves: o.Serves, Pid: o.Pid,
		Older: update.Newer(mine, o.Version), Newer: update.Newer(o.Version, mine),
	}
}

// replaceable is whether this magpie takes the gateway over from o by
// itself: this is the app, and o an older release of it.
func replaceable(o gateway.Served, sk *gatewaySkew) bool {
	return handoverHost.Load() && sk != nil && sk.Older && update.Released(o.Version) &&
		(o.Serves == "app" || o.Serves == "tray") && o.Pid > 0 &&
		replacedOlder.Load() < replaceMost
}

// The background service (`magpie daemon`) of this install, left on the
// version before an app update (Norethcy on X): the link ~/.local/bin/magpie
// follows the app's binary, but the service runs the old one until it
// restarts itself, which it does only once the gateway is idle, and the
// user ran `magpie daemon restart` by hand. The app now asks it, as
// `magpie update` does (gateway.RestartPath): it restarts into the new
// version once idle, in the same place under the system's service manager,
// and the page says nothing is wrong while it comes up (PortHeld.Own).

// ownDaemon says the magpie o is this install's background service: the
// process the service manager runs, and its program is this app's own
// (the link, or the file itself). A var for the tests.
var ownDaemon = func(o gateway.Served) bool {
	if o.Serves != "serve" || o.Pid <= 0 {
		return false
	}
	st, err := daemon.Get()
	if err != nil || !st.Installed || !st.Running || st.PID != o.Pid {
		return false
	}
	prog, err := os.Executable()
	if err != nil {
		return false
	}
	return samePath(daemon.Program(), prog)
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if r, err := filepath.EvalSymlinks(a); err == nil {
		a = r
	}
	if r, err := filepath.EvalSymlinks(b); err == nil {
		b = r
	}
	x, err := os.Stat(a)
	if err != nil {
		return false
	}
	y, err := os.Stat(b)
	return err == nil && os.SameFile(x, y)
}

// nudged is the service processes asked to restart: the app asks once per
// process, not at every look.
var (
	nudgedMu sync.Mutex
	nudged   = map[int]bool{}
)

// askRestart is the ask itself; a var for the tests.
var askRestart = func(version string) (gateway.RestartReply, error) {
	c := &http.Client{Timeout: 10 * time.Second}
	res, err := c.Post(gateway.URL()+gateway.RestartPath, "application/json",
		strings.NewReader(`{"version":`+strconv.Quote(version)+`}`))
	if err != nil {
		return gateway.RestartReply{}, err
	}
	defer res.Body.Close()
	var r gateway.RestartReply
	err = json.NewDecoder(io.LimitReader(res.Body, 1<<16)).Decode(&r)
	return r, err
}

// nudgeDaemon asks the background service o, older than this app, to
// restart into the version on disk, once per process. It waits for its
// requests in flight as its own look at the disk does, then runs the new
// program in its place.
func nudgeDaemon(o gateway.Served, sk *gatewaySkew) {
	if sk == nil || !sk.Older || !update.Released(o.Version) || !ownDaemon(o) {
		return
	}
	nudgedMu.Lock()
	if nudged[o.Pid] {
		nudgedMu.Unlock()
		return
	}
	nudged[o.Pid] = true
	nudgedMu.Unlock()
	go func() {
		r, err := askRestart(gateway.Version)
		if err != nil {
			log.Printf("gateway: the background service (magpie %s, pid %d) wasn't asked to restart: %v", sk.Version, o.Pid, err)
			return
		}
		log.Printf("gateway: asked the background service (magpie %s, pid %d) to restart into %s: %s", sk.Version, o.Pid, sk.Mine, r.State)
	}()
}

// otherServes, under gatewayMu with this one serving nothing, looks at the
// magpie o that serves the gateway: what the page is told of it, and, for
// an older copy of the app with no request in flight, quits it. True when
// it was quit and the port is free for this one.
func otherServes(o gateway.Served) bool {
	sk := skewOf(o)
	before := gatewayOther.Load()
	if sk == nil {
		gatewayOther.Store(nil)
		return false
	}
	if before != nil && before.Stuck != "" && before.Pid == sk.Pid {
		sk.Stuck = before.Stuck // tried; the button is the user's now
	} else if replaceable(o, sk) {
		sk.Replacing = true
		n, ok := gateway.ServedBusy()
		if !ok {
			n = -1
		}
		sk.Busy = n
		if n == 0 {
			log.Printf("gateway: magpie %s (pid %d), an older copy of this app, serves %s with nothing in flight; quitting it for this one (%s) to serve", sk.Version, sk.Pid, gateway.URL(), sk.Mine)
			out := quitMagpieOnPort(o.Version)
			if out.OK {
				replacedOlder.Add(1)
				gatewayOther.Store(nil)
				return true
			}
			log.Printf("gateway: magpie %s wasn't quit (%s %s); it keeps the gateway", sk.Version, out.Reason, out.Error)
			sk.Replacing, sk.Busy, sk.Stuck = false, 0, out.Reason
		}
	}
	nudgeDaemon(o, sk)
	if before == nil || before.Version != sk.Version || before.Pid != sk.Pid {
		how := "older than this one"
		switch {
		case sk.Newer:
			how = "newer than this one"
		case !sk.Older:
			how = "a build other than this one"
		}
		next := "until it quits"
		if sk.Replacing {
			next = "until this one takes it over, once it has no request in flight"
		}
		log.Printf("gateway: magpie %s (%s, pid %d) serves %s, %s (%s); agents' requests go through it %s", sk.Version, sk.Serves, sk.Pid, gateway.URL(), how, sk.Mine, next)
	}
	gatewayOther.Store(sk)
	return false
}

package agent

import (
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/wslrun"
)

// An agent pointed at magpie by an address other than this machine's
// loopback — this machine's Codex at Windows as WSL sees it or a forward of
// the user's (#816), an agent in a WSL distro under NAT — is connected by
// its config alone; whether anything answers there is another matter
// (#1013: a portproxy gone after a reboot, the Agents page still said
// connected). Drift asks: each such address is tried from here, in the
// background, and one that doesn't answer while the gateway does on
// loopback is told (Drift kind "unreachable"), with what listens there.

// reachAge is how long an answer is taken as it is: Drift is asked every
// few seconds while the window is up, a probe at most this often.
var reachAge = 20 * time.Second

// reachProbe says whether anything HTTP answers at base: any status will
// do (a gateway shared on the network answers 401 to no key), a refused or
// timed-out connection doesn't. A var for tests.
var reachProbe = realReachProbe

func realReachProbe(base string) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	res, err := c.Get(strings.TrimSuffix(base, "/") + "/v1/models")
	if err != nil {
		return false
	}
	res.Body.Close()
	return true
}

var reach struct {
	sync.Mutex
	m map[string]reachAnswer
}

type reachAnswer struct {
	at       time.Time // when the last probe finished; zero: none yet
	ok, busy bool
}

// reachable is what the last probe of base said, and whether there is one
// (known); a probe older than reachAge, or none, is started in the
// background and never waited for, so Drift stays quick.
func reachable(base string) (ok, known bool) {
	reach.Lock()
	defer reach.Unlock()
	if reach.m == nil {
		reach.m = map[string]reachAnswer{}
	}
	a := reach.m[base]
	if !a.busy && time.Since(a.at) > reachAge {
		a.busy = true
		reach.m[base] = a
		probe := reachProbe
		go func() {
			ok := probe(base)
			reach.Lock()
			reach.m[base] = reachAnswer{at: time.Now(), ok: ok}
			reach.Unlock()
		}()
	}
	return a.ok, !a.at.IsZero()
}

// offLoopback: base is an http address on a host other than this
// machine's loopback and the gateway's own.
func offLoopback(base string) bool {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" {
		return false
	}
	h := u.Hostname()
	if h == "localhost" || sameHost(base, gateway.URL()) {
		return false
	}
	ip := net.ParseIP(h)
	return ip == nil || !ip.IsLoopback()
}

// pointedAt is the gateway's address in the agent's config: where it is
// kept apart from gateway.URL (this machine's Codex), a WSL distro's way to
// it, else "".
func (a *Agent) pointedAt() string {
	switch {
	case a.reach != nil:
		return a.reach()
	case a.Gateway != nil:
		return a.Gateway()
	}
	return ""
}

// unreachable is the drift of an agent pointed at an address off loopback
// that doesn't answer from here while the gateway answers on loopback; nil
// when it answers, isn't asked yet, or the gateway itself is down (then
// nothing would, and that is no fault of the address).
func (a *Agent) unreachable(on Field, now string) *Drift {
	addr := a.pointedAt()
	if !offLoopback(addr) {
		return nil
	}
	// both asked at once, so one round of probes settles it
	ok, known := reachable(addr)
	up, upKnown := reachable(gateway.URL())
	if ok || !known || !up || !upKnown {
		return nil
	}
	d := &Drift{Kind: "unreachable", Field: on.Key, Now: now, Want: now, Addr: addr}
	host := strings.TrimPrefix(addr, "http://")
	d.Detail = a.Name + " is pointed at magpie at " + addr + ", where nothing answers from this computer, though magpie answers at " + gateway.URL() + ". "
	if a.WSL != "" || runtime.GOOS == "windows" {
		d.Detail += "Under WSL's NAT networking, Windows as WSL sees it reaches magpie only while something listens on " + host +
			": magpie itself with Settings › Share on local network on (it then listens on every address, and asks other computers for a gateway key), or a forward of your own (netsh interface portproxy, which may need setting up again after Windows restarts). " +
			"Once it answers here and WSL still can't connect, Windows' firewall may be keeping WSL out: allow the port " + gateway.Port() + " for the WSL network. " +
			"On Windows 11, networkingMode=mirrored under [wsl2] in %UserProfile%\\.wslconfig lets WSL reach magpie at 127.0.0.1 instead."
	} else {
		d.Detail += "Whatever forwarded " + host + " to magpie isn't listening now; start it again, or point " + a.Name + " at " + gateway.URL() + "."
	}
	if to, distro := wslMovedFrom(addr); to != "" {
		d.Move = to
		d.Detail += " WSL " + distro + " reaches Windows at " + to + " now, not " + addr + " as before: Use " + to + " points " + a.Name + " there."
	}
	return d
}

// wslMovedFrom is the address a WSL distro reaches Windows at now, when
// base is on one it reached Windows at before (its networking's address
// changed since, or it is in mirrored or consomme networking now, where
// that is 127.0.0.1), and that distro's name; "" otherwise. A host no
// distro was seen at is the user's own, and is never offered to be moved.
func wslMovedFrom(base string) (to, name string) {
	u, err := url.Parse(base)
	if err != nil {
		return "", ""
	}
	h, port := u.Hostname(), u.Port()
	wsl.Lock()
	defer wsl.Unlock()
	for n, d := range wsl.seen {
		if d == nil {
			continue
		}
		loopback := wslrun.HostLoopback(d.Net)
		if !loopback && (d.Gateway == "" || d.Gateway == h) {
			continue
		}
		for _, w := range d.Was {
			if w != h {
				continue
			}
			if loopback {
				return gateway.URL(), n
			}
			if port == "" {
				port = gateway.Port()
			}
			return "http://" + net.JoinHostPort(d.Gateway, port), n
		}
	}
	return "", ""
}

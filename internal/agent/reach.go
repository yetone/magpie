package agent

import (
	"cmp"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/netproxy"
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
	// a certificate magpie made itself isn't one this computer trusts, and
	// whether something answers is all that is asked here
	c := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	res, err := c.Get(strings.TrimSuffix(base, "/") + "/v1/models")
	if err != nil {
		return false
	}
	res.Body.Close()
	return true
}

// magpieProbe says whether base answers as a magpie gateway: GET base/ is
// 200 with {"name":"magpie"} (the gateway's info), as it is through a
// reverse proxy that forwards base to it. A var for tests.
var magpieProbe = realMagpieProbe

func realMagpieProbe(base string) bool {
	c := &http.Client{Timeout: 3 * time.Second}
	res, err := c.Get(strings.TrimSuffix(base, "/") + "/")
	if err != nil {
		return false
	}
	return answersAsMagpie(res)
}

// answersAsMagpie: res, closed here, is the gateway's info (GET /).
func answersAsMagpie(res *http.Response) bool {
	defer res.Body.Close()
	var info struct {
		Name string `json:"name"`
	}
	return res.StatusCode == http.StatusOK && json.NewDecoder(res.Body).Decode(&info) == nil && info.Name == "magpie"
}

// magpieAt is whether base answers as magpie (magpieProbe), and whether
// that is known yet: the last probe's answer, a new one started in the
// background as reachable does, never waited for. A var for tests, which
// are told no without a probe.
var magpieAt = func(base string) (ok, known bool) {
	return probed("magpie "+base, func() bool { return magpieProbe(base) })
}

// proxyBase is v, the address an agent's config names where magpie wrote
// want, with want's path (/v1 where want has it) cut off its end, when
// that can be another way to magpie: a URL on another machine or a name,
// such as a company's reverse proxy in front of the gateway (davisjiahao,
// #1985: https://ai.corp.example/magpie forwarding to 127.0.0.1:3425), or
// one on this machine's loopback with a path of its own before magpie's (a
// compliance proxy's http://127.0.0.1:8788/claude-code-anthropic/<route>).
// One on loopback with none is magpie's own on an older port, never
// another way to it.
func proxyBase(v, want string) (string, bool) {
	w, err := url.Parse(want)
	if err != nil || (w.Scheme != "http" && w.Scheme != "https") || w.Host == "" {
		return "", false
	}
	base, ok := strings.CutSuffix(strings.TrimSuffix(strings.TrimSpace(v), "/"), strings.TrimSuffix(w.Path, "/"))
	if !ok || !onAnotherMachine(base) && !loopbackWithPath(base) {
		return "", false
	}
	if u, err := url.Parse(base); err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	return base, true
}

// loopbackWithPath says base is an http(s) address on this machine's
// loopback with a path: a local proxy's route, never the gateway's own
// address, which has none.
func loopbackWithPath(base string) bool {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || onAnotherMachine(base) {
		return false
	}
	return strings.Trim(u.Path, "/") != ""
}

// reachesMagpie says v reaches magpie another way (proxyBase) whose root
// answers as magpie. Until it has been tried it is taken as reaching
// magpie, as an address not yet tried isn't unreachable either: telling
// drift first would offer Reconnect, which writes the proxy away.
func reachesMagpie(v, want string) bool {
	if chainsToGateway(v, want) {
		return true
	}
	base, ok := proxyBase(v, want)
	if !ok {
		return false
	}
	ok, known := magpieAt(base)
	return ok || !known
}

// surelyMagpie is reachesMagpie once magpie has answered there: what a
// write that keeps the address waits for (magpieAsked), so a relay not yet
// tried never gets magpie's model or key.
func surelyMagpie(v, want string) bool {
	if chainsToGateway(v, want) {
		return true
	}
	base, ok := proxyBase(v, want)
	if !ok {
		return false
	}
	ok, known := magpieAt(base)
	return ok && known
}

// chainsToGateway says v is a proxy on this machine's loopback that names
// want, magpie's gateway, as its upstream at the end of its path:
// billion-context's http://127.0.0.1:18787/bili/http://127.0.0.1:3425
// (its Claude Code lane, which the Library's billion-context tab installs;
// also /bili/anthropic/<upstream>, and bili start's 8787). Its requests
// reach magpie whether or not the proxy answers GET / as magpie: a bili
// lane runs only beside its agent, and magpieProbe asked it while it was
// down, so the Agents page told drift and Reconnect wrote the gateway over
// the user's billion-context (33sczf on X). No probe is needed, as the
// upstream is the gateway itself.
func chainsToGateway(v, want string) bool {
	v = strings.TrimSuffix(strings.TrimSpace(v), "/")
	if !loopbackWithPath(v) {
		return false
	}
	u, err := url.Parse(v)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	rest := u.Path
	i := strings.Index(rest, "/http://")
	if j := strings.Index(rest, "/https://"); j >= 0 && (i < 0 || j < i) {
		i = j
	}
	if i < 0 {
		return false
	}
	up, err := url.Parse(rest[i+1:])
	if err != nil {
		return false
	}
	w, err := url.Parse(strings.TrimSuffix(want, "/"))
	if err != nil || onAnotherMachine(want) || onAnotherMachine(rest[i+1:]) {
		return false
	}
	return up.Scheme == w.Scheme && up.Port() == w.Port() && up.Port() != "" &&
		strings.TrimSuffix(up.Path, "/") == strings.TrimSuffix(w.Path, "/") &&
		up.User == nil && up.RawQuery == ""
}

// magpieAsked is magpieAt waited for, for a write: base, not asked yet, is
// asked now (probedNow), so what the write
// reads after is magpie's answer there. Unasked, surelyMagpie is no, and
// the first write in a magpie just started, the CLI's, took a proxy to
// magpie for the user's own endpoint (#1985). One answered before, at any
// age, is what the write goes by, as the reads do.
func magpieAsked(base string) {
	if _, known := magpieAt(base); !known {
		probedNow("magpie "+base, func() bool { return magpieProbe(base) })
	}
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
	probe := reachProbe
	return probed(base, func() bool { return probe(base) })
}

// probed is reachable for any probe, kept under key: its last answer, and
// whether there is one, with a new one started in the background when
// that is older than reachAge.
func probed(key string, probe func() bool) (ok, known bool) {
	reach.Lock()
	defer reach.Unlock()
	if reach.m == nil {
		reach.m = map[string]reachAnswer{}
	}
	a := reach.m[key]
	if !a.busy && time.Since(a.at) > reachAge {
		a.busy = true
		reach.m[key] = a
		go func() {
			ok := probe()
			reach.Lock()
			reach.m[key] = reachAnswer{at: time.Now(), ok: ok}
			reach.Unlock()
		}()
	}
	return a.ok, !a.at.IsZero()
}

// probedNow is probed waited for: the last answer while it is fresh, else
// one asked now, which the next probed of key gets too.
func probedNow(key string, probe func() bool) bool {
	reach.Lock()
	a := reach.m[key]
	reach.Unlock()
	if !a.at.IsZero() && time.Since(a.at) <= reachAge {
		return a.ok
	}
	ok := probe()
	reach.Lock()
	if reach.m == nil {
		reach.m = map[string]reachAnswer{}
	}
	reach.m[key] = reachAnswer{at: time.Now(), ok: ok}
	reach.Unlock()
	return ok
}

// offLoopback: base is an http(s) address on a host other than this
// machine's loopback and the gateway's own.
func offLoopback(base string) bool {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
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
	inside := a.reachFrom != nil && gateway.OnWSLNet(addr)
	var ok, known bool
	if inside {
		ok, known = a.reachFrom(addr)
	} else {
		ok, known = reachable(addr)
	}
	up, upKnown := reachable(gateway.URL())
	if ok || !known || !up || !upKnown {
		return nil
	}
	d := &Drift{Kind: "unreachable", Field: on.Key, Now: now, Want: now, Addr: addr}
	host := strings.TrimPrefix(strings.TrimPrefix(addr, "http://"), "https://")
	if inside {
		// magpie listens there itself (gateway/wsl.go): what keeps WSL out
		// is Windows' firewall
		d.Detail = a.Name + " is pointed at magpie at " + addr + ", where magpie listens for WSL, but nothing answers there from WSL " + a.WSL + ": Windows' firewall keeps WSL out. " +
			"Allow magpie in when Windows asks, or, in PowerShell as administrator: " + wslFirewallCommand() + ". " +
			"On Windows 11, networkingMode=mirrored under [wsl2] in %UserProfile%\\.wslconfig lets WSL reach magpie at 127.0.0.1 instead."
		if to, distro := wslMovedFrom(addr); to != "" {
			// another of Windows' addresses answers from WSL (wslFindVia)
			d.Move = to
			d.Detail = a.Name + " is pointed at magpie at " + addr + ", where nothing answers from WSL " + distro + ", but magpie answers it at " + to + ": Use " + to + " points " + a.Name + " there."
		}
		return d
	}
	d.Detail = a.Name + " is pointed at magpie at " + addr + ", where nothing answers from this computer, though magpie answers at " + gateway.URL() + ". "
	if a.WSL != "" || runtime.GOOS == "windows" {
		d.Detail += "Under WSL's NAT networking, Windows as WSL sees it reaches magpie only while something listens on " + host +
			": magpie itself with Settings › Network and sharing › Share on local network on (it then listens on every address, and asks other computers for a gateway key), or a forward of your own (netsh interface portproxy, which may need setting up again after Windows restarts). " +
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

// wslFirewallCommand lets magpie in from WSL's networks through Windows'
// firewall, for an administrator's PowerShell. WSL's network is a Public
// one, so a rule for Private networks only (what Windows' own prompt
// offers ticked) doesn't do.
func wslFirewallCommand() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "magpie.exe"
	}
	return "New-NetFirewallRule -DisplayName 'magpie (WSL)' -Direction Inbound -Action Allow -Profile Any -InterfaceAlias 'vEthernet (WSL*)' -Program '" + strings.ReplaceAll(exe, "'", "''") + "'"
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
		host := d.host()
		if !loopback && (host == "" || host == h) {
			continue
		}
		// the WSL network's address, passed over for a via (wslFindVia),
		// is one it reached Windows at before too
		if !slices.Contains(d.Was, h) && (loopback || h != d.Gateway) {
			continue
		}
		if loopback {
			return gateway.URL(), n
		}
		if port == "" {
			port = gateway.Port()
		}
		return "http://" + net.JoinHostPort(host, port), n
	}
	return "", ""
}

// proxied is the drift of an agent whose requests for the gateway go
// through a proxy that doesn't pass them on to it, while the gateway
// answers here; nil when they get there, or it isn't known yet.
func (a *Agent) proxied(on Field, now string) *Drift {
	if a.viaProxy == nil {
		return nil
	}
	// with no proxy in the way, nothing is tried; with one, both are asked
	// at once, so one round of probes settles it
	ok, known, proxy, from := a.viaProxy()
	if proxy == "" {
		return nil
	}
	up, upKnown := reachable(gateway.URL())
	if ok || !known || !up || !upKnown {
		return nil
	}
	base := cmp.Or(a.pointedAt(), gateway.URL())
	return &Drift{Kind: "proxied", Field: on.Key, Now: now, Want: now, Addr: netproxy.Redact(proxy), Detail: codexProxiedDetail(base, proxy, from)}
}

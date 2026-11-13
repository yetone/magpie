package netproxy

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A host on the user's own network — another computer's magpie (a Remote
// magpie), an Ollama on the NAS — is reached directly, whatever proxy the
// settings, the environment or the system name (#982: a Remote magpie at
// a LAN name was asked through the Mac's proxy, which answered 502 Bad
// Gateway, and its model list never came). A proxy is for reaching
// vendors; one elsewhere can't reach the user's network, and one on this
// computer would only go there directly itself. A provider's own proxy
// (With) is still taken as it is: that one was set for it. The plugin
// host applies the same rule to plugins' requests (onLAN in
// internal/plugin/host.js, #1572): a change here is made there too.

// lanLookup resolves a name to its addresses; tests replace it.
var lanLookup = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

// lanWait is how long a name is looked up for before it is taken as not
// on the network: a vendor whose name doesn't resolve here (reached only
// through the proxy) waits no longer than this, once a minute.
const lanWait = 400 * time.Millisecond

// lanKeep is how long what a name resolved to is kept.
const lanKeep = time.Minute

var lanCache = struct {
	sync.Mutex
	m map[string]lanSeen
}{m: map[string]lanSeen{}}

type lanSeen struct {
	lan bool
	at  time.Time
}

// onLAN reports whether host is on the user's own network: a private,
// link-local or carrier-grade NAT (Tailscale's 100.64/10) address, a
// .local name, or a name every address of which is one of those. A name
// that doesn't resolve is not.
func onLAN(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return lanIP(ip)
	}
	if strings.HasSuffix(host, ".local") {
		return true
	}
	lanCache.Lock()
	s, ok := lanCache.m[host]
	lanCache.Unlock()
	if ok && time.Since(s.at) < lanKeep {
		return s.lan
	}
	ctx, cancel := context.WithTimeout(context.Background(), lanWait)
	addrs, err := lanLookup(ctx, host)
	cancel()
	lan := err == nil && len(addrs) > 0
	for _, a := range addrs {
		if !lanIP(a.IP) {
			lan = false
		}
	}
	lanCache.Lock()
	if len(lanCache.m) >= 256 {
		clear(lanCache.m)
	}
	lanCache.m[host] = lanSeen{lan: lan, at: time.Now()}
	lanCache.Unlock()
	return lan
}

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func lanIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLoopback() || cgnat.Contains(ip)
}

// lanDial is how long a connection to an address on the user's own
// network is waited for: one there is taken within milliseconds, while
// one nobody holds any more (a Remote magpie whose computer's router
// handed it another address, le13ure on X) went unanswered until the
// system gave up, over a minute on macOS and two on Linux, each try of a
// request, before its retries, or the look for where it went
// (provider's SeekMoved), could begin.
var lanDial = 5 * time.Second

// lanDialCap has t give up on connecting to a private or link-local
// address after lanDial. A name is left to the system's wait: it may
// stand for an address outside, and Tailscale's (100.64/10), relayed on
// its first packets, may take longer.
func lanDialCap(t *http.Transport) {
	dial := t.DialContext
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if h, _, err := net.SplitHostPort(addr); err == nil {
			if ip := net.ParseIP(h); ip != nil && (ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
				ctx, cancel := context.WithTimeout(ctx, lanDial)
				defer cancel()
				return dial(ctx, network, addr)
			}
		}
		return dial(ctx, network, addr)
	}
}

package gateway

// A connection that fails because this computer has no network says
// nothing of the provider (usrname1833 on Discord: offline, a routing
// group's providers failed one after another, each rested, and the first
// stayed behind the others once the network was back). When a request
// never got a reply — a name that didn't resolve, a connection that
// couldn't be made — and nothing has answered magpie lately, the network
// is asked: no interface up with an address means none, and otherwise
// the other providers' hosts are dialled, and none taking the connection
// means this computer can't reach the network. Then the next one is
// asked, and nobody rests or is counted, as for a proxy that is down
// (failProxy). One provider failing to connect while another's host
// takes a connection, or any reply came in the last moments, is still
// that provider's failure, and rests it as before.

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/provider"
)

// connFailed is the error Go (and Bun, for a plugin's fetch) gives when no
// connection was made: a name that didn't resolve, no route, refused, a
// dial or TLS handshake that timed out. A proxy that is down (proxyDown)
// is told apart before it.
var connFailed = lazyre.New(`dial tcp |dial udp |lookup [^ ]+|TLS handshake timeout|network is unreachable|no route to host|Unable to connect|ConnectionRefused|FailedToOpenSocket|ENOTFOUND|EAI_AGAIN|ECONNREFUSED|ENETUNREACH|EHOSTUNREACH`)

// heardFresh is how long a reply from anywhere says the network is up.
const heardFresh = 15 * time.Second

// offlineKeep is how long a verdict on the network is kept: the failures
// of one request's members, and of the requests beside it, ask once.
const offlineKeep = 5 * time.Second

// offlineDial is how long one dial of another provider's host is waited for.
var offlineDial = 2 * time.Second

// network is what a Server has heard of the network: when an upstream
// last answered, and the verdict kept. It is the Server's own, so a reply
// that reaches another Server in the process (a test's, finishing after
// its test) says nothing of this one's network (#7871).
type network struct {
	heard atomic.Int64 // when any upstream last answered, as unix nanoseconds

	mu   sync.Mutex
	at   time.Time
	off  bool
	busy chan struct{} // closed when the network being asked now is known
}

// heardNet notes a reply from an upstream: whatever it said, the network
// reached it.
func (n *network) heardNet() { n.heard.Store(time.Now().UnixNano()) }

// hasNetwork says whether an interface is up with an address beyond its own
// link; swapped in tests. Unknown is yes, as before.
var hasNetwork = func() bool {
	ifs, err := net.Interfaces()
	if err != nil {
		return true
	}
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagRunning == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := i.Addrs()
		if err != nil {
			return true
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.IsGlobalUnicast() {
				return true
			}
		}
	}
	return false
}

// nearHost says a host answers whether or not the network does: this
// computer, the local network's. It tells nothing, so it isn't dialled;
// swapped in tests, whose servers are all on loopback.
var nearHost = func(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || !strings.Contains(h, ".") && !strings.Contains(h, ":") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())
}

// dialHost dials one host:port; swapped in tests.
var dialHost = func(ctx context.Context, hostport string) error {
	c, err := (&net.Dialer{Timeout: offlineDial}).DialContext(ctx, "tcp", hostport)
	if err == nil {
		c.Close()
	}
	return err
}

// hostsOf is the host:port each of p's base URLs is at.
func hostsOf(p provider.Provider) []string {
	var out []string
	for _, b := range []string{p.Chat, p.Responses, p.Anthropic, p.Gemini} {
		u, err := url.Parse(b)
		if err != nil || u.Hostname() == "" {
			continue
		}
		port := u.Port()
		if port == "" {
			port = "443"
			if u.Scheme == "http" {
				port = "80"
			}
		}
		out = append(out, net.JoinHostPort(u.Hostname(), port))
	}
	return out
}

// otherHosts is up to three hosts of providers other than p, reached
// directly as p was, that tell whether the network is up: not p's own,
// nor one reached through a proxy or at addresses of the user's choosing
// (Resolve: dialled by name here, it may not resolve on a managed
// network that works), nor a near one.
func otherHosts(p provider.Provider) []string {
	own := map[string]bool{}
	for _, h := range hostsOf(p) {
		own[h] = true
	}
	var out []string
	for _, o := range provider.All() {
		if o.ID == p.ID || o.Proxy != "" || len(o.Resolve) > 0 {
			continue
		}
		for _, h := range hostsOf(o) {
			host, _, _ := net.SplitHostPort(h)
			if own[h] || nearHost(host) || slices.Contains(out, h) {
				continue
			}
			out = append(out, h)
			if len(out) == 3 {
				return out
			}
		}
	}
	return out
}

// reachesNone dials hosts at once and says none took the connection.
func reachesNone(hosts []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), offlineDial+time.Second)
	defer cancel()
	ok := make(chan bool, len(hosts))
	for _, h := range hosts {
		go func() { ok <- dialHost(ctx, h) == nil }()
	}
	for range hosts {
		if <-ok {
			return false
		}
	}
	return true
}

// offlineFailure says the failure c met, status and body, is this
// computer having no network: no connection was made, nothing answered
// lately, and the network is down or reaches none of the other providers.
// With no other provider to ask, and an interface up, it is c's.
func (s *Server) offlineFailure(c candidate, status int, body []byte) bool {
	if status != http.StatusBadGateway || proxyDown.Match(body) || !connFailed.Match(body) {
		return false
	}
	n := &s.network
	if time.Since(time.Unix(0, n.heard.Load())) < heardFresh {
		return false
	}
	for {
		n.mu.Lock()
		if time.Since(n.at) < offlineKeep {
			off := n.off
			n.mu.Unlock()
			return off
		}
		if wait := n.busy; wait != nil {
			n.mu.Unlock()
			<-wait
			continue
		}
		done := make(chan struct{})
		n.busy = done
		n.mu.Unlock()
		off := !hasNetwork()
		if !off {
			hosts := otherHosts(c.p)
			off = len(hosts) > 0 && reachesNone(hosts)
		}
		n.mu.Lock()
		n.at, n.off, n.busy = time.Now(), off, nil
		n.mu.Unlock()
		close(done)
		return off
	}
}

package provider

// A Remote magpie with other addresses (Addresses, netproxy.WithFailover)
// is one reached from more than one network: at home at its LAN address,
// from outside through Tailscale or EasyTier (evjohnicu on X). Away from
// home, its home network's address is a private one on whatever network
// this computer is on now, a café's or a hotel's, where any host there,
// one that took that address or answers ARP for it, would take the
// connection and be handed the provider's key in clear, and, answering,
// become the address asked first from then on (the one that last
// answered), given the user's prompts and files. A look for a moved
// remote (remote_moved.go) guards against just that, and so does this:
// an address reached in plain http at a private or link-local IP, or a
// .local name, is sent nothing keyed until it proves it holds the
// provider's gateway key (proves), the proof asked the way the request
// itself would go. A name is resolved here and connected to at the IP
// that proved. An https address is the server its certificate says it
// is, and a Tailscale address (100.64.0.0/10) is the peer WireGuard says
// it is: neither is asked to prove. A verdict is kept for the networks
// this computer is on (vouchPrint) for a while.

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
)

// vouchHost says host is on a network another network can't tell apart
// from its own: a private or link-local IP, a .local name. Swapped in
// tests, whose magpies are on loopback.
var vouchHost = func(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if strings.HasSuffix(h, ".local") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && !ip.IsLoopback() && (ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

// vouchLookup resolves a name to the addresses it is asked to prove at;
// swapped in tests.
var vouchLookup = func(ctx context.Context, host string) ([]string, error) {
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ip := range ips {
		out = append(out, ip.IP.String())
	}
	return out, nil
}

// vouchNets names the networks this computer is on now; swapped in tests.
var vouchNets = func() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	var names []string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() {
			names = append(names, n.String())
		}
	}
	slices.Sort(names)
	return strings.Join(names, ",")
}

// vouchKept is how long an address that proved is taken as proved, on
// the same networks; one that didn't is asked again after vouchMissed.
var vouchKept, vouchMissed = 10 * time.Minute, 30 * time.Second

type vouched struct {
	at    string
	ok    bool
	until time.Time
}

var vouchSeen = struct {
	sync.Mutex
	m map[string]vouched
}{m: map[string]vouched{}}

// vouch is p's netproxy.Vouch: what of its addresses may be sent its key.
func (p Provider) vouch() netproxy.Vouch {
	key := p.Key
	return func(ctx context.Context, root *url.URL, ask func(*http.Request) (*http.Response, error)) (string, bool) {
		if key == "" || !strings.EqualFold(root.Scheme, "http") || !vouchHost(root.Hostname()) {
			return "", true
		}
		port := root.Port()
		if port == "" {
			port = "80"
		}
		seen := root.String() + "\n" + RemoteKeyID(key) + "\n" + vouchNets()
		vouchSeen.Lock()
		v, had := vouchSeen.m[seen]
		vouchSeen.Unlock()
		if had && time.Now().Before(v.until) {
			return v.at, v.ok
		}
		v = vouched{until: time.Now().Add(vouchMissed)}
		hosts := []string{root.Hostname()}
		if net.ParseIP(root.Hostname()) == nil {
			lctx, cancel := context.WithTimeout(ctx, seekAnswer)
			hosts, _ = vouchLookup(lctx, root.Hostname())
			cancel()
			if len(hosts) > 4 {
				hosts = hosts[:4]
			}
		}
		for _, h := range hosts {
			at := net.JoinHostPort(h, port)
			if provesVia(ctx, "http", at, strings.TrimRight(root.Path, "/"), key, ask) {
				v = vouched{at: at, ok: true, until: time.Now().Add(vouchKept)}
				break
			}
		}
		vouchSeen.Lock()
		if len(vouchSeen.m) >= 256 {
			clear(vouchSeen.m)
		}
		vouchSeen.m[seen] = v
		vouchSeen.Unlock()
		return v.at, v.ok
	}
}

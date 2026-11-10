package gateway

import (
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The allow list (lc on Discord, magpie in Docker on a public host): the
// addresses other computers may reach magpie from at all, as IPs and CIDR
// ranges. A request from any other address is refused 403 before its key
// is looked at, so a gateway key that leaks is of no use from elsewhere.
// No list lets every address in, as before. This computer (loopback, and
// the computer's own addresses) is always let in.
//
// The address judged is the connection's own (r.RemoteAddr). A forwarding
// header (X-Forwarded-For and the rest) is never taken for it: anyone can
// write one, and magpie keeps no list of proxies to believe. A proxy or
// tunnel on this computer reaches magpie from loopback, so what it passes
// on is let in here, and still needs a gateway key (proxied); limit who
// reaches such a proxy at the proxy.

// AllowIPsEnv names the environment variable that sets the list, in place
// of Settings' (for Docker and `magpie web`).
const AllowIPsEnv = "MAGPIE_ALLOW_IPS"

// AllowIPs is the allow list in force, as written, and whether
// MAGPIE_ALLOW_IPS set it. Empty: no list, every address let in.
func AllowIPs() (list []string, fromEnv bool) {
	list, fromEnv, _ = allowList()
	return list, fromEnv
}

// lastAllow is the Settings list as last read whole. settings.json that
// can't be read, or doesn't parse with no last good generation, holds a
// list nobody knows: it is never taken for "no list" (every address let
// in). The one read last stays in force, and with none read since magpie
// started, only this computer is let in.
var lastAllow struct {
	sync.Mutex
	list   []string
	read   bool // list was read from a good settings.json
	warned bool // the unknown read is logged, once until one reads again
}

// allowList is AllowIPs, and unknown when settings.json couldn't be read
// and no list was read before it: then only this computer is let in.
func allowList() (list []string, fromEnv, unknown bool) {
	if v := strings.TrimSpace(os.Getenv(AllowIPsEnv)); v != "" {
		return settings.SplitAllowIPs(v), true, false
	}
	s, err := settings.SharedChecked() // only AllowIPs read, kept as is
	lastAllow.Lock()
	defer lastAllow.Unlock()
	if err == nil {
		lastAllow.list, lastAllow.read, lastAllow.warned = s.AllowIPs, true, false
		return s.AllowIPs, false, false
	}
	if !lastAllow.warned {
		lastAllow.warned = true
		if lastAllow.read {
			log.Printf("allow list: %v; the list read last stays in force", err)
		} else {
			log.Printf("allow list: %v; only this computer is let in until it reads", err)
		}
	}
	return lastAllow.list, false, !lastAllow.read
}

var warnedAllow sync.Map

// allowPrefixes is the list in force, read; on says one is set, or that
// settings.json holds one nobody knows (unknown), which lets in only this
// computer. An entry that doesn't read (a mistyped MAGPIE_ALLOW_IPS) lets
// nothing in and is logged once: the list stays on, so a typo never opens
// magpie to every address.
func allowPrefixes() (ps []netip.Prefix, on, unknown bool) {
	list, _, unknown := allowList()
	if unknown {
		return nil, true, true
	}
	for _, e := range list {
		p, err := settings.ParseAllowIP(e)
		if err != nil {
			if _, done := warnedAllow.LoadOrStore(e, true); !done {
				log.Printf("%s: %v; it lets no address in", AllowIPsEnv, err)
			}
			continue
		}
		ps = append(ps, p)
	}
	return ps, len(list) > 0, false
}

// peerAddr is the address r's connection came from; false when
// RemoteAddr holds none.
func peerAddr(r *http.Request) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().WithZone("").Unmap(), true
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		h = r.RemoteAddr
	}
	a, err := netip.ParseAddr(strings.Trim(h, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return a.WithZone("").Unmap(), true
}

// ownAddr: a is one of this computer's own addresses, which a connection
// comes from only when it was made here (an agent calling the address
// MAGPIE_ADDR names, which isn't loopback).
var ownAddr = func(a netip.Addr) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, ia := range addrs {
		if n, ok := ia.(*net.IPNet); ok {
			if b, ok := netip.AddrFromSlice(n.IP); ok && b.Unmap() == a {
				return true
			}
		}
	}
	return false
}

// AllowedFrom: a request from a may reach magpie under the allow list —
// there is none, a is this computer's, or a is in it.
func AllowedFrom(a netip.Addr) bool {
	ps, on, _ := allowPrefixes()
	return allowedIn(ps, on, a)
}

// allowedIn is AllowedFrom for the list ps (on: there is one).
func allowedIn(ps []netip.Prefix, on bool, a netip.Addr) bool {
	if !on || a.IsLoopback() {
		return true
	}
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return ownAddr(a)
}

// allowedListFrom is AllowedFrom for a list not yet in force (Settings
// checking a new one won't shut out the page it was saved from).
func allowedListFrom(list []string, a netip.Addr) bool {
	if len(list) == 0 || a.IsLoopback() {
		return true
	}
	for _, e := range list {
		if p, err := settings.ParseAllowIP(e); err == nil && p.Contains(a) {
			return true
		}
	}
	return ownAddr(a)
}

// KeepsOut: the allow list list would refuse r, a request that came in
// from elsewhere; it names r's address.
func KeepsOut(list []string, r *http.Request) (string, bool) {
	a, ok := peerAddr(r)
	if !ok {
		return "", false
	}
	return a.String(), !allowedListFrom(list, a)
}

// allowed: r may reach magpie under the allow list; when it may not, it
// names r's address and says why.
func allowed(r *http.Request) (addr, why string, ok bool) {
	ps, on, unknown := allowPrefixes()
	if !on {
		return "", "", true
	}
	a, ok := peerAddr(r)
	if !ok {
		return r.RemoteAddr, notAllowed(r.RemoteAddr, unknown), false
	}
	if allowedIn(ps, on, a) {
		return a.String(), "", true
	}
	return a.String(), notAllowed(a.String(), unknown), false
}

// notAllowed says why a request from addr was refused: it isn't on the
// list, or (unknown) settings.json, which holds the list, can't be read.
func notAllowed(addr string, unknown bool) string {
	if unknown {
		return "magpie couldn't read its settings.json in " + settings.Dir() + ", which holds its allow list, so it answers only this computer until it can, and this request came from " + addr + ": repair that file, or set the list in " + AllowIPsEnv
	}
	return "magpie answers only the addresses on its allow list (" + AllowIPsEnv + ", or Settings → Network and sharing → Allowed addresses), and this request came from " + addr + ": add it there to let it in"
}

// AllowGuard refuses a request from an address the allow list leaves out,
// 403, before anything else looks at it: the page `magpie web` serves.
func AllowGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if addr, why, ok := allowed(r); !ok {
			log.Printf("refused %s %s from %s: not on the allow list", r.Method, r.URL.Path, addr)
			http.Error(w, why, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// refuseNotAllowed answers a gateway request from an address the allow
// list leaves out, in the error shape agents show; true when it did.
func refuseNotAllowed(w http.ResponseWriter, r *http.Request) bool {
	addr, why, ok := allowed(r)
	if ok {
		return false
	}
	log.Printf("refused %s %s from %s: not on the allow list", r.Method, r.URL.Path, addr)
	writeError(w, provider.Chat, http.StatusForbidden, why)
	return true
}

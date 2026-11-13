package netproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
)

type resolveKey struct{}

// WithResolve is ctx for requests made on behalf of a provider the user
// added whose host names are connected to addresses of their choosing, as
// curl's --resolve does (@quantum_lsfc on X: a managed network sends some
// domains to an internal L4 SNI gateway, an IP in the base URL fails, and
// /etc/hosts can't be edited). rules maps a host name to the places its
// TCP connection is made, in order: an IP address (with a port of its own
// if it names one), or "dns:<name>", the addresses that name resolves to
// when the connection is made, so a rotation of its records is followed.
// The URL, so the TLS server name and the Host header, stays as it was;
// only where the connection goes changes. A place that doesn't connect
// is followed by the next, and when none does the host is dialled as if
// there were no rule. A request that goes through a proxy is sent to the
// proxy as before: the proxy connects to the name itself, so no rule
// applies to it. With (a request made for another provider) drops it.
// CheckResolve is what a saved rule is held to.
func WithResolve(ctx context.Context, rules map[string][]string) context.Context {
	if len(rules) == 0 {
		return ctx
	}
	return context.WithValue(ctx, resolveKey{}, rules)
}

func resolveOf(ctx context.Context) map[string][]string {
	if ctx == nil {
		return nil
	}
	r, _ := ctx.Value(resolveKey{}).(map[string][]string)
	return r
}

type directKey struct{}

// resolveRoute is the route's name for req's host rule (WithResolve): the
// host and its places, "" when req's host has none.
func resolveRoute(req *http.Request) string {
	rules := resolveOf(req.Context())
	if len(rules) == 0 || req.URL == nil {
		return ""
	}
	h := strings.ToLower(strings.TrimSuffix(req.URL.Hostname(), "."))
	to := rules[h]
	if len(to) == 0 {
		return ""
	}
	return h + "\x00" + strings.Join(to, "\x00")
}

// resolveTry is how long one address of a rule is given to take the
// connection before the next is tried.
var resolveTry = 8 * time.Second

// lookupHost is how a "dns:" place is resolved; swapped in tests.
var lookupHost = func(ctx context.Context, name string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, name)
}

// resolveTLS has t, a clone kept for one host's rule (resolveRoute), dial
// that host at the rule's places. Only a connection a request meant to
// make direct (directKey) is changed: the dial of a proxy, which may be
// at a name the rule has too, is left alone.
func resolveTLS(t *http.Transport, route string) {
	parts := strings.Split(route, "\x00")
	host, places := parts[0], parts[1:]
	next := t.DialContext
	if next == nil {
		next = (&net.Dialer{}).DialContext
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		h, port, err := net.SplitHostPort(addr)
		if err != nil || !strings.EqualFold(strings.TrimSuffix(h, "."), host) || ctx.Value(directKey{}) == nil {
			return next(ctx, network, addr)
		}
		for _, pl := range places {
			for _, at := range placeAddrs(ctx, pl, port) {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				try, cancel := context.WithTimeout(ctx, resolveTry)
				c, err := next(try, network, at)
				cancel()
				if err == nil {
					return c, nil
				}
			}
		}
		return next(ctx, network, addr)
	}
}

// placeAddrs is the host:port addresses a place of a rule stands for, in
// the order they are tried: an IP address as it is, a "dns:" name's as
// its resolver gave them. A name that doesn't resolve has none.
func placeAddrs(ctx context.Context, place, port string) []string {
	name, dns := strings.CutPrefix(place, "dns:")
	host, p := splitPlace(name)
	if p == "" {
		p = port
	}
	if !dns {
		return []string{net.JoinHostPort(host, p)}
	}
	rctx, cancel := context.WithTimeout(ctx, resolveTry)
	defer cancel()
	ips, err := lookupHost(rctx, host)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.JoinHostPort(ip, p))
	}
	return out
}

// splitPlace is a place's host and the port it names, "" for none:
// 10.0.0.1, 10.0.0.1:8443, [fd00::1]:8443, fd00::1, gw.corp:8443.
func splitPlace(s string) (host, port string) {
	if h, p, err := net.SplitHostPort(s); err == nil {
		return h, p
	}
	return strings.Trim(s, "[]"), ""
}

// Limits of what a rule set holds.
const (
	maxResolveHosts  = 32
	maxResolvePlaces = 16
)

// CheckResolve says why rules can't be kept, and returns them as they are
// kept: host names in lower case, places trimmed, repeated places left
// out. nil for none. A host is a name (not an address or a URL), and has
// at least one place: an IP address, with a port if the place is not on
// the URL's, or "dns:" and the name to follow.
func CheckResolve(rules map[string][]string) (map[string][]string, error) {
	var out map[string][]string
	if len(rules) > maxResolveHosts {
		return nil, fmt.Errorf("resolve: at most %d host names", maxResolveHosts)
	}
	for h, places := range rules {
		host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
		if err := checkName(host); err != nil {
			return nil, fmt.Errorf("resolve: %q: %w", h, err)
		}
		if _, err := netip.ParseAddr(host); err == nil {
			return nil, fmt.Errorf("resolve: %q is an address: give the host name the URL has", h)
		}
		if _, dup := out[host]; dup {
			return nil, fmt.Errorf("resolve: %q is given twice", host)
		}
		var kept []string
		for _, pl := range places {
			pl = strings.TrimSpace(pl)
			if pl == "" {
				continue
			}
			if err := checkPlace(pl); err != nil {
				return nil, fmt.Errorf("resolve: %s: %q: %w", host, pl, err)
			}
			if !slices.Contains(kept, pl) {
				kept = append(kept, pl)
			}
		}
		if len(kept) == 0 {
			return nil, fmt.Errorf("resolve: %s needs an IP address or dns:<name> to connect to", host)
		}
		if len(kept) > maxResolvePlaces {
			return nil, fmt.Errorf("resolve: %s: at most %d addresses", host, maxResolvePlaces)
		}
		if out == nil {
			out = map[string][]string{}
		}
		out[host] = kept
	}
	return out, nil
}

// checkName says why s isn't a host name.
func checkName(s string) error {
	if s == "" {
		return errors.New("a host name is needed")
	}
	if strings.ContainsAny(s, " \t/\\@?#:*[]") {
		return errors.New("give a bare host name, as in the URL: no scheme, port or path")
	}
	return nil
}

// checkPlace says why s isn't a place a rule connects to.
func checkPlace(s string) error {
	name, dns := strings.CutPrefix(s, "dns:")
	host, port := splitPlace(name)
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return errors.New("the port isn't a number from 1 to 65535")
		}
	}
	if dns {
		if err := checkName(host); err != nil {
			return fmt.Errorf("dns: needs a host name: %w", err)
		}
		return nil
	}
	if _, err := netip.ParseAddr(host); err != nil {
		return errors.New("not an IP address (a name goes as dns:<name>)")
	}
	return nil
}

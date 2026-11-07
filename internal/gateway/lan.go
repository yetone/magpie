package gateway

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/netguard"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// The gateway accepts named caller keys locally and, while shared or on the
// network, remotely. Loopback clients may still use any token, including a
// stale named key.

// listenAddr is where the gateway listens: every interface while it is
// shared, on its port, else its address. A host MAGPIE_ADDR names itself
// (127.0.0.1 behind Tailscale Serve, one interface's address) is kept
// while shared too: sharing asks remote callers for a gateway key, and
// never widens the listener past the address the user chose (#1112).
func listenAddr() string {
	if s := settings.Load(); s.LAN && pinnedHost() == "" {
		return "0.0.0.0:" + Port()
	}
	return Addr()
}

// pinnedHost is the host MAGPIE_ADDR names, "" when it isn't set or names
// every interface (0.0.0.0, ::, or no host).
func pinnedHost() string {
	a := os.Getenv("MAGPIE_ADDR")
	if a == "" {
		return ""
	}
	h, _, err := net.SplitHostPort(a)
	if err != nil || h == "" {
		return ""
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsUnspecified() {
		return ""
	}
	return h
}

// Port is the gateway's port.
func Port() string {
	_, p, err := net.SplitHostPort(Addr())
	if err != nil {
		return strconv.Itoa(settings.DefaultPort)
	}
	return p
}

func migrateLANKey() error { return access.MigrateLegacyLANKey() }

func migrateLANKeyBestEffort() {
	access.MigrateLegacyLANKeyBestEffort()
}

func logInvalidPublicURL() {
	log.Printf("ignoring invalid MAGPIE_PUBLIC_URL: expected an HTTP or HTTPS address with a host")
}

var warnInvalidPublicURL = sync.OnceFunc(logInvalidPublicURL)

// publicURL is the validated MAGPIE_PUBLIC_URL, with a scheme and without
// trailing slashes. An invalid value is treated as unset and warned once.
func publicURL() string {
	u := strings.TrimSpace(os.Getenv("MAGPIE_PUBLIC_URL"))
	if u == "" {
		return ""
	}
	// Keep a scheme-only value invalid after trimming its slashes.
	hasScheme := strings.Contains(u, "://")
	u = strings.TrimRight(u, "/")
	if !hasScheme {
		u = Scheme() + "://" + u
	}
	parsed, err := url.Parse(u)
	// A query, a fragment or credentials would be lost or shown when the
	// address is joined with a path, so only a plain base URL is taken.
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		warnInvalidPublicURL()
		return ""
	}
	return u
}

// PublicURL is MAGPIE_PUBLIC_URL, with a scheme; "" when it isn't set.
func PublicURL() string { return publicURL() }

// OnNetwork: MAGPIE_ADDR puts the gateway beyond loopback (0.0.0.0 in the
// Docker image, a server's or one interface's address), so it is reached
// from other machines, and from a container's host, whether or not it is
// shared from Settings. Such callers need an enabled gateway key, as they
// do while it is shared: nothing that reaches it from elsewhere is let in
// on any key.
func OnNetwork() bool {
	h, _, err := net.SplitHostPort(Addr())
	return err == nil && h != "localhost" && !net.ParseIP(h).IsLoopback()
}

// remoteKeyed: requests from another machine are answered, with an enabled
// gateway key — while the gateway is shared, or listens on the network.
func remoteKeyed() bool { return settings.Load().LAN || OnNetwork() }

// PublicHost is MAGPIE_PUBLIC_URL's host, "" when it isn't set.
func PublicHost() string {
	u, err := url.Parse(publicURL())
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// ContainerAddrs: the addresses magpie finds for itself are a container's,
// which other machines can't reach, and MAGPIE_PUBLIC_URL doesn't say the
// host's. A container on the host's network (network_mode: host, which
// NAS panels offer) has the host's own addresses, and they are said as
// they are.
func ContainerAddrs() bool {
	return publicURL() == "" && inContainer("/") && !hostNetwork(ifaceNames())
}

// hostNetwork: the interfaces names are the host's, not a container's own:
// a host running containers has their bridges and veth pairs (docker0,
// br-…, veth…, Podman's cni-podman0 or podman0), which a container on a
// bridge network never sees; it has its eth0 alone.
func hostNetwork(names []string) bool {
	for _, n := range names {
		for _, p := range []string{"docker0", "br-", "veth", "cni-podman", "podman"} {
			if strings.HasPrefix(n, p) {
				return true
			}
		}
	}
	return false
}

// ifaceNames are this system's network interfaces' names; a variable for
// the tests.
var ifaceNames = func() []string {
	ifs, _ := net.Interfaces()
	out := make([]string, 0, len(ifs))
	for _, i := range ifs {
		out = append(out, i.Name)
	}
	return out
}

// ContainerHostURL is where other containers on this host reach the
// gateway while magpie runs in a container on a bridge network: the host
// at the container's default route, the bridge's gateway (Docker's
// 172.17.0.1), on the gateway's port as published. The container's own
// address is reached only from its own Docker network, and the host's LAN
// address may not be reached from a container through the NAS's firewall
// (jiezhengj on Discord: 192.168.31.218 worked from other devices, and
// other containers on the NAS needed 172.17.0.1); host.docker.internal is
// not there on Linux unless a container is given it. "" when magpie is
// not in a container, is on the host's network, or has no default route.
func ContainerHostURL() string {
	if !inThisContainer() || hostNetwork(ifaceNames()) {
		return ""
	}
	gw := defaultGateway(routeTable())
	if gw == "" {
		return ""
	}
	return Scheme() + "://" + net.JoinHostPort(gw, Port())
}

// inThisContainer is inContainer("/"); a variable for the tests.
var inThisContainer = func() bool { return inContainer("/") }

// routeTable is Linux's IPv4 routing table; a variable for the tests.
var routeTable = func() string {
	b, _ := os.ReadFile("/proc/net/route")
	return string(b)
}

// defaultGateway is the gateway of /proc/net/route's default route: its
// Destination 00000000 with RTF_GATEWAY (0x2) set, the address in the
// kernel's byte order, little-endian on every machine Docker images are
// built for here (amd64, arm64). "" when there is none.
func defaultGateway(table string) string {
	for _, line := range strings.Split(table, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || f[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(f[3], 16, 16)
		if err != nil || flags&0x2 == 0 {
			continue
		}
		v, err := strconv.ParseUint(f[2], 16, 32)
		if err != nil || v == 0 {
			continue
		}
		ip := netip.AddrFrom4([4]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)})
		if ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		return ip.String()
	}
	return ""
}

// InContainer: this magpie runs in a container (Docker, Podman, a
// Kubernetes pod), whose image is what gets updated, not its binary.
func InContainer() bool { return inContainer("/") }

// inContainer: the system under root is a container's — Docker's or
// Podman's marker file, or a container runtime in PID 1's cgroup.
func inContainer(root string) bool {
	for _, f := range []string{".dockerenv", "run/.containerenv"} {
		if _, err := os.Stat(filepath.Join(root, f)); err == nil {
			return true
		}
	}
	b, _ := os.ReadFile(filepath.Join(root, "proc/1/cgroup"))
	for _, w := range []string{"docker", "containerd", "kubepods", "libpod", "lxc"} {
		if strings.Contains(string(b), w) {
			return true
		}
	}
	return false
}

// LANURLs are the addresses other machines on the network reach the
// gateway at: MAGPIE_PUBLIC_URL when set, else one per IPv4 address this
// computer has there.
func LANURLs() []string {
	if u := publicURL(); u != "" {
		return []string{u}
	}
	// a gateway MAGPIE_ADDR keeps on one host is reached there alone; on
	// loopback, only through a proxy, whose address MAGPIE_PUBLIC_URL says
	if h := pinnedHost(); h != "" {
		if ip := net.ParseIP(h); h == "localhost" || ip != nil && ip.IsLoopback() {
			return nil
		}
		return []string{Scheme() + "://" + net.JoinHostPort(h, Port())}
	}
	var out []string
	for _, h := range LANHosts() {
		out = append(out, Scheme()+"://"+net.JoinHostPort(h, Port()))
	}
	return out
}

// LANHosts are this computer's IPv4 addresses other machines can reach it
// at, on every interface that is up: a private one, and a public one an
// ISP assigns to the network card itself. The gateway's addresses and
// `magpie web`'s links are both made from these, so the two list the
// same computer the same way (a static public address was in one and not
// the other). Private ones come first: in the interfaces' order the public
// one came first, and Settings offered it as the address under "Share on
// local network" (the same user, asking why sharing was on the internet).
// Of the private ones, RFC 1918's (a home or office network) come before
// carrier-grade NAT's (Tailscale). A proxy's TUN address is left out:
// 198.18.0.0/15 (netguard.FakeIP), which Clash, Mihomo, sing-box, Surge
// and Stash take for their TUN interfaces and fake-ip answers, and which
// no network hands out: no other device reaches this computer there, and
// Settings offered http://198.18.0.1 instead of 192.168.110.x (coeo91 on
// Discord). It goes by the address, not the interface's name: Tailscale's
// is a utun too, and its address is reached.
func LANHosts() []string {
	var home, near, far []string
	for _, ip := range hostIPs() {
		if ip.To4() == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || fakeIP(ip) {
			continue
		}
		switch {
		case ip.IsPrivate():
			home = append(home, ip.String())
		case PublicIP(ip):
			far = append(far, ip.String())
		default:
			near = append(near, ip.String())
		}
	}
	return slices.Concat(home, near, far)
}

// fakeIP: ip is a proxy's TUN or fake-ip address (netguard.FakeIP).
func fakeIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	return ok && netguard.FakeIP(a)
}

// sharedRanges are IPv4 ranges outside RFC 1918 that still aren't the
// internet: carrier-grade NAT, which Tailscale uses, and the benchmarking
// range proxies' TUN interfaces (Clash, Surge) take.
var sharedRanges = []*net.IPNet{
	{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)},
	{IP: net.IPv4(198, 18, 0, 0), Mask: net.CIDRMask(15, 32)},
}

// PublicIP: ip is an internet address, which any computer online may
// reach, not one on a private network.
func PublicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return false
	}
	for _, n := range sharedRanges {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// URLKind is what u's host is: "internet" for an address anyone online
// may reach (PublicIP), "local" for any other IP address (a home or
// office network's, Tailscale's, a Docker network's), and "" for a name,
// which magpie doesn't look up. MAGPIE_PUBLIC_URL is often a private one:
// a NAS's 192.168.31.218, which the GUI called "Public address" (公网地址)
// beside the 172.17.0.1 other containers on it use (jiezhengj on Discord).
func URLKind(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	ip := net.ParseIP(p.Hostname())
	switch {
	case ip == nil:
		return ""
	case PublicIP(ip):
		return "internet"
	}
	return "local"
}

// PublicURLs are those of urls (LANURLs) at an internet address.
func PublicURLs(urls []string) []string {
	var out []string
	for _, u := range urls {
		p, err := url.Parse(u)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(p.Hostname()); ip != nil && PublicIP(ip) {
			out = append(out, u)
		}
	}
	return out
}

// hostIPs are the addresses on this computer's interfaces that are up,
// loopback's left out; a variable for the tests.
var hostIPs = func() []net.IP {
	var out []net.IP
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				out = append(out, n.IP)
			}
		}
	}
	return out
}

// listen listens on addr, serving TLS to other computers when the gateway
// does; under lnMu, or before serving.
func (s *Server) listen(addr string) (net.Listener, error) {
	ln, err := Listen(addr)
	if err != nil || s.tls == nil {
		return ln, err
	}
	return newTLSListener(ln, s.tls), nil
}

// Relisten moves the gateway to where settings now say it listens — onto
// the network or back to loopback, or to another port. Requests in flight
// finish.
func (s *Server) Relisten() error {
	migrateLANKeyBestEffort()
	s.lnMu.Lock()
	defer s.lnMu.Unlock()
	if s.ln == nil {
		return nil // not serving
	}
	to := listenAddr()
	if s.ln.Addr().String() == to {
		return nil
	}
	was := s.ln.Addr().String()
	// another port (Settings' port changed): the new one is taken up before
	// the old one lets go, so one the gateway can't have leaves it serving
	// where it was
	if _, p, _ := net.SplitHostPort(was); p != Port() {
		ln, err := s.listen(to)
		if err != nil {
			return err
		}
		old := s.ln
		s.ln = ln
		old.Close()
		return nil
	}
	// the port is the same, so the old one goes first
	s.ln.Close()
	ln, err := s.listen(to)
	if err != nil {
		if ln, _ = s.listen(was); ln == nil {
			return err
		}
		s.ln = ln
		return err
	}
	s.ln = ln
	return nil
}

// lanGuard: a request from an address the allow list (allow.go) leaves out
// is refused first, whatever key it carries. A request from another machine — any peer that isn't
// loopback, and a proxy's or tunnel's on loopback — needs an enabled gateway
// key, and is answered at all only while the gateway is shared or listens on
// the network (OnNetwork). Loopback clients may still use any token,
// including a stale named key.
func lanGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ownCallback(r) {
			next.ServeHTTP(w, r)
			return
		}
		r = takePagePort(r)
		// a WSL distro's agent, at the address Windows has on its network
		// (wsl.go), is one of this computer's
		if fromWSL(r) {
			r = asFromLoopback(r)
		}
		// the allow list comes before the key: one that leaked is of no
		// use from an address left out of it
		if refuseNotAllowed(w, r) {
			return
		}
		remote := !local(r)
		if !remote && rebound(r) {
			log.Printf("refused %s %s: a web page reached loopback under the name %q", r.Method, r.URL.Path, r.Host)
			writeError(w, provider.Chat, http.StatusForbidden, "magpie doesn't answer a browser that reached it as "+r.Host+" (a web site's name pointed at this computer): call it as localhost or 127.0.0.1, or send an enabled gateway key")
			return
		}
		if remote && !remoteKeyed() {
			if viaPagePort(r) {
				log.Printf("refused %s %s on magpie web's page port: magpie isn't shared", r.Method, r.URL.Path)
				http.Error(w, "this request came to magpie web's page port, which answers the gateway's routes as from another machine (the port may sit behind a proxy or a forward): only while Settings → Network and sharing → Share on local network is on, with an enabled gateway key (Gateway → Gateway keys) sent as Authorization: Bearer <key> or x-api-key. An agent on this computer calls the gateway's own address ("+URL()+") instead, which needs no key", http.StatusForbidden)
				return
			}
			if proxied(r) {
				log.Printf("refused %s %s through a proxy or tunnel: magpie isn't shared", r.Method, r.URL.Path)
				http.Error(w, "this request came through a proxy or tunnel (it carries a forwarding header such as X-Forwarded-For or Cf-Connecting-IP), so magpie answers it as one from another machine: only while Settings → Network and sharing → Share on local network is on, with an enabled gateway key (Gateway → Gateway keys) sent as Authorization: Bearer <key> or x-api-key", http.StatusForbidden)
				return
			}
			http.Error(w, "magpie isn't shared on the local network", http.StatusForbidden)
			return
		}
		// a browser's preflight carries no key, and CORS forbids putting
		// one on it: a listed origin's could never pass the key check
		// below, so corsGuard, next, answers it (204 and the headers) with
		// nothing behind it reached, while the call it asks after still
		// needs the key (#4931: a preflight from another machine, direct on
		// the LAN or behind a reverse proxy, was refused 401 for the key it
		// may not carry, and the browser gave up before the keyed call).
		// Any other origin's meets the key check as before: corsGuard
		// passes some on (a page on this computer, the request's own
		// origin, an app's scheme), and a route that takes OPTIONS
		// (Codex's relay, the MCP paths) would serve it with no key.
		if remote && corsPreflight(r) && corsAllowed(r.Header.Get("Origin")) {
			next.ServeHTTP(w, r)
			return
		}
		// the key page itself is the same bytes for everyone, so it is
		// opened before the key it asks for is typed; its data isn't
		if remote && keyPageOpen(r) {
			next.ServeHTTP(w, r)
			return
		}
		// a link the image server handed out opens with its own token
		// and no key (imageTokenAsked); the handler judges the token, and
		// refuses one that opens nothing as a request without a key
		if remote && imageTokenAsked(r) {
			next.ServeHTTP(w, r)
			return
		}
		// a CI job's OIDC token traded for a key carries the token, not a
		// key: the handler verifies it, and counts one it refuses against
		// tooManyAuthFails (oidc_keys.go)
		if remote && oidcExchange(r) {
			next.ServeHTTP(w, r)
			return
		}
		// a magpie's proof that it holds a key is asked with none: the
		// handler answers only a key's holder, refuses a browser, and
		// counts an unknown key against tooManyAuthFails (remote_prove.go)
		if remote && proveAsked(r) {
			next.ServeHTTP(w, r)
			return
		}
		if remote || managedKey(r) {
			var ok bool
			r, ok = identifyCaller(w, r)
			if !ok {
				return
			}
			if remote {
				r = r.WithContext(context.WithValue(r.Context(), lanKeyed{}, true))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// callbacks are the callback tokens of the Claude subscription runs now
// live (addCallback, dropCallback).
var callbacks = struct {
	sync.Mutex
	tokens map[string]bool
}{tokens: map[string]bool{}}

// addCallback makes token, a run's callback token (randomToken: 24 bytes
// from crypto/rand), one ownCallback takes, until dropCallback.
func addCallback(token string) {
	callbacks.Lock()
	callbacks.tokens[token] = true
	callbacks.Unlock()
}

func dropCallback(token string) {
	callbacks.Lock()
	delete(callbacks.tokens, token)
	callbacks.Unlock()
}

// ownCallback: r is the MCP helper of a Claude Code magpie runs handing a
// tool call back, on the path it was given — POST claudeMCPPath and the
// callback token of a run now live, nothing else. It is let in without a
// gateway key from wherever it comes: the helper calls the address the
// gateway listens on, and with MAGPIE_ADDR on one interface's address (a
// LAN, Tailscale or loopback-alias IP), which isn't loopback, it was asked
// for a key it doesn't have (401). Its tool call never reached the run,
// so every turn with tool results found no run waiting and started a new
// Claude Code told the whole conversation, its prompt cache written anew
// (#1546, jiayeZhu). The token, unguessable and handed to the helper in a
// file only this user reads, is what lets it in: a wrong, spent or missing
// one, another method, or any other path is judged as every request is.
func ownCallback(r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.RawPath != "" {
		return false
	}
	token, ok := strings.CutPrefix(r.URL.Path, claudeMCPPath)
	if !ok || token == "" {
		return false
	}
	// while the magpie this one took the gateway over from finishes, a
	// token this one doesn't have may be one of that one's runs: it is
	// passed on there (forwardCallback), and that magpie judges it as it
	// would its own
	if forwarding() {
		return true
	}
	callbacks.Lock()
	defer callbacks.Unlock()
	found := false
	for t := range callbacks.tokens {
		if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			found = true
		}
	}
	return found
}

// rebound: a browser sent this request to loopback under a hostname that
// isn't this computer's — a web site whose DNS answered 127.0.0.1 after its
// page loaded (DNS rebinding). The page is then same-origin with the
// gateway: it sends no Origin on a GET, or one equal to the Host, so
// corsGuard lets it by, and on loopback a call needs no key, so the page
// could read the models and quotas and spend the user's subscriptions.
// Agents call magpie as localhost or an IP, and send none of a browser's
// marks; a hostname of this computer's own (MAGPIE_PUBLIC_URL's,
// MAGPIE_ADDR's, its own name, a container runtime's name for the host)
// isn't a web site's. An enabled gateway key, which such a page can't
// know, still lets a call in.
func rebound(r *http.Request) bool {
	if !fromBrowser(r) || ownHost(r.Host) {
		return false
	}
	return !slices.ContainsFunc(callerKeys(r), func(k string) bool { _, ok := access.Authenticate(k); return ok })
}

// fromBrowser: the request carries what a browser puts on every request
// and a page's script can't take off.
func fromBrowser(r *http.Request) bool {
	return r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" ||
		r.Header.Get("Sec-Fetch-Mode") != "" || strings.HasPrefix(r.UserAgent(), "Mozilla/")
}

// containerHostNames are the names container runtimes give the host.
var containerHostNames = []string{"host.docker.internal", "gateway.docker.internal", "host.containers.internal", "host.orb.internal", "docker.for.mac.localhost"}

// ownHost: host (a Host header) names this computer — an IP, localhost,
// or one of the names above. A subdomain of localhost doesn't (#2256):
// Safari and other WebKit views ask the DNS for it, so a page served at
// one from anywhere can have it turned to 127.0.0.1 after it loads.
func ownHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "" || net.ParseIP(host) != nil || host == "localhost" || slices.Contains(containerHostNames, host) {
		return true
	}
	if strings.EqualFold(host, PublicHost()) {
		return true
	}
	if h, _, err := net.SplitHostPort(Addr()); err == nil && strings.EqualFold(host, h) {
		return true
	}
	if name, err := os.Hostname(); err == nil {
		name = strings.TrimSuffix(strings.ToLower(name), ".local")
		if name != "" && (host == name || host == name+".local") {
			return true
		}
	}
	return false
}

// callerGuard also covers embedded handlers used by the web app and tests.
func callerGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if access.Caller(r.Context()).KeyID == "" && managedKey(r) && (local(r) || remoteKeyed()) {
			var ok bool
			r, ok = identifyCaller(w, r)
			if !ok {
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func identifyCaller(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	var who access.Identity
	ok := false
	var refused *access.Refused
	for _, k := range callerKeys(r) {
		var err error
		if who, err = access.Check(k); err == nil {
			ok = true
			break
		}
		if refused == nil {
			errors.As(err, &refused)
		}
	}
	if !ok && refused != nil {
		// an expired key, or one its issuer no longer lets in, is told so
		// on every route, from this computer too: a sandbox's agent on
		// loopback holding a key that has run out is not let on as one
		// with no key, which would hand it everything its key held it from
		log.Printf("refused %s %s from %s: %s", r.Method, r.URL.Path, r.RemoteAddr, refused.Why)
		writeError(w, provider.Chat, http.StatusUnauthorized, refused.Why)
		return r, false
	}
	if !ok && !local(r) {
		if over, retry := tooManyAuthFails(); over {
			// A flood of wrong keys: stop answering each one (no 401 oracle,
			// no unbounded work) until the bucket refills. A valid key never
			// reaches here, so a real caller is not held.
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
			writeError(w, provider.Chat, http.StatusTooManyRequests, "too many failed authentications; slow down and send an enabled magpie gateway key")
			return r, false
		}
		msg := refusedKey(callerKey(r))
		log.Printf("refused %s %s from %s: %s", r.Method, r.URL.Path, r.RemoteAddr, msg)
		writeError(w, provider.Chat, http.StatusUnauthorized, msg)
		return r, false
	}
	if ok {
		r = r.WithContext(access.WithIdentity(r.Context(), who))
	}
	r.Header = r.Header.Clone()
	r.Header.Set("Authorization", "Bearer "+Token)
	for _, h := range []string{"x-api-key", "x-goog-api-key"} {
		if r.Header.Get(h) != "" {
			r.Header.Set(h, Token)
		}
	}
	if q := r.URL.Query(); q.Get("key") != "" {
		q.Set("key", Token)
		u := *r.URL
		u.RawQuery = q.Encode()
		r.URL = &u
	}
	return r, true
}

// accountOf is the subscription account p answers as, named as the Routing
// trace names it (Account.User: an email, a login), never by a token; ""
// for a key or a provider without an account (#557).
func accountOf(p provider.Provider) string {
	if p.Account == nil {
		return ""
	}
	return p.Account.User
}

func appendUsage(r *http.Request, rec usage.Record) {
	if rec.Session == "" {
		rec.Session = sessionOf(r.Header)
	}
	if rec.Subagent == "" {
		rec.Subagent, rec.ParentAgent = subagentOf(r.Header)
	}
	who := access.Caller(r.Context())
	rec.CallerKeyID, rec.CallerKeyName, rec.CallerIssuerID = who.KeyID, who.KeyName, who.IssuerID()
	rec.Local = local(r)
	if r.Context().Value(observedKey{}) != nil {
		rec.Watched = true
	}
	if !rec.IsProbe() {
		// a request an MCP tool call made: its row names the model (mcpUse)
		mcpUseOf(r.Context()).used(rec.Provider, rec.Model)
	}
	budget.Append(rec)
}

// lanKeyed marks a request from another machine that carried the key.
type lanKeyed struct{}

// sharedWith: the request came from another machine with the key, the
// gateway shared from the Settings page.
func sharedWith(r *http.Request) bool {
	ok, _ := r.Context().Value(lanKeyed{}).(bool)
	return ok
}

// callerKey is the API key a request carries, however its client sends one:
// the first of callerKeys, "" for none.
func callerKey(r *http.Request) string {
	if ks := callerKeys(r); len(ks) > 0 {
		return ks[0]
	}
	return ""
}

// callerKeys are the API keys a request carries, in the order they are
// read: Authorization's bearer token, x-api-key, x-goog-api-key, ?key=. A
// header that carries none ("Authorization: Bearer ", sent beside an
// x-api-key by a client with no token set) is passed over, and a client
// that sends two different values — a stale or placeholder token in
// Authorization (ANTHROPIC_AUTH_TOKEN left at magpie), the key in x-api-key
// — is let in by whichever is an enabled gateway key. Loopback takes any
// token, so this mattered only from another computer, which was refused
// what this one was answered (悠悠哥 on Discord: a client's model list from
// magpie shared on a NAS failed, from magpie on 127.0.0.1 it came).
func callerKeys(r *http.Request) []string {
	var out []string
	add := func(v string) {
		if v = strings.TrimSpace(v); v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	a := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(a) >= 7 && strings.EqualFold(a[:7], "Bearer ") {
		a = a[7:]
	} else if strings.EqualFold(a, "Bearer") {
		a = ""
	}
	add(a)
	for _, h := range []string{"x-api-key", "x-goog-api-key"} {
		add(r.Header.Get(h))
	}
	add(r.URL.Query().Get("key"))
	return out
}

// managedKey: the request carries a named gateway key's form (sk-magpie-…),
// or a key's own value a user gave it, in any of the places a key is read.
func managedKey(r *http.Request) bool {
	return slices.ContainsFunc(callerKeys(r), access.Named)
}

// refusedKey says why a caller's key was turned away: none came, or the
// one that came (its last four characters, and only of a long one) is no enabled
// gateway key, so a client that drops its key is told apart from a wrong
// key (#545).
func refusedKey(key string) string {
	if key == "" {
		return "no API key was sent: send a magpie gateway key as Authorization: Bearer <key> (or x-api-key)"
	}
	which := "the API key sent"
	if len(key) >= 12 { // a short one isn't named, so as not to give most of it away
		which = "the API key ending in " + key[len(key)-4:]
	}
	return which + " is not an enabled magpie gateway key: it is disabled, removed or mistyped"
}

// local is a request from this computer: one that reached the gateway on
// loopback, wasn't passed on by a proxy or tunnel running here, and didn't
// come through `magpie web`'s page port (viaPagePort).
func local(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() && !proxied(r) && !viaPagePort(r)
}

// forwardHeaders are what a proxy or tunnel adds to a request it passes
// on: cloudflared (Cf-Connecting-IP, and X-Forwarded-For beside it),
// ngrok, Tailscale serve and funnel, frp's and Caddy's and nginx's HTTP
// proxies. An agent calling magpie sends none of them.
var forwardHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Real-IP", "Cf-Connecting-IP", "True-Client-IP", "Tailscale-User-Login"}

// proxied: the request came to loopback through a proxy or tunnel on this
// computer (#1022, znjhahaha: a Cloudflare Tunnel to 127.0.0.1:3425), so it
// is someone else's, from wherever the proxy reaches, and is treated as
// from another machine: answered only while magpie is shared, with an
// enabled gateway key. MAGPIE_TRUST_PROXY=1 says a proxy here signs its
// clients in itself, and keeps what it forwards local, as before.
func proxied(r *http.Request) bool {
	if trustProxy() {
		return false
	}
	return slices.ContainsFunc(forwardHeaders, func(h string) bool { return r.Header.Get(h) != "" })
}

func trustProxy() bool {
	v, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv("MAGPIE_TRUST_PROXY")))
	return v
}

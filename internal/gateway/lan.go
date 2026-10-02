package gateway

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// The gateway accepts named caller keys locally and, while shared, remotely.
// Loopback clients may still use any token, including a stale named key.

// listenAddr is where the gateway listens: every interface while it is
// shared, on its port, else its address.
func listenAddr() string {
	if s := settings.Load(); s.LAN {
		return "0.0.0.0:" + Port()
	}
	return Addr()
}

// Port is the gateway's port.
func Port() string {
	_, p, err := net.SplitHostPort(Addr())
	if err != nil {
		return "3425"
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
		u = "http://" + u
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

// OpenToAnyone: the gateway listens beyond loopback (MAGPIE_ADDR) and isn't
// shared, so anyone who reaches it is let in with any key. Shared, it asks
// for an enabled gateway key instead.
func OpenToAnyone() bool {
	if settings.Load().LAN {
		return false
	}
	h, _, err := net.SplitHostPort(Addr())
	return err == nil && h != "localhost" && !net.ParseIP(h).IsLoopback()
}

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
// host's.
func ContainerAddrs() bool { return publicURL() == "" && inContainer("/") }

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
	var out []string
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil || n.IP.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, "http://"+net.JoinHostPort(n.IP.String(), Port()))
		}
	}
	return out
}

// Relisten moves the gateway to where settings now say it listens — onto
// the network or back to loopback. Requests in flight finish.
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
	// the port is the same, so the old one goes first
	s.ln.Close()
	ln, err := Listen(to)
	if err != nil {
		if ln, _ = Listen(was); ln == nil {
			return err
		}
		s.ln = ln
		return err
	}
	s.ln = ln
	return nil
}

// lanGuard requires a named caller key on the local network. An explicit
// MAGPIE_ADDR without LAN sharing retains its existing open-gateway behavior.
func lanGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote := !local(r)
		shared := settings.Load().LAN
		if remote && !shared && os.Getenv("MAGPIE_ADDR") == "" {
			http.Error(w, "magpie isn't shared on the local network", http.StatusForbidden)
			return
		}
		if (remote && shared) || (!remote && access.Managed(callerKey(r))) {
			var ok bool
			r, ok = identifyCaller(w, r)
			if !ok {
				return
			}
			if remote && shared {
				r = r.WithContext(context.WithValue(r.Context(), lanKeyed{}, true))
			}
		}
		next.ServeHTTP(w, r)
	})
}

// callerGuard also covers embedded handlers used by the web app and tests.
func callerGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if access.Caller(r.Context()).KeyID == "" && access.Managed(callerKey(r)) && (local(r) || settings.Load().LAN) {
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
	key := callerKey(r)
	who, ok := access.Authenticate(key)
	if !ok && !local(r) {
		msg := refusedKey(key)
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
	who := access.Caller(r.Context())
	rec.CallerKeyID, rec.CallerKeyName = who.KeyID, who.KeyName
	usage.Append(rec)
}

// lanKeyed marks a request from another machine that carried the key.
type lanKeyed struct{}

// sharedWith: the request came from another machine with the key, the
// gateway shared from the Settings page.
func sharedWith(r *http.Request) bool {
	ok, _ := r.Context().Value(lanKeyed{}).(bool)
	return ok
}

// callerKey is the API key a request carries, however its client sends one.
func callerKey(r *http.Request) string {
	if a := r.Header.Get("Authorization"); a != "" {
		return strings.TrimSpace(strings.TrimPrefix(a, "Bearer "))
	}
	for _, h := range []string{"x-api-key", "x-goog-api-key"} {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	return r.URL.Query().Get("key")
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

// local is a request from this computer.
func local(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

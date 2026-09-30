package gateway

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// The gateway accepts named caller keys locally and, while shared, remotely.
// Loopback clients may still use the usual magpie token.

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

// publicURL is MAGPIE_PUBLIC_URL, the base URL other machines are told to
// reach the gateway at, without its trailing slashes: a magpie in a
// container sees only the container's own addresses, never the host's.
func publicURL() string { return strings.TrimRight(os.Getenv("MAGPIE_PUBLIC_URL"), "/") }

// lanPublicURL is publicURL with a scheme, as a link needs.
func lanPublicURL() string {
	u := publicURL()
	if u != "" && !strings.Contains(u, "://") {
		u = "http://" + u
	}
	return u
}

// PublicHost is MAGPIE_PUBLIC_URL's host, "" when it isn't set.
func PublicHost() string {
	u, err := url.Parse(lanPublicURL())
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
	if u := lanPublicURL(); u != "" {
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
	if err := migrateLANKey(); err != nil {
		return err
	}
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
		if (remote && shared) || access.Managed(callerKey(r)) {
			var ok bool
			r, ok = identifyCaller(w, r)
			if !ok {
				return
			}
		}
		if remote && settings.Load().LAN {
			r = r.WithContext(context.WithValue(r.Context(), lanKeyed{}, true))
		}
		next.ServeHTTP(w, r)
	})
}

// lanKeyed marks a request from another machine that carried the key.
type lanKeyed struct{}

// sharedWith: the request came from another machine with the key, the
// gateway shared from the Settings page.
func sharedWith(r *http.Request) bool {
	ok, _ := r.Context().Value(lanKeyed{}).(bool)
	return ok
}

// callerGuard also covers embedded handlers used by the web app and tests.
func callerGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if access.Caller(r.Context()).KeyID == "" && access.Managed(callerKey(r)) {
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
	who, ok := access.Authenticate(callerKey(r))
	if !ok {
		writeError(w, provider.Chat, http.StatusUnauthorized, "API key is disabled, removed or invalid")
		return r, false
	}
	r = r.WithContext(access.WithIdentity(r.Context(), who))
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

func appendUsage(r *http.Request, rec usage.Record) {
	who := access.Caller(r.Context())
	rec.CallerKeyID, rec.CallerKeyName = who.KeyID, who.KeyName
	usage.Append(rec)
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

// local is a request from this computer.
func local(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

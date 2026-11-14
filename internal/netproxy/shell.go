package netproxy

import (
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// A proxy exported in the shell profile (`export https_proxy=…` in
// .zshrc, as Clash's and most proxy apps' guides say) reaches every
// program run in a terminal, magpie run there too, but not magpie opened
// from the Dock or a desktop menu: with the proxy app's "system proxy"
// off, Auto found none and went direct, and the user set the proxy in
// magpie by hand, then took it out again when they left the proxy off
// (jimlee0824v2 on X). Auto now takes the login shell's after the
// system's, as a terminal would. A loopback one is taken only while
// something listens on its port, so a profile that exports the proxy
// app's port for good doesn't cut magpie off while the app is closed.

// shellVar is what the login shell last said of a proxy variable
// (proc.ProxyVars), "" for none; it never waits on the shell.
var shellVar = proc.LoginEnvSaid

func shellOf(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(shellVar(k)); v != "" {
			return v
		}
	}
	return ""
}

// fromShell is fromEnv over the login shell's variables: HTTPS_PROXY for
// an https:// or wss:// request and HTTP_PROXY for any other, else
// ALL_PROXY, none for a host its NO_PROXY names; nil when there is none,
// or it is a loopback one nothing listens on.
func fromShell(u *url.URL) *url.URL {
	if noProxy(u.Hostname(), shellOf("NO_PROXY", "no_proxy")) {
		return nil
	}
	var raw string
	switch u.Scheme {
	case "https", "wss":
		raw = shellOf("HTTPS_PROXY", "https_proxy")
	default:
		raw = shellOf("HTTP_PROXY", "http_proxy")
	}
	if raw == "" {
		raw = shellOf("ALL_PROXY", "all_proxy")
	}
	return liveShell(raw)
}

// shellProxy is the login shell's proxy for a CLI magpie starts, which
// reads one for every request (HTTPS_PROXY, else HTTP_PROXY, else
// ALL_PROXY), and its NO_PROXY's hosts; "" when there is none, or it is a
// loopback one nothing listens on.
func shellProxy() (string, []string) {
	p := liveShell(shellOf("HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"))
	if p == nil {
		return "", nil
	}
	var bypass []string
	for _, h := range strings.Split(shellOf("NO_PROXY", "no_proxy"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			bypass = append(bypass, h)
		}
	}
	return p.String(), bypass
}

func liveShell(raw string) *url.URL {
	if raw == "" || strings.HasPrefix(raw, "@") { // never a saved proxy's name
		return nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	p, err := url.Parse(raw)
	if err != nil || p.Hostname() == "" || !listening(p) {
		return nil
	}
	return p
}

// How long a loopback proxy's port is taken as listening (shellUp) or
// not (shellDown) once looked at; a proxy app opened after magpie is
// taken within shellDown.
var (
	shellUp   = 15 * time.Second
	shellDown = 2 * time.Second
	dialShell = func(addr string) bool {
		c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		if err == nil {
			c.Close()
		}
		return err == nil
	}
)

var shellPorts struct {
	sync.Mutex
	seen map[string]shellPort
}

type shellPort struct {
	at time.Time
	up bool
}

// listening is whether p's port has a listener, for a loopback p; any
// other is taken as it is, as a terminal's programs take it.
func listening(p *url.URL) bool {
	if !loopback(strings.ToLower(p.Hostname())) {
		return true
	}
	port := p.Port()
	if port == "" {
		switch p.Scheme {
		case "https":
			port = "443"
		case "socks5", "socks5h", "socks4", "socks4a", "socks":
			port = "1080"
		default:
			port = "80"
		}
	}
	addr := net.JoinHostPort(p.Hostname(), port)
	shellPorts.Lock()
	s, ok := shellPorts.seen[addr]
	shellPorts.Unlock()
	keep := shellDown
	if s.up {
		keep = shellUp
	}
	if ok && time.Since(s.at) < keep {
		return s.up
	}
	up := dialShell(addr)
	shellPorts.Lock()
	if shellPorts.seen == nil {
		shellPorts.seen = map[string]shellPort{}
	}
	shellPorts.seen[addr] = shellPort{time.Now(), up}
	shellPorts.Unlock()
	return up
}

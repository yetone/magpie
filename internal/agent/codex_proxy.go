package agent

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/proc"
)

// Codex sends its requests to magpie's gateway, http://127.0.0.1:3425, the
// way it sends every other: through a proxy when one is set (Muma102kMichael
// on X: every project and every new chat 502, "a local gateway config and a
// proxy switch, two pitfalls stacked"). Its HTTP client is reqwest with its
// default features (codex-rs Cargo.toml), whose proxy is hyper-util's
// Matcher::from_system: HTTP_PROXY, else the system's proxy for http://
// (Windows' Internet Options when it names one proxy for all, macOS' "Web
// proxy (HTTP)" entry alone: never its HTTPS or SOCKS one), else ALL_PROXY,
// and nothing goes
// around it but what NO_PROXY names — or, with no NO_PROXY, Windows'
// ProxyOverride — as an IP, a CIDR or "*" for an address: "127.*" and
// "localhost" there don't keep 127.0.0.1 off it, and macOS' exceptions
// aren't read at all. A proxy that sends 127.0.0.1 on to a server of its own
// (Clash in global mode, a rule set with no LAN-direct rule) answers 502,
// and Codex is down on every model, its own included once magpie carries
// them. With [features] respect_system_proxy (codex-rs outbound_proxy),
// Codex asks the system alone: Windows' bypass list is matched with its
// wildcards and <local>, macOS' exceptions likewise, and with no proxy set
// there it goes direct; a SOCKS one alone on macOS it can't use, and takes
// HTTP_PROXY/ALL_PROXY then. reqwest is built without SOCKS (codex-rs'
// Cargo.lock has no tokio-socks), so a socks5:// proxy in ALL_PROXY is
// spoken to as an HTTP one.
//
// The environment is the one Codex runs with: a terminal's, or, for the
// ChatGPT app's Codex, the login shell's, which the app takes as it opens.
// magpie opened from the Dock has neither, so the login shell's are asked
// (proc.LoginEnv), over magpie's own.
//
// So while magpie has Codex's requests, it asks the way Codex would go:
// through which proxy, and whether a request sent through it reaches
// magpie (Drift kind "proxied").

// codexProxyEnv, codexSystemProxy and codexProxyOS are what codexProxyFor
// reads, and loginEnv what codexEnv does; vars so tests can say.
var (
	codexProxyEnv    = codexEnv
	codexSystemProxy = netproxy.System
	codexProxyOS     = runtime.GOOS
	loginEnv         = proc.LoginEnv
)

// codexEnv is k as Codex has it: the login shell's, else magpie's own.
func codexEnv(k string) string {
	if v := loginEnv(k); v != "" {
		return v
	}
	return os.Getenv(k)
}

// codexProxyFor is the proxy this machine's Codex, configured by the
// config.toml at cfg, sends a request for base through, and where that
// proxy is set ("HTTP_PROXY", "the system's proxy settings"); "" when it
// goes straight there.
func codexProxyFor(cfg, base string) (proxy, from string) {
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" {
		return "", ""
	}
	host := u.Hostname()
	// env is the first of keys set, and which one it is: the detail names
	// the variable the value is in, http_proxy or HTTP_PROXY (#2627)
	env := func(keys ...string) (string, string) {
		for _, k := range keys {
			if v := strings.TrimSpace(codexProxyEnv(k)); v != "" {
				return v, k
			}
		}
		return "", ""
	}
	httpKeys := []string{"HTTP_PROXY", "http_proxy"}
	if u.Scheme == "https" {
		httpKeys = []string{"HTTPS_PROXY", "https_proxy"}
	}
	if f, _ := edit.GetTOMLTable(cfg, "features"); f["respect_system_proxy"] == "true" {
		// the system's alone, its own bypass list deciding: with no proxy
		// set there Codex goes direct, HTTP_PROXY or not (the environment's
		// count only when the system can't be read; tried with codex-cli
		// 0.162.1)
		sys := codexSystemProxy()
		if sys.URL == "" || systemBypassed(host, sys.Bypass) {
			return "", ""
		}
		if codexProxyOS != "darwin" {
			return sys.URL, systemProxyName()
		}
		// CFNetwork's answer for an http:// URL: the HTTP entry (the HTTPS
		// one is for https:// alone); a SOCKS one Codex can't use, and
		// takes HTTP_PROXY/ALL_PROXY instead (codex-rs outbound_proxy:
		// UnsupportedProxyScheme, then resolve_env_proxy_route)
		switch {
		case sys.HTTP != "":
			return sys.HTTP, systemProxyName()
		case sys.SOCKS == "":
			return "", ""
		}
		p, k := env(httpKeys...)
		if p == "" {
			p, k = env("ALL_PROXY", "all_proxy")
		}
		if no, _ := env("NO_PROXY", "no_proxy"); p == "" || reqwestNoProxy(host, no) {
			return "", ""
		}
		return p, k
	}
	// reqwest's: each of the environment's first, the system's for
	// http:// where it names none, then ALL_PROXY; NO_PROXY, or Windows'
	// bypass list without it, goes around them
	p, k := env(httpKeys...)
	no, _ := env("NO_PROXY", "no_proxy")
	sys := codexSystemProxy()
	if p == "" && sys.HTTP != "" {
		p, k = sys.HTTP, systemProxyName()
	}
	if p == "" {
		p, k = env("ALL_PROXY", "all_proxy")
	}
	if no == "" && codexProxyOS == "windows" && sys.URL != "" {
		no = strings.ReplaceAll(strings.Join(sys.Bypass, ","), "*.", "")
	}
	if p == "" || reqwestNoProxy(host, no) {
		return "", ""
	}
	return p, k
}

func systemProxyName() string {
	if codexProxyOS == "windows" {
		return "Windows' proxy settings"
	}
	return "the system's proxy settings"
}

// reqwestNoProxy is hyper-util's NoProxy.contains: entries apart by commas;
// an address is matched only by an IP or a CIDR that holds it, a name by
// itself or a domain it is under; "*" matches every name, never an
// address.
func reqwestNoProxy(host, list string) bool {
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	for _, e := range strings.Split(list, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if ip != nil {
			if _, n, err := net.ParseCIDR(e); err == nil && n.Contains(ip) {
				return true
			}
			if x := net.ParseIP(e); x != nil && x.Equal(ip) {
				return true
			}
			continue
		}
		d := strings.TrimPrefix(e, ".")
		if e == "*" || strings.EqualFold(host, d) || strings.HasSuffix(strings.ToLower(host), "."+strings.ToLower(d)) {
			return true
		}
	}
	return false
}

// systemBypassed is Codex's own match of a host against the system's bypass
// list (codex-rs outbound_proxy/windows.rs): <local> is a name with no dot,
// an entry with * a wildcard, any other the host itself or, with a leading
// dot, a domain it is under.
func systemBypassed(host string, bypass []string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	for _, e := range bypass {
		e = strings.ToLower(strings.TrimSpace(e))
		e = strings.TrimPrefix(strings.TrimPrefix(e, "http://"), "https://")
		switch {
		case e == "":
		case e == "<local>":
			if !strings.Contains(host, ".") {
				return true
			}
		case e == "*":
			return true
		case strings.HasPrefix(e, "."):
			if host == e[1:] || strings.HasSuffix(host, e) {
				return true
			}
		case strings.Contains(e, "*"):
			if ok, _ := path.Match(e, host); ok {
				return true
			}
		case host == e:
			return true
		}
	}
	return false
}

// proxyProbe says whether a request for base sent through proxy is answered
// by magpie: its gateway's GET / names it. A proxy's own 502, or anything
// else that isn't magpie, is not. A socks proxy is asked as Codex asks it:
// as an HTTP one at the same address, since its reqwest has no SOCKS. A var
// for tests.
var proxyProbe = realProxyProbe

func realProxyProbe(base, proxy string) bool {
	p, err := netproxy.Parse(proxy)
	if err != nil {
		return false
	}
	if strings.HasPrefix(strings.ToLower(p.Scheme), "socks") {
		p.Scheme = "http"
	}
	c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(p)}}
	res, err := c.Get(strings.TrimSuffix(base, "/") + "/")
	if err != nil {
		return false
	}
	return answersAsMagpie(res)
}

// codexThroughProxy says whether Codex, configured by cfg, gets to the
// gateway at base: true when it goes there directly, or through a proxy
// that is known to pass its requests on. known is false while that proxy
// hasn't been tried yet; a try is started then (see reachable).
func codexThroughProxy(cfg, base string) (ok, known bool, proxy, from string) {
	proxy, from = codexProxyFor(cfg, base)
	if proxy == "" {
		return true, true, "", ""
	}
	ok, known = probed("proxy "+proxy+" "+base, func() bool { return proxyProbe(base, proxy) })
	return ok, known, proxy, from
}

// codexProxiedDetail says what keeps Codex off magpie through proxy, and
// how to get it there.
func codexProxiedDetail(base, proxy, from string) string {
	host := strings.TrimPrefix(base, "http://")
	d := "Codex sends its requests for magpie (" + host + ") through the proxy at " + netproxy.Redact(proxy) + ", set in " + from + ", and that proxy doesn't pass them on to magpie: every turn fails, with 502 Bad Gateway, whichever model it is on. " +
		"Have the proxy send 127.0.0.1 direct (a DIRECT rule for 127.0.0.0/8, or rule mode rather than global), "
	switch {
	case strings.Contains(strings.ToUpper(from), "_PROXY"):
		d += "or add 127.0.0.1 to NO_PROXY where " + from + " is set (your shell profile, such as ~/.zshrc, if it's there), "
	case codexProxyOS == "windows":
		d += "or add 127.0.0.1 to \"Don't use the proxy server for addresses beginning with\" in Windows' proxy settings (127.* there is not enough for Codex), "
	default:
		d += "or add export NO_PROXY=127.0.0.1 to your shell profile (~/.zshrc): Codex in a terminal and the ChatGPT app's Codex both read it, "
	}
	return d + "then quit and reopen Codex. Until then, Disconnect from magpie puts Codex's config back as it was."
}

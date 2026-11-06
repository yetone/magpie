// Package netproxy decides which proxy magpie's own requests to vendors go
// through. An app started from the Dock or the Start menu has no
// HTTPS_PROXY in its environment, so without this every call to a vendor
// that is only reachable through a proxy hangs until the agent gives up.
//
// In order: the proxy set in magpie's settings (or "direct" for none), the
// usual *_PROXY variables, then the system's proxy (macOS network
// settings, Windows Internet Options). Loopback is never proxied, nor,
// unless a provider has a proxy of its own, a host on the user's own
// network (lan.go).
package netproxy

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/probelog"
	"github.com/yetone/magpie/internal/settings"
)

// Install routes http.DefaultTransport (and so http.DefaultClient) through
// Func, and http.DefaultClient's requests made on a provider's behalf
// (With) through a transport of that provider's proxy (Dispatch). Its
// HTTP/2 connections are checked as the gateway's are (HTTP2Health), and
// the requests magpie sends on its own are written down (probelog).
func Install() {
	if t, ok := http.DefaultTransport.(*http.Transport); ok {
		t.Proxy = Func
		if t.HTTP2 == nil {
			t.HTTP2 = HTTP2Health()
		}
		// a request magpie sends a provider on its own (a Test, a model
		// list) is written into the request log as it goes (probelog)
		http.DefaultClient.Transport = probelog.Transport(Dispatch(t))
	}
}

// PingAfter and PingWait: an HTTP/2 connection that has had no frame for
// PingAfter is pinged, and dropped when no answer comes within PingWait.
// The health check is the only probe of a connection whose path has died
// silently, so a turn in flight on it waits for the next one: PingAfter is
// how long a vendor's silence before a turn's first byte can cost (#4936,
// a first try read EOF 20.5s in on a connection whose tunnel a NAT or a
// relay had dropped). The budget PingAfter+PingWait is also the margin a
// body under uploadBytes needs on a slow uplink, a ping queued behind it
// answered only after the last byte (#3909): 20s covers 64KB at about
// 3KB/s.
var PingAfter, PingWait = 10 * time.Second, 10 * time.Second

// HTTP2Health is the HTTP/2 setting of a transport to the vendors. Every
// request to one host shares one HTTP/2 connection, and one gone silent
// without closing (a proxy's way out died, a NAT forgot the flow) was
// given every new request, each waiting for its transport's
// ResponseHeaderTimeout while the kernel took the writes, until a restart
// (#2843). A ping finds it, and the requests on it fail, so a group moves
// on and the next request dials anew. A live vendor answers a ping
// whatever its streams are doing, so a long wait for a first byte is
// never cut by it.
func HTTP2Health() *http.HTTP2Config {
	return &http.HTTP2Config{SendPingTimeout: PingAfter, PingTimeout: PingWait}
}

// Func is an http.Transport Proxy function: the proxy the request's
// context names (With), or else the global one, which a host on the
// user's own network goes around (onLAN).
func Func(req *http.Request) (*url.URL, error) {
	if err := Held(req.Context()); err != nil {
		return nil, err
	}
	if loopback(req.URL.Hostname()) {
		return nil, nil
	}
	if c := choiceOf(req.Context()); c != "" {
		return forChoice(c)
	}
	p, err := For(req.URL)
	if p != nil && onLAN(req.URL.Hostname()) {
		return nil, nil
	}
	return p, err
}

type choiceKey struct{}

type holdKey struct{}

// Hold is ctx for work magpie was not asked to do and must not send: no
// request made with it through Func, or a transport Dispatch made, leaves
// the machine; each fails with err before it dials (#1518, allowance
// reads while the user has them read only when asked).
func Hold(ctx context.Context, err error) context.Context {
	return context.WithValue(ctx, holdKey{}, err)
}

// Held is the error ctx holds its requests with (Hold), nil when it holds
// none.
func Held(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	err, _ := ctx.Value(holdKey{}).(error)
	return err
}

// With is ctx for requests made on behalf of one provider, whose own proxy
// is choice (issue #237: Codex through a proxy, a vendor at home without):
// "" follows the global one, "direct" goes through none, anything else is
// the proxy's address (http://, https://, socks5://; host:port means
// http). Requests made with it through Func, or through a transport
// Dispatch made, take that proxy. A choice ctx already names is replaced,
// "" too: a request magpie makes for one provider while serving another's
// (a web search asked of Codex in a turn served by a provider set to
// "direct", #1522) goes through its own provider's proxy, never the one
// it was made under. A provider's leave not to check its certificate
// (WithUncheckedTLS) goes the same way: it is the outer provider's, and
// dropped here, and so is its ask for HTTP/1.1 (WithHTTP1).
func With(ctx context.Context, choice string) context.Context {
	if choice = strings.TrimSpace(choice); choice == "" && choiceOf(ctx) == "" && uncheckedOf(ctx) == nil && http1Of(ctx) == nil {
		return ctx
	}
	ctx = context.WithValue(ctx, choiceKey{}, choice)
	if uncheckedOf(ctx) != nil {
		ctx = context.WithValue(ctx, uncheckedKey{}, map[string]bool(nil))
	}
	if http1Of(ctx) != nil {
		ctx = context.WithValue(ctx, http1Key{}, map[string]bool(nil))
	}
	return ctx
}

// Choice is the proxy ctx names (With), "" when it names none — for a CLI
// run on a provider's behalf, whose *_PROXY is set from it (EnvWith).
func Choice(ctx context.Context) string { return choiceOf(ctx) }

func choiceOf(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	c, _ := ctx.Value(choiceKey{}).(string)
	return c
}

// forChoice is the proxy a provider's own choice names; nil for "direct".
func forChoice(c string) (*url.URL, error) {
	if c == "direct" {
		return nil, nil
	}
	return Parse(c)
}

// HTTP1 says a request to u goes over HTTP/1.1 only, whatever its
// transport offers. internal/provider sets it to the ChatGPT backend's
// host, which Codex itself speaks HTTP/1.1 to; nil leaves every request to
// its transport.
var HTTP1 func(u *url.URL) bool

// Dispatch is a RoundTripper sending a request through base, unless its
// context names a provider's own proxy (With): then through a clone of
// base kept for that proxy alone. Connections are so reused among the
// requests going through one proxy and never carried to another — HTTP/2
// ones, which a transport shares by host whatever proxy they were dialled
// through, among them. A move of the global proxy (the settings) drops
// the idle connections the old one dialled, which a transport, pooling
// by target host alone, would have carried over the switch (#4523). A
// request HTTP1 names, one to a host its provider asked HTTP/1.1 of
// (WithHTTP1), and one to a host whose HTTP/2 connection carried a TLS
// record that didn't decrypt (recordError, for h2Broken after) go
// through a clone kept for HTTP/1.1 (http1Only), and one to a host whose certificate the provider's user
// said not to check (WithUncheckedTLS) through a clone kept for that
// host alone (uncheckTLS). A host whose IPv6 is taken but goes nowhere
// is dialed over IPv4 (v4Fallback).
func Dispatch(base *http.Transport) http.RoundTripper {
	v4Fallback(base)
	return &dispatch{base: base, own: map[route]*http.Transport{}, moved: Watch()}
}

type dispatch struct {
	base *http.Transport
	mu   sync.Mutex
	own  map[route]*http.Transport
	// moved is whether the global proxy moved since the dispatch's last
	// request: a move drops the idle connections, which would carry the
	// requests after a switch through the old proxy (#4523).
	moved func() bool
}

// route is what a request needs of its transport other than base: a
// provider's own proxy, HTTP/1.1 only, the host:port whose certificate is
// not checked, or any of them together.
type route struct {
	choice    string
	h1        bool
	unchecked string
}

func (d *dispatch) RoundTrip(req *http.Request) (*http.Response, error) {
	if d.moved() {
		d.CloseIdleConnections()
	}
	r := route{choice: choiceOf(req.Context()), h1: HTTP1 != nil && HTTP1(req.URL) || http1Asked(req) || h2BrokenFor(req), unchecked: uncheckedHost(req)}
	t := d.base
	if r != (route{}) {
		t = d.transport(r)
	}
	res, err := v4Retry(t, req)
	if r.h1 {
		return res, err
	}
	if err != nil {
		if recordError(err) {
			markH2Broken(req)
		}
		return res, err
	}
	if res.ProtoMajor == 2 && res.Body != nil && res.Body != http.NoBody {
		res.Body = h2Watch{ReadCloser: res.Body, req: req}
	}
	return res, err
}

// Watch is a check a transport of its own makes before each request: it
// says the global proxy in magpie's settings moved since the check's last
// call, and the caller closes its transport's idle connections, which
// would carry the request after a switch through the old proxy. The
// setting is read at dial time (Func), but a transport pools by target
// host, and its HTTP/2 connections by the authority alone, so a
// connection dialled through the old proxy carries the requests after a
// switch too, until it is dropped. Dropping the idle connections sends
// the request after it through the proxy in force; one a request is
// still reading is left to finish. Each check keeps its own last value,
// so each transport sees, and answers, a move on the first request it
// sends after it (#4523, #4824). The first call sees no move.
func Watch() func() bool {
	var mu sync.Mutex
	var proxy string
	var seen bool
	return func() bool {
		// Shared, not Load: this runs on every request
		now := strings.TrimSpace(settings.Shared().Proxy)
		mu.Lock()
		defer mu.Unlock()
		moved := seen && proxy != now
		proxy, seen = now, true
		return moved
	}
}

func (d *dispatch) transport(r route) *http.Transport {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.own[r]; ok {
		return t
	}
	if len(d.own) >= 32 { // choices come and go as they are edited
		for k, t := range d.own {
			t.CloseIdleConnections()
			delete(d.own, k)
		}
	}
	t := d.base.Clone()
	if c := r.choice; c != "" {
		t.Proxy = func(req *http.Request) (*url.URL, error) {
			if err := Held(req.Context()); err != nil {
				return nil, err
			}
			if loopback(req.URL.Hostname()) {
				return nil, nil
			}
			return forChoice(c)
		}
	}
	if r.h1 {
		http1Only(t)
	}
	if r.unchecked != "" {
		uncheckTLS(t)
	}
	d.own[r] = t
	return t
}

// http1Only has t, a clone, speak HTTP/1.1 only, and its TLS hello offer
// no protocol (ALPN), as Codex's does. A transport that has spoken HTTP/2
// carries h2 in its TLS config, which a clone copies, and a server that
// took it would be sent HTTP/1.1 on an HTTP/2 connection.
func http1Only(t *http.Transport) {
	var h1 http.Protocols
	h1.SetHTTP1(true)
	t.Protocols, t.ForceAttemptHTTP2 = &h1, false
	if t.TLSClientConfig != nil {
		t.TLSClientConfig.NextProtos = nil
	}
}

// CloseIdleConnections closes base's and every proxy's own.
func (d *dispatch) CloseIdleConnections() {
	d.base.CloseIdleConnections()
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, t := range d.own {
		t.CloseIdleConnections()
	}
}

// For is the proxy a request to u goes through; nil means direct.
func For(u *url.URL) (*url.URL, error) {
	switch s := strings.TrimSpace(settings.Load().Proxy); s {
	case "direct":
		return nil, nil
	case "":
	default:
		return Parse(s)
	}
	if p, err := fromEnv(u); p != nil || err != nil {
		return p, err
	}
	sys := System()
	if sys.URL == "" || bypassed(u.Hostname(), sys.Bypass) {
		return nil, nil
	}
	return Parse(sys.URL)
}

// Env is env (the process's own when nil) for an agent's CLI that magpie
// starts — a sign-in, or a subscription's run — told to use the proxy
// magpie's own requests would: the CLI reads only *_PROXY, which an app
// started from the Dock or the Start menu doesn't have, so a proxy set in
// magpie or the system's alone left it trying the vendor directly. A
// proxy set in magpie replaces any in env and "direct" drops them; the
// system's is only added where env names none. Loopback never goes
// through it, since a CLI calls back to magpie there.
func Env(env []string) []string { return EnvWith("", env) }

// EnvWith is Env for a CLI run on one provider's behalf, whose own proxy
// is choice (see With): "" is Env's.
func EnvWith(choice string, env []string) []string {
	if env == nil {
		env = os.Environ()
	}
	var p string
	var bypass []string
	s := strings.TrimSpace(choice)
	if s == "" {
		s = strings.TrimSpace(settings.Load().Proxy)
	}
	switch s {
	case "direct":
		return withProxy(env, "", nil)
	case "":
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			if proxyVar(k) && !strings.EqualFold(k, "NO_PROXY") && strings.TrimSpace(v) != "" {
				return env
			}
		}
		sys := System()
		if sys.URL == "" {
			return env
		}
		p, bypass = sys.URL, sys.Bypass
	default:
		u, err := Parse(s)
		if err != nil {
			return env
		}
		p = u.String()
	}
	return withProxy(env, p, bypass)
}

func proxyVar(k string) bool {
	switch strings.ToUpper(k) {
	case "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY":
		return true
	}
	return false
}

// withProxy is env with its *_PROXY replaced by p ("" for none), and
// NO_PROXY naming loopback and bypass.
func withProxy(env []string, p string, bypass []string) []string {
	out := make([]string, 0, len(env)+8)
	for _, e := range env {
		if k, _, _ := strings.Cut(e, "="); !proxyVar(k) {
			out = append(out, e)
		}
	}
	if p == "" {
		return out
	}
	no := []string{"localhost", "127.0.0.1", "::1"}
	for _, b := range bypass {
		switch b = strings.TrimPrefix(strings.TrimSpace(b), "*"); b {
		case "", "<local>":
		default:
			no = append(no, b)
		}
	}
	nos := strings.Join(no, ",")
	return append(out, "HTTPS_PROXY="+p, "https_proxy="+p, "HTTP_PROXY="+p, "http_proxy="+p,
		"ALL_PROXY="+p, "all_proxy="+p, "NO_PROXY="+nos, "no_proxy="+nos)
}

// Parse reads a proxy address; a bare host:port is an HTTP proxy.
func Parse(s string) (*url.URL, error) {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return url.Parse(s)
}

// Describe says, for the settings page, what a request to u would use.
func Describe() (proxy, source string) {
	defer func() { proxy = Redact(proxy) }()
	switch s := strings.TrimSpace(settings.Load().Proxy); s {
	case "direct":
		return "", "off"
	case "":
	default:
		return s, "settings"
	}
	u, _ := url.Parse("https://chatgpt.com")
	if p, _ := fromEnv(u); p != nil {
		return p.String(), "environment"
	}
	if sys := System(); sys.URL != "" {
		return sys.URL, "system"
	}
	return "", "none"
}

// Redact is a proxy address as magpie may show it: in a log line, an
// error, a status or the GUI (#852, #1886). Its user and password are
// left out, and the rest kept as it was written (no scheme added to one
// written without). A malformed address is "", because its userinfo
// cannot be safely separated. Requests still go through the address
// itself; only what is shown of it goes through here.
func Redact(raw string) string {
	u, err := Parse(raw)
	if err != nil {
		return ""
	}
	if u.User == nil {
		return raw
	}
	u.User = nil
	address := u.String()
	if !strings.Contains(raw, "://") {
		address = strings.TrimPrefix(address, "http://")
	}
	return address
}

// fromEnv is the proxy the environment names for u: HTTPS_PROXY or
// HTTP_PROXY as Go reads them, else ALL_PROXY (curl's, which Go doesn't
// read: a shell with only ALL_PROXY=socks5://… set has curl go through it,
// and magpie, its updates among its requests, too). NO_PROXY is kept for
// it as well.
func fromEnv(u *url.URL) (*url.URL, error) {
	if p, err := http.ProxyFromEnvironment(&http.Request{URL: u}); p != nil || err != nil {
		return p, err
	}
	all := envOf("ALL_PROXY", "all_proxy")
	if all == "" || noProxy(u.Hostname(), envOf("NO_PROXY", "no_proxy")) {
		return nil, nil
	}
	return Parse(all)
}

func envOf(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// noProxy matches host against NO_PROXY as curl does: "*" for every host,
// else entries apart by commas, each the host or a domain it is under
// (example.com and .example.com both take api.example.com); a port on an
// entry is ignored. Loopback is never proxied anyway.
func noProxy(host, list string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || loopback(host) {
		return true
	}
	for _, e := range strings.Split(list, ",") {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "*" {
			return true
		}
		if h, _, err := net.SplitHostPort(e); err == nil {
			e = h
		}
		e = strings.TrimPrefix(strings.TrimPrefix(e, "*"), ".")
		if e != "" && (host == e || strings.HasSuffix(host, "."+e)) {
			return true
		}
	}
	return false
}

// Proxy is the system's proxy: one address, and the hosts that skip it.
// HTTP and SOCKS are the system's entries for plain http:// requests and
// for SOCKS, each alone, for a client that reads them so (Codex's reqwest
// takes macOS' HTTP entry and nothing else for an http:// URL).
type Proxy struct {
	URL    string
	HTTP   string
	SOCKS  string
	Bypass []string
}

var sysCache struct {
	sync.Mutex
	at     time.Time // when the last read ended; zero: nothing read yet
	p      Proxy
	failed bool // the last read failed, so p is from one before it
	// reading is closed when the read under way, begun at since, ends;
	// nil while none is
	reading chan struct{}
	since   time.Time
}

// How long a system proxy read stands: a proxy that is set, 15 seconds;
// none, 2. magpie started at login comes up before the proxy app does
// (#1518), and its requests went direct for up to 15 seconds after the
// proxy app had set the system's proxy. A request waits for a read until
// readWait after the read began, then takes what was read before: a
// scutil that never answered held every request that asked for the
// system's proxy for as long as it hung (#3421).
var (
	systemKeep = 15 * time.Second
	noneKeep   = 2 * time.Second
	readWait   = 2 * time.Second
	readSystem = system
)

// System reads the system proxy, at most every 15 seconds while one is
// set and every 2 while none is. One read runs at a time, outside the
// lock. A read that fails keeps what was read before, and the one after
// it holds nobody.
func System() Proxy {
	sysCache.Lock()
	keep := systemKeep
	if sysCache.p.URL == "" {
		keep = noneKeep
	}
	if !sysCache.at.IsZero() && time.Since(sysCache.at) <= keep {
		p := sysCache.p
		sysCache.Unlock()
		return p
	}
	if sysCache.reading == nil {
		sysCache.reading, sysCache.since = make(chan struct{}), time.Now()
		go refreshSystem(sysCache.reading)
	}
	var wait time.Duration
	if sysCache.at.IsZero() || !sysCache.failed {
		wait = time.Until(sysCache.since.Add(readWait))
	}
	done := sysCache.reading
	sysCache.Unlock()
	if wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-done:
		case <-t.C:
		}
		t.Stop()
	}
	sysCache.Lock()
	defer sysCache.Unlock()
	return sysCache.p
}

// refreshSystem reads the system proxy into sysCache, then closes done.
// A read that fails keeps what was read before, if anything was.
func refreshSystem(done chan struct{}) {
	p, ok := readSystem()
	sysCache.Lock()
	if ok || sysCache.at.IsZero() {
		sysCache.p = p
	}
	sysCache.at, sysCache.failed, sysCache.reading = time.Now(), !ok, nil
	sysCache.Unlock()
	close(done)
}

func loopback(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// bypassed matches the system's exception list: exact hosts, "*.example.com"
// or ".example.com" suffixes, and "<local>" for dotless names.
func bypassed(host string, list []string) bool {
	host = strings.ToLower(host)
	for _, b := range list {
		b = strings.ToLower(strings.TrimSpace(b))
		switch {
		case b == "":
		case b == "<local>":
			if !strings.Contains(host, ".") {
				return true
			}
		case strings.HasPrefix(b, "*."), strings.HasPrefix(b, "."):
			suf := strings.TrimPrefix(b, "*")
			if strings.HasSuffix(host, suf) || host == suf[1:] {
				return true
			}
		case host == b:
			return true
		}
	}
	return false
}

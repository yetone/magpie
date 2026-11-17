package gui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Named proxies (the Network page's list): kept once, chosen by reference
// wherever a proxy can be chosen. The page is sent the address without its
// login, the username and whether a password is set; the password is
// written and never read back, as the GitHub token is.

// savedProxyJSON is a saved proxy as the page sees it.
type savedProxyJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	User    string `json:"user,omitempty"`
	HasPass bool   `json:"hasPass,omitempty"`
	// an SSH server's: whether a private key is kept, and the SHA256
	// fingerprint of the host key trusted for it
	HasKey  bool   `json:"hasKey,omitempty"`
	HostKey string `json:"hostKey,omitempty"`
	// Uses is where it is chosen: Settings' own two, then providers,
	// accounts and keys
	Uses  []proxyUseJSON `json:"uses,omitempty"`
	Check *proxyTestJSON `json:"check,omitempty"`
}

type proxyUseJSON struct {
	Where    string `json:"where,omitempty"` // "global", "plugins" or "updates"; else a provider
	Provider string `json:"provider,omitempty"`
	Account  string `json:"account,omitempty"`
}

// proxyTestJSON is one connectivity check: whether a request got through
// the proxy, how long it took, and where it left from. A failure magpie
// can explain has its reason in Why (netproxy.Why: a sentence the page
// translates, its slots filled from Addr, Was and Now) and Go's words in
// Error.
type proxyTestJSON struct {
	OK    bool   `json:"ok"`
	Ms    int64  `json:"ms,omitempty"`
	IP    string `json:"ip,omitempty"`
	Loc   string `json:"loc,omitempty"`
	Error string `json:"error,omitempty"`
	Why   string `json:"why,omitempty"`
	Addr  string `json:"addr,omitempty"`
	Was   string `json:"was,omitempty"`
	Now   string `json:"now,omitempty"`
	At    int64  `json:"at"`
}

// failed is a check that failed with err, its reason in Why when it is
// one magpie can say.
func (o proxyTestJSON) failed(err error) proxyTestJSON {
	o.OK = false
	var w *netproxy.Why
	if errors.As(err, &w) {
		o.Why, o.Addr, o.Was, o.Now = w.Why, w.Addr, w.Was, w.Now
		o.Error = ""
		if w.Err != nil {
			o.Error = redactProxyErr(w.Err.Error())
		}
		return o
	}
	o.Error = redactProxyErr(err.Error())
	return o
}

// proxyTestClient makes the check's request; a test swaps its own.
var proxyTestClient = http.DefaultClient

var (
	proxyTestsMu sync.Mutex
	proxyTests   = map[string]proxyTestJSON{}
)

func savedProxies() []savedProxyJSON {
	cur := settings.Load()
	var out []savedProxyJSON
	proxyTestsMu.Lock()
	defer proxyTestsMu.Unlock()
	for _, p := range cur.Proxies {
		ref := settings.ProxyRef(p.ID)
		row := savedProxyJSON{ID: p.ID, Name: p.Name, URL: p.URL, User: p.User, HasPass: p.Pass != "", HasKey: p.Key != ""}
		if p.HostKey != "" {
			row.HostKey = netproxy.HostKeyFingerprint(p.HostKey)
		}
		if strings.TrimSpace(cur.Proxy) == ref {
			row.Uses = append(row.Uses, proxyUseJSON{Where: "global"})
		}
		if strings.TrimSpace(cur.PluginProxy) == ref {
			row.Uses = append(row.Uses, proxyUseJSON{Where: "plugins"})
		}
		if strings.TrimSpace(cur.UpdateProxy) == ref {
			row.Uses = append(row.Uses, proxyUseJSON{Where: "updates"})
		}
		for _, u := range provider.ProxyUses(p.ID) {
			row.Uses = append(row.Uses, proxyUseJSON{Provider: u.Provider, Account: u.Account})
		}
		if c, ok := proxyTests[p.ID]; ok {
			c := c
			row.Check = &c
		}
		out = append(out, row)
	}
	return out
}

// proxyIn is what the page sends to save or test a proxy. A nil Pass or
// Key keeps the saved one (an edit that left it blank). Trust drops the
// SSH host key trusted for it, so the one the server presents now is
// trusted instead.
type proxyIn struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	URL   string  `json:"url"`
	User  string  `json:"user"`
	Pass  *string `json:"pass"`
	Key   *string `json:"key"`
	Trust bool    `json:"trustNewKey"`
}

func (in proxyIn) named(cur []settings.NamedProxy) settings.NamedProxy {
	p := settings.NamedProxy{ID: strings.TrimSpace(in.ID), Name: in.Name, URL: strings.TrimSpace(in.URL), User: strings.TrimSpace(in.User)}
	var old settings.NamedProxy
	for _, c := range cur {
		if c.ID == p.ID && p.ID != "" {
			old = c
		}
	}
	if in.Pass != nil {
		p.Pass = *in.Pass
	} else {
		p.Pass = old.Pass
	}
	if in.Key != nil {
		p.Key = strings.TrimSpace(*in.Key)
		if p.Key != "" {
			p.Key += "\n" // a PEM's last line ends in one
		}
	} else {
		p.Key = old.Key
	}
	// a host key is the server's: it goes with the address it was
	// trusted at
	if !in.Trust && old.URL == p.URL {
		p.HostKey = old.HostKey
	}
	return p
}

// sshLogin is whether saving p changes how its SSH server is logged in
// to, from old: then the login is tried before the save.
func sshLogin(p, old settings.NamedProxy) bool {
	return p.URL != old.URL || p.User != old.User || p.Pass != old.Pass || p.Key != old.Key || p.HostKey == ""
}

func handleProxySave(rw http.ResponseWriter, r *http.Request) {
	var in proxyIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(rw, err)
		return
	}
	s := settings.Load()
	p := in.named(s.Proxies)
	found := false
	var old settings.NamedProxy
	for _, c := range s.Proxies {
		if c.ID == p.ID && p.ID != "" {
			old = c
		}
	}
	if p.IsSSH() {
		if err := settings.CheckSSHProxy(p); err != nil {
			fail(rw, err)
			return
		}
		if sshLogin(p, old) {
			// logged in to once before it is kept: a refused login says
			// why here, and the server's key is trusted from now on
			key, err := netproxy.CheckSSH(r.Context(), p)
			if err != nil {
				fail(rw, err)
				return
			}
			p.HostKey = key
		}
	}
	if p.ID == "" {
		p.ID = settings.NewProxyID()
	}
	for i := range s.Proxies {
		if s.Proxies[i].ID == p.ID {
			s.Proxies[i], found = p, true
		}
	}
	if !found {
		s.Proxies = append(s.Proxies, p)
	}
	if err := settings.Save(s); err != nil {
		fail(rw, err)
		return
	}
	netproxy.CloseSSH(p.ID) // its tunnel logs in again, as saved
	provider.ProxyEdited()  // Claude Code's env holds an address, not the name
	// what it was checked as no longer says anything about it
	proxyTestsMu.Lock()
	delete(proxyTests, p.ID)
	proxyTestsMu.Unlock()
	rw.Header().Set("X-Magpie-Proxy", p.ID) // the page tests what it saved
	writeJSON(rw, settingsState())
}

func handleProxyDelete(rw http.ResponseWriter, r *http.Request) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(rw, err)
		return
	}
	s := settings.Load()
	var keep []settings.NamedProxy
	for _, p := range s.Proxies {
		if p.ID != in.ID {
			keep = append(keep, p)
		}
	}
	if len(keep) == len(s.Proxies) {
		writeJSON(rw, settingsState())
		return
	}
	for _, row := range savedProxies() {
		if row.ID == in.ID && len(row.Uses) > 0 {
			rw.Header().Set("Content-Type", "application/json")
			rw.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(rw).Encode(map[string]any{"error": "the proxy is in use", "uses": row.Uses})
			return
		}
	}
	s.Proxies = keep
	if err := settings.Save(s); err != nil {
		fail(rw, err)
		return
	}
	netproxy.CloseSSH(in.ID)
	proxyTestsMu.Lock()
	delete(proxyTests, in.ID)
	proxyTestsMu.Unlock()
	writeJSON(rw, settingsState())
}

// handleProxyTest asks the trace page through one saved proxy (by id), or
// through one being typed (its address and login, unsaved).
func handleProxyTest(rw http.ResponseWriter, r *http.Request) {
	var in proxyIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(rw, err)
		return
	}
	cur := settings.Load().Proxies
	var p settings.NamedProxy
	if strings.TrimSpace(in.ID) != "" {
		for _, c := range cur {
			if c.ID == strings.TrimSpace(in.ID) {
				p = c
			}
		}
		if p.ID == "" {
			fail(rw, fmt.Errorf("the saved proxy isn't there"))
			return
		}
		if strings.TrimSpace(in.URL) != "" { // an edit not yet saved
			p = in.named(cur)
		}
	} else {
		p = in.named(cur)
		p.Name = "test"
	}
	saved := strings.TrimSpace(in.ID) != "" && strings.TrimSpace(in.URL) == ""
	var out proxyTestJSON
	if p.IsSSH() {
		out = testSSH(r.Context(), p, saved)
	} else {
		addr, err := p.Address()
		if err != nil {
			fail(rw, err)
			return
		}
		// a typed address is checked as saving would
		if err := settings.CheckProxy(addr); err != nil {
			fail(rw, err)
			return
		}
		out = testProxy(r.Context(), addr)
	}
	if in.ID != "" && strings.TrimSpace(in.URL) == "" {
		proxyTestsMu.Lock()
		proxyTests[p.ID] = out
		proxyTestsMu.Unlock()
	}
	writeJSON(rw, out)
}

// testSSH is the check of an SSH proxy: a login first, which says why it
// failed where a request through the tunnel would say only that SOCKS
// did, then the request. A saved one is checked through the tunnel its
// requests take; one being typed through a tunnel of its own, closed
// after.
func testSSH(ctx context.Context, p settings.NamedProxy, saved bool) proxyTestJSON {
	out := proxyTestJSON{At: time.Now().Unix()}
	if saved {
		if err := netproxy.SSHReady(ctx, p); err != nil {
			return out.failed(err)
		}
		addr, err := p.Address()
		if err != nil {
			return out.failed(err)
		}
		return sshTested(p, testProxy(ctx, addr))
	}
	key, err := netproxy.CheckSSH(ctx, p)
	if err != nil {
		return out.failed(err)
	}
	p.HostKey = key
	addr, done, err := netproxy.OpenSSH(p)
	if err != nil {
		return out.failed(err)
	}
	defer done()
	return sshTested(p, testProxy(ctx, addr))
}

// sshTested says a failed check through an SSH tunnel about the SSH
// server: the tunnel's own local address is nothing the user can check.
func sshTested(p settings.NamedProxy, out proxyTestJSON) proxyTestJSON {
	if out.OK || out.Error == "" {
		return out
	}
	out.Why, out.Addr, out.Was, out.Now = "", "", "", ""
	if w := netproxy.SSHForwardWhy(p, out.Error); w != nil {
		out.Why, out.Addr = w.Why, w.Addr
	}
	return out
}

// testProxy asks the trace page through addr. Its Ms is the latency
// through the proxy: from the request written to the answer's first
// byte, one round trip to the trace page and back through the proxy.
// The whole request's time would count a new connection's dial, tunnel
// and TLS on a first Test and not on the next one, which reuses it.
func testProxy(ctx context.Context, addr string) proxyTestJSON {
	// the transport calls these from its own goroutines
	start := time.Now()
	var wrote, first atomic.Int64
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest:         func(httptrace.WroteRequestInfo) { wrote.Store(int64(time.Since(start))) },
		GotFirstResponseByte: func() { first.Store(int64(time.Since(start))) },
	})
	leg := throughLeg(netproxy.With(ctx, addr), proxyTestClient)
	out := proxyTestJSON{Ms: leg.Ms, IP: leg.IP, Loc: leg.Loc, At: time.Now().Unix()}
	if w, f := wrote.Load(), first.Load(); w > 0 && f > w {
		out.Ms = max(time.Duration(f-w).Milliseconds(), 1)
	}
	switch {
	case leg.Error != "":
		out.Error = redactProxyErr(leg.Error)
		if w := netproxy.ExplainServer(ctx, addr, out.Error); w != nil {
			out.Why, out.Addr = w.Why, w.Addr
		}
	case !leg.Proxied:
		out.Error = "the request didn't go to the proxy"
	default:
		out.OK = true
	}
	return out
}

func proxyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/proxies/save", handleProxySave)
	mux.HandleFunc("POST /api/proxies/delete", handleProxyDelete)
	mux.HandleFunc("POST /api/proxies/test", handleProxyTest)
}

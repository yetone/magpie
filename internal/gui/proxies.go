package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
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
// the proxy, how long it took, and where it left from.
type proxyTestJSON struct {
	OK    bool   `json:"ok"`
	Ms    int64  `json:"ms,omitempty"`
	IP    string `json:"ip,omitempty"`
	Loc   string `json:"loc,omitempty"`
	Error string `json:"error,omitempty"`
	At    int64  `json:"at"`
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
		row := savedProxyJSON{ID: p.ID, Name: p.Name, URL: p.URL, User: p.User, HasPass: p.Pass != ""}
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

// proxyIn is what the page sends to save or test a proxy. A nil Pass keeps
// the saved password (an edit that left it blank).
type proxyIn struct {
	ID   string  `json:"id"`
	Name string  `json:"name"`
	URL  string  `json:"url"`
	User string  `json:"user"`
	Pass *string `json:"pass"`
}

func (in proxyIn) named(cur []settings.NamedProxy) settings.NamedProxy {
	p := settings.NamedProxy{ID: strings.TrimSpace(in.ID), Name: in.Name, URL: in.URL, User: strings.TrimSpace(in.User)}
	if in.Pass != nil {
		p.Pass = *in.Pass
	} else {
		for _, c := range cur {
			if c.ID == p.ID {
				p.Pass = c.Pass
			}
		}
	}
	return p
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
	if p.ID == "" {
		p.ID = settings.NewProxyID()
		if in.Pass == nil {
			p.Pass = ""
		}
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
	provider.ProxyEdited() // Claude Code's env holds an address, not the name
	// what it was checked as no longer says anything about it
	proxyTestsMu.Lock()
	delete(proxyTests, p.ID)
	proxyTestsMu.Unlock()
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
	out := testProxy(r.Context(), addr)
	if in.ID != "" && strings.TrimSpace(in.URL) == "" {
		proxyTestsMu.Lock()
		proxyTests[p.ID] = out
		proxyTestsMu.Unlock()
	}
	writeJSON(rw, out)
}

func testProxy(ctx context.Context, addr string) proxyTestJSON {
	leg := throughLeg(netproxy.With(ctx, addr), proxyTestClient)
	out := proxyTestJSON{Ms: leg.Ms, IP: leg.IP, Loc: leg.Loc, At: time.Now().Unix()}
	switch {
	case leg.Error != "":
		out.Error = redactProxyErr(leg.Error)
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

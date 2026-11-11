package provider

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
)

// Via is ctx for requests made on the provider's behalf: they go through
// its own proxy (ProxyChoice), or the global one when it has none. A
// provider the user added and asked not to have its certificate checked
// (InsecureTLS) has its own hosts' taken as they are, no other host's;
// one asked to be spoken HTTP/1.1 to (HTTP1) is, at its own hosts alone;
// one whose turns go over the Responses WebSocket (Socket) has them go
// there, at its Responses URL alone (SocketTurn).
func (p Provider) Via(ctx context.Context) context.Context {
	ctx = netproxy.With(ctx, p.ProxyChoice())
	if p.InsecureTLS && p.TakesInsecureTLS() {
		ctx = netproxy.WithUncheckedTLS(ctx, p.UncheckedHosts()...)
	}
	if p.HTTP1 && p.TakesInsecureTLS() {
		ctx = netproxy.WithHTTP1(ctx, p.UncheckedHosts()...)
	}
	if (p.ClientCert != "" || p.ServerCA != "") && p.TakesClientCert() {
		ctx = netproxy.WithClientCert(ctx, p.ClientCert, p.ClientKey, p.ServerCA, p.UncheckedHosts()...)
	}
	return withSocket(ctx, p)
}

// ProxyChoice is the proxy requests made on the provider's behalf go
// through, as netproxy.With takes it: for one account of a subscription
// that holds several, or one key of a provider that holds several
// (Beyfish_Wang on X: 不同 key 走不同的代理), its own (AccountProxies),
// else the provider's (Proxy); "" follows the global one. An account in
// use beside the agent's own is made afresh by some agents (AlsoOn),
// without the provider's settings: those are read from providers.json.
func (p Provider) ProxyChoice() string {
	if p.Account == nil {
		if v := p.AccountProxies[keyID(p.Key)]; p.Key != "" && v != "" {
			return v
		}
		return p.Proxy
	}
	own, prov := p.AccountProxies, p.Proxy
	if own == nil && prov == "" {
		if s, ok := storedPicks(p.ID); ok {
			own, prov = s.AccountProxies, s.Proxy
		}
	}
	if v := own[accountKey(p.Account.User)]; v != "" {
		return v
	}
	return prov
}

// ProxyOf is the own proxy of the provider or signed-in account with id,
// "" when it follows the global one.
func ProxyOf(id string) string {
	if s, ok := storedPicks(id); ok {
		return s.Proxy
	}
	return ""
}

// ProxyOfLogin is the proxy requests made on behalf of the account user
// of the subscription id go through (gakki: one Codex account through one
// proxy, another through another): the account's own, or else the
// subscription's (ProxyOf); "" follows the global one.
func ProxyOfLogin(id, user string) string {
	s, ok := storedPicks(id)
	if !ok {
		return ""
	}
	if v := s.AccountProxies[accountKey(user)]; v != "" {
		return v
	}
	return s.Proxy
}

// Via is ctx for requests made on behalf of the provider or signed-in
// account with id (see Provider.Via).
func Via(ctx context.Context, id string) context.Context {
	return netproxy.With(ctx, ProxyOf(id))
}

// ViaLogin is ctx for requests made on behalf of the account user of the
// subscription id (ProxyOfLogin).
func ViaLogin(ctx context.Context, id, user string) context.Context {
	return netproxy.With(ctx, ProxyOfLogin(id, user))
}

// ViaSignedIn is ctx for requests made with the sign-in an agent has now
// (Codex's own, relayed as it came): through the proxy of the account it
// is signed in to.
func ViaSignedIn(ctx context.Context, id string) context.Context {
	if l, ok := liveLogin(id); ok {
		return ViaLogin(ctx, id, l.User)
	}
	return Via(ctx, id)
}

func storedPicks(id string) (Provider, bool) {
	for _, p := range storedFile().Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// accountKey is how an account is known in AccountProxies: its name as the
// accounts list shows it, in lower case, as accounts are told apart.
func accountKey(user string) string { return strings.ToLower(strings.TrimSpace(user)) }

// normalAccountProxies is m as it is kept: keys in lower case, addresses
// trimmed, accounts following the provider's proxy left out; nil for none.
func normalAccountProxies(m map[string]string) map[string]string {
	var out map[string]string
	for u, v := range m {
		u, v = accountKey(u), strings.TrimSpace(v)
		if u == "" || v == "" {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[u] = v
	}
	return out
}

// keyProxies is a provider of keys' AccountProxies as it is kept: those
// of the keys it still holds, by their KeyID; nil for none.
func keyProxies(p Provider) map[string]string {
	var out map[string]string
	for _, k := range p.KeyList() {
		if v := p.AccountProxies[k.ID]; v != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[k.ID] = v
		}
	}
	return out
}

// keepSignInProxy keeps the proxy an account of the subscription id was
// signed in through, picked for that sign-in, as the one its requests go
// through (#1663): the subscription's when the account is its only one,
// as the editor shows one account's proxy, else the account's own.
func keepSignInProxy(id, user, proxy string) error {
	proxy = strings.TrimSpace(proxy)
	if err := settings.CheckProxy(proxy); err != nil {
		return err
	}
	if ProxyOfLogin(id, user) == proxy {
		return nil
	}
	alone := true
	for _, l := range Logins(id) {
		if user == "" || accountKey(l.User) != accountKey(user) {
			alone = false
		}
	}
	f, err := read()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(f.Providers, func(p Provider) bool { return p.ID == id })
	if i < 0 {
		f.Providers = append(f.Providers, Provider{ID: id})
		i = len(f.Providers) - 1
	}
	p := &f.Providers[i]
	own := maps.Clone(p.AccountProxies)
	if own == nil {
		own = map[string]string{}
	}
	switch {
	case alone:
		p.Proxy = proxy
		delete(own, accountKey(user))
	case user == "":
		return errors.New("the account it signed in to isn't known")
	case proxy == p.Proxy:
		delete(own, accountKey(user))
	case proxy == "":
		// an account's own can't say "the global one": "" follows the
		// subscription's
		return errors.New("an account can't follow the global proxy while its subscription has a proxy of its own")
	default:
		own[accountKey(user)] = proxy
	}
	p.AccountProxies = normalAccountProxies(own)
	return store(f)
}

// checkAccountProxies refuses an account's proxy Settings would refuse.
func checkAccountProxies(m map[string]string) error {
	for u, v := range m {
		if err := settings.CheckProxy(v); err != nil {
			return fmt.Errorf("%s: %w", u, err)
		}
	}
	return nil
}

// ProxyUse is a place a saved proxy is chosen: a provider (Account empty),
// or an account or key of one.
type ProxyUse struct {
	Provider string `json:"provider"`
	Account  string `json:"account,omitempty"`
}

// ProxyUses lists where the saved proxy id is chosen among the providers, so
// that deleting it can say what would lose it. Settings' own two (global,
// plugin downloads) are the caller's.
func ProxyUses(id string) []ProxyUse {
	ref := settings.ProxyRef(id)
	var out []ProxyUse
	for _, p := range storedFile().Providers {
		name := p.Name
		if name == "" {
			name = p.ID
		}
		if strings.TrimSpace(p.Proxy) == ref {
			out = append(out, ProxyUse{Provider: name})
		}
		accts := make([]string, 0, len(p.AccountProxies))
		for a, v := range p.AccountProxies {
			if strings.TrimSpace(v) == ref {
				accts = append(accts, a)
			}
		}
		slices.Sort(accts)
		for _, a := range accts {
			out = append(out, ProxyUse{Provider: name, Account: a})
		}
	}
	return out
}

// ProxyGone is the name of a saved proxy p chooses (its own, or an
// account's) that isn't on this computer, "" when none is missing: what a
// sync from a magpie that didn't bring the proxy list leaves.
func ProxyGone(p Provider) string {
	gone := func(v string) string {
		v = strings.TrimSpace(v)
		if !settings.IsProxyRef(v) {
			return ""
		}
		if _, ok := settings.ResolveProxy(v); ok {
			return ""
		}
		return settings.ProxyLabel(settings.ProxyID(v))
	}
	if n := gone(p.Proxy); n != "" {
		return n
	}
	keys := slices.Sorted(maps.Keys(p.AccountProxies))
	for _, k := range keys {
		if n := gone(p.AccountProxies[k]); n != "" {
			return n
		}
	}
	return ""
}

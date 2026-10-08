package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/netproxy"
	"github.com/yetone/magpie/internal/settings"
)

// Via is ctx for requests made on the provider's behalf: they go through
// its own proxy (ProxyChoice), or the global one when it has none.
func (p Provider) Via(ctx context.Context) context.Context {
	return netproxy.With(ctx, p.ProxyChoice())
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
	for _, p := range load().Providers {
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

// checkAccountProxies refuses an account's proxy Settings would refuse.
func checkAccountProxies(m map[string]string) error {
	for u, v := range m {
		if err := settings.CheckProxy(v); err != nil {
			return fmt.Errorf("%s: %w", u, err)
		}
	}
	return nil
}

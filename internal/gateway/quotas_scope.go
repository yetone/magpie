package gateway

import (
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A key held to a budget, models or accounts is told none of this
// magpie's quotas (#1981), unless the key's owner lets it be told those
// of what it may use (access.Key.Quotas, Kenoia, #2216). It is then told
// a card only when:
//
//   - the card's provider is one of this magpie's, in the catalog, and
//     the key may use every model the catalog lists of it: a key held to
//     some of a provider's models is told nothing of the provider, since
//     its quota is spent by the models the key may not use as well; and
//   - the card's account or key is one the key may use (AllowsAccount):
//     when the key's account list names the provider, the card's account
//     must be one it names, matched by who is signed in or the key's
//     name; a card no account of the provider is known by is told to no
//     such key.
//
// The quotas, their kept report, their history, the cards another magpie
// reads and a card's refresh all go through it. The accounts' lanes
// (/v1/magpie/concurrency) and the usage summary are still refused.

// quotaScope is what a held key with Quotas set may be told; nil is
// everything, for a key held to nothing and this machine's owner.
type quotaScope struct {
	who   access.Identity
	every map[string]bool // provider id → the key may use every model of it
	refs  map[string][]provider.AccountRef
}

// scopeQuotas is the request's quota scope. It answers a held key that
// may not be told quotas 403 (refuseHeld) and says so with ok false.
func scopeQuotas(w http.ResponseWriter, r *http.Request) (*quotaScope, bool) {
	who := access.Caller(r.Context())
	if !heldKey(who) {
		return nil, true
	}
	if !who.Quotas {
		refuseHeld(w, r, heldQuotasOff)
		return nil, false
	}
	return newQuotaScope(who, provider.Catalog()), true
}

func newQuotaScope(who access.Identity, catalog []provider.Entry) *quotaScope {
	s := &quotaScope{who: who, every: map[string]bool{}, refs: map[string][]provider.AccountRef{}}
	for _, e := range catalog {
		if e.Group != "" || e.Provider.ID == "" {
			continue
		}
		id := e.Provider.ID
		ok, seen := s.every[id]
		if seen && !ok {
			continue
		}
		s.every[id] = modelAllowed(who, e.Provider, e.Model)
	}
	return s
}

// provider says the key may be told provider id's quota at all.
func (s *quotaScope) provider(id string) bool {
	if s == nil {
		return true
	}
	return s.every[id]
}

// card says the key may be told the card of provider id's account user.
func (s *quotaScope) card(id, user string) bool {
	if s == nil {
		return true
	}
	if !s.provider(id) {
		return false
	}
	if len(s.who.Accounts) == 0 {
		return true
	}
	refs, ok := s.refs[id]
	if !ok {
		p, err := provider.Find(id)
		if err != nil {
			return false
		}
		refs = p.AccountIDs()
		s.refs[id] = refs
	}
	// a provider's only key has a card named by no one (cardKeys)
	if strings.TrimSpace(user) == "" && len(refs) == 1 {
		return s.who.AllowsAccount(id, refs[0].ID)
	}
	matched := false
	for _, a := range refs {
		if !strings.EqualFold(strings.TrimSpace(a.User), strings.TrimSpace(user)) {
			continue
		}
		// two accounts or keys shown by one name: the key may use both,
		// or is told neither's card
		if !s.who.AllowsAccount(id, a.ID) {
			return false
		}
		matched = true
	}
	if matched {
		return true
	}
	// a card no account is known by: only for a provider the key's list
	// names no account of, which it uses on every one
	return s.who.AllowsAccount(id, "")
}

// cards is cs as the key may be told them.
func (s *quotaScope) cards(cs []provider.SubscriptionQuota) []provider.SubscriptionQuota {
	if s == nil {
		return cs
	}
	out := []provider.SubscriptionQuota{}
	for _, q := range cs {
		if q.From == "" && s.card(q.Provider, q.User) {
			out = append(out, q)
		}
	}
	return out
}

// writeCards answers another magpie the cards the key may be told. A key
// told none of the cards here is answered as held: the other magpie then
// shows the key's own limit, not that nothing was read here, which a
// refresh wouldn't change.
func writeCards(w http.ResponseWriter, r *http.Request, s *quotaScope) {
	all := provider.CachedCards(time.Now())
	cs := s.cards(all)
	if len(cs) == 0 && len(all) > 0 {
		refuseHeld(w, r, heldRefused)
		return
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": cs})
}

// quotas is qs as the key may be told them. A window's whole (Holds,
// #2333) is left out: it is reckoned from every call routed through the
// account, other keys' and the owner's too, which a held key is not told.
func (s *quotaScope) quotas(qs []provider.Quota) []provider.Quota {
	if s == nil {
		return qs
	}
	out := []provider.Quota{}
	for _, q := range qs {
		if q.From == "" && s.card(q.Provider, q.User) {
			ws := make([]provider.QuotaSpan, len(q.Windows))
			for i, w := range q.Windows {
				w.Holds, w.HoldsWhy = nil, ""
				ws[i] = w
			}
			q.Windows = ws
			out = append(out, q)
		}
	}
	return out
}

// histories is hs as the key may be told them.
func (s *quotaScope) histories(hs []provider.QuotaHistory) []provider.QuotaHistory {
	if s == nil {
		return hs
	}
	out := []provider.QuotaHistory{}
	for _, h := range hs {
		if s.card(h.Provider, h.User) {
			out = append(out, h)
		}
	}
	return out
}

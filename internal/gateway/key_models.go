package gateway

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A gateway key may be held to some models (#882, access.Key.Models): it
// is shown only those in the model lists, and a request of it for another
// is refused before any provider is asked. A model is held to by its
// provider's id and its own, "<provider>/<model>", whatever the agent
// called it. A routing group passes when the key names it, "group/<id>"
// (or "group/*", every group), and then every member of it does, through
// it; else only when every model in it does. A group the key names inside
// one it doesn't counts for its own members.

// magpieChoseKey marks, in a request's context, a call magpie makes for a
// model the user picked in its settings rather than the caller (a web
// search's, an image description's, a Codex title's): the key's models
// don't hold it.
type magpieChoseKey struct{}

func magpieChose(ctx context.Context) context.Context {
	return context.WithValue(ctx, magpieChoseKey{}, true)
}

// keyHolds is the calling key's models, when they hold this request.
func keyHolds(r *http.Request) (access.Identity, bool) {
	who := access.Caller(r.Context())
	if !who.Restricted() {
		return who, false
	}
	if chose, _ := r.Context().Value(magpieChoseKey{}).(bool); chose {
		return who, false
	}
	return who, true
}

// accountHolds is the calling key's accounts, when they hold this
// request: magpie's own calls for the key — a web search, a picture
// described, a Codex title — are of the models the user picked, so the
// models don't hold them, but their spend lands on an account or key all
// the same, and the accounts hold them too.
func accountHolds(r *http.Request) (access.Identity, bool) {
	who := access.Caller(r.Context())
	return who, len(who.Accounts) > 0
}

// modelAllowed says the key may use provider p's model.
func modelAllowed(who access.Identity, p provider.Provider, model string) bool {
	return who.Allows(p.ID + "/" + model)
}

// memberAllowed says the key may use a routing group's member: the model,
// or a group it is reached through that the key names.
func memberAllowed(who access.Identity, m provider.Member) bool {
	return who.Allows(append([]string{m.Provider.ID + "/" + m.Model}, m.Path...)...)
}

// groupNamed says the key names the routing group g itself.
func groupNamed(who access.Identity, g provider.Group) bool {
	return g.ID != "" && who.Allows(provider.GroupPrefix+g.ID)
}

// groupAllowed says the key may use the routing group g: it names it, or
// may use every member of it.
func groupAllowed(who access.Identity, g provider.Group, ms []provider.Member) bool {
	return groupNamed(who, g) || membersAllowed(who, ms)
}

// groupKeeps is the members of g a request through it may go to for the
// key: every one when it names g, else those it may use.
func groupKeeps(who access.Identity, g provider.Group, ms []provider.Member) map[string]bool {
	out := map[string]bool{}
	named := groupNamed(who, g)
	for _, m := range ms {
		if named || memberAllowed(who, m) {
			out[m.Provider.ID+"/"+m.Model] = true
		}
	}
	return out
}

// membersAllowed says the key may use every member of a routing group:
// one it may use only some of is the key's no more than an empty one.
func membersAllowed(who access.Identity, ms []provider.Member) bool {
	if len(ms) == 0 {
		return !who.Restricted()
	}
	for _, m := range ms {
		if !memberAllowed(who, m) {
			return false
		}
	}
	return true
}

// entryAllowed says the key is shown a model of the catalog — and some
// account or key it may use serves it (#905): one the provider's
// accounts are set not to serve (#474) is none of the key's.
func entryAllowed(who access.Identity, e provider.Entry) bool {
	if !who.Restricted() {
		return true
	}
	if e.Group != "" {
		g, ms, ok := provider.FindGroup(e.ID)
		if !ok || !groupAllowed(who, g, ms) {
			return false
		}
		for _, m := range ms {
			if accountServes(who, m.Provider, m.Model) {
				return true
			}
		}
		return false
	}
	if !modelAllowed(who, e.Provider, e.Model) {
		return false
	}
	return accountServes(who, e.Provider, e.Model)
}

// accountServes says some account or key the key may use serves the provider's
// model: an account of several set to serve other models only (#474) is
// one the key may use, and none of the key's.
func accountServes(who access.Identity, p provider.Provider, model string) bool {
	if len(who.Accounts) == 0 {
		return true
	}
	refs := p.AccountIDs()
	if len(refs) == 0 {
		// a provider with no account and no key (a local ollama): the
		// list holds a key to some accounts of the providers it names
		// (AllowsAccount's rule), so one it names none of serves as it
		// always did; one it names — an entry kept after its last account
		// or key went — serves none, holding the key closer
		return who.AllowsAccount(p.ID, "")
	}
	for _, r := range refs {
		if !who.AllowsAccount(p.ID, r.ID) {
			continue
		}
		ref := r.User
		if r.Key {
			ref = r.ID // a key's own list is keyed by its fingerprint
		}
		if p.AccountServes(ref, model) {
			return true
		}
	}
	return false
}

// keyAllowed filters the catalog to what the calling key may use.
func keyAllowed(r *http.Request, es []provider.Entry) []provider.Entry {
	who, held := keyHolds(r)
	if !held {
		return es
	}
	out := make([]provider.Entry, 0, len(es))
	for _, e := range es {
		if entryAllowed(who, e) {
			out = append(out, e)
		}
	}
	return out
}

// allowedCandidates leaves out of a plan the providers' models the key
// may not use — a model's fallbacks are other models — and the accounts
// and keys it may not either, through a named group too: naming a group
// is not naming its accounts. The plan's order keeps step with what is
// left, and an account the key may not use is said on the plan's left,
// held. members are the group's the key may go to through it
// (groupKeeps), nil for a model. held says every candidate was an
// account or key the key may not use.
func allowedCandidates(who access.Identity, cs []candidate, pl planned, members map[string]bool) ([]candidate, planned, bool) {
	out := cs[:0:0]
	order := pl.order[:0:0]
	held := false
	for i, c := range cs {
		if !members[c.p.ID+"/"+c.model] && !modelAllowed(who, c.p, c.model) {
			continue // a model the key may not use: skipped, as before
		}
		if !accountAllowed(who, c) {
			held = true
			if i < len(pl.order) {
				w := pl.order[i]
				w.Held = true
				pl.left = append(pl.left, w)
			}
			continue
		}
		out = append(out, c)
		if i < len(pl.order) {
			order = append(order, pl.order[i])
		}
	}
	pl.order = order
	// nor is one held for its credits there for a reset to bring back
	pl.held = slices.DeleteFunc(slices.Clone(pl.held), func(c candidate) bool {
		return !members[c.p.ID+"/"+c.model] && !modelAllowed(who, c.p, c.model) || !accountAllowed(who, c)
	})
	return out, pl, held && len(out) == 0
}

// accountAllowed says the key may use a candidate's account or key: a
// signed-in account by its stable id, kept through renames, a key by its
// fingerprint — "<provider>/<ref>", told apart in lower case, as
// accounts are. A key that lists none uses every account, as keys
// always did.
func accountAllowed(who access.Identity, c candidate) bool {
	if len(who.Accounts) == 0 {
		return true
	}
	return who.AllowsAccount(c.p.ID, c.p.AccountID())
}

// allowedKey is p sent on the first of its keys in use the calling key
// may use (#905), for the paths that send on one key of the provider
// rather than over its candidates — a video, a drawing — in place of the
// first, which may be one the key may not: a key held to a later one is
// sent on it, and refused only when it may use none. A provider of an
// account keeps its one account, checked as it is; one with no account
// and no key (a local ollama) has nothing to hold. A key's own list of
// models (#474) is not weighed here; the decision path, which gates on
// one, picks its key itself.
func allowedKey(who access.Identity, p provider.Provider, model string) (provider.Provider, bool) {
	if p.Key == "" {
		return p, p.Account == nil || accountAllowed(who, candidate{p: p, model: model})
	}
	for _, k := range p.KeysOn() {
		if q := p.WithKey(k); accountAllowed(who, candidate{p: q, model: model}) {
			return q, true
		}
	}
	return p, false
}

// keyModelError is what a request for a model its key may not use is told.
func keyModelError(who access.Identity, model string) string {
	return fmt.Sprintf("The gateway key %q may not use %s; it may use %s. Change the key's models in magpie's Gateway page, or use a model it has.",
		who.KeyName, model, strings.Join(who.Models, ", "))
}

// keyAccountsError is what a request every candidate of which was an
// account or key its key may not use is told.
func keyAccountsError(who access.Identity, model string) string {
	names := provider.AccountNames()
	shown := make([]string, len(who.Accounts))
	for i, a := range who.Accounts {
		if n, ok := names[a]; ok {
			shown[i] = n
		} else {
			shown[i] = a
		}
	}
	return fmt.Sprintf("The gateway key %q is not allowed to use the accounts behind %s: no account or key behind it is one the key may use; it may use %s. Change the key's accounts in magpie's Gateway page, or use an account it has.",
		who.KeyName, model, strings.Join(shown, ", "))
}

// countHeld answers the 403 a gateway key held to some models (#882) gets
// for counting tokens on one it may not use, as it would be refused
// serving it: Anthropic's count_tokens and Gemini's :countTokens, id the
// model as each asks it, resolved — a bare group's name, an auto group's
// stand-in — and a group judged by its members. proto is the API the
// error is answered on.
func countHeld(w http.ResponseWriter, r *http.Request, proto provider.Protocol, id string) bool {
	keyWho, held := keyHolds(r)
	if !held {
		return false
	}
	if gid, ok := provider.GroupFor(id); ok {
		id = gid
	}
	if sid, ok := provider.AutoStandIn(id); ok {
		id = sid
	}
	p, model, ok := provider.Resolve(id)
	if !ok {
		return false // counted nowhere: a local estimate, as before
	}
	g, ms, isGroup := provider.FindGroup(id)
	if isGroup && !groupAllowed(keyWho, g, ms) || !isGroup && !modelAllowed(keyWho, p, model) {
		writeError(w, proto, http.StatusForbidden, keyModelError(keyWho, id))
		return true
	}
	return false
}

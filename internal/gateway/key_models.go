package gateway

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// A gateway key may be held to some models (#882, access.Key.Models): it
// is shown only those in the model lists, and a request of it for another
// is refused before any provider is asked. A model is held to by its
// provider's id and its own, "<provider>/<model>", whatever the agent
// called it. A routing group passes when the key names it, "group/<id>"
// (or "group/*", every group), a group magpie found by its auto/<model>
// too, either id passing for the other (allows), and then every member of
// it does, through it. A group of the user's own the key doesn't name is
// never the key's, however few models it has and all of them the key's
// (kautism on Discord: a key given gemini 3.8 was shown, and could use, a
// group the user made of gemini 3.8 alone, a group never handed out); a
// group magpie found, one model several providers serve, passes when
// every model in it does, as it is that model.

// magpieChoseKey marks, in a request's context, a call magpie makes for a
// model the user picked in its settings rather than the caller (a web
// search's, an image description's, a Codex title's): the key's models
// don't hold it.
type magpieChoseKey struct{}

func magpieChose(ctx context.Context) context.Context {
	return context.WithValue(ctx, magpieChoseKey{}, true)
}

// keyHolds is the calling key's models, when they hold this request: the
// models it lists, and those a limit of some models it has spent counts
// (access.Identity.Spent, zola_xynb on X), which hold magpie's own calls
// for the key too, as their spend counts in it.
func keyHolds(r *http.Request) (access.Identity, bool) {
	who := access.Caller(r.Context())
	if chose, _ := r.Context().Value(magpieChoseKey{}).(bool); chose {
		if len(who.Spent) == 0 {
			return who, false
		}
		who.Models, who.AccountModels = nil, false
		who.KeyID = "" // nor do the keys a group is bound to (groupBarred)
		return who, true
	}
	return who, who.Restricted() || len(who.Spent) > 0
}

// keyAllows is whether the calling key may use one of p's models, nil when
// the key isn't held to any (keyHolds): for a model the client names inside
// a request, beside the one it asks for (Claude Code's advisor).
func keyAllows(r *http.Request, p provider.Provider) func(model string) bool {
	who, held := keyHolds(r)
	if !held {
		return nil
	}
	return func(model string) bool { return modelAllowed(who, p, model) }
}

// keyListHolds is keyHolds for a list of models shown to the key: a
// spent limit leaves its models listed, as a spent limit of the whole key
// does, and a request for one is told why it is refused.
func keyListHolds(r *http.Request) (access.Identity, bool) {
	who := access.Caller(r.Context())
	who.Spent = nil
	if !who.Restricted() {
		return who, false
	}
	if chose, _ := r.Context().Value(magpieChoseKey{}).(bool); chose {
		return who, false
	}
	return who, true
}

// spentFor is the key's spent limit that counts provider p's model, by
// any id the model goes by, the one that resets last when several do.
func spentFor(who access.Identity, p provider.Provider, model string) (access.SpentScope, bool) {
	if len(who.Spent) == 0 {
		return access.SpentScope{}, false
	}
	ids := []string{p.ID + "/" + model}
	if own := p.OwnModelID(model); own != model {
		ids = append(ids, p.ID+"/"+own)
	}
	if local := p.ListedModelID(model); local != model {
		ids = append(ids, p.ID+"/"+local)
	}
	var out access.SpentScope
	found := false
	for _, id := range ids {
		if s, ok := who.SpentOn(id); ok && (!found || s.Reset.After(out.Reset)) {
			out, found = s, true
		}
	}
	return out, found
}

// spentRef is the spent limit a request for ref is refused by: a model's
// that counts it, or for a routing group the key may use, the one of its
// members' that resets last, when each member is spent or not the key's.
func spentRef(who access.Identity, ref string) (access.SpentScope, bool) {
	if len(who.Spent) == 0 {
		return access.SpentScope{}, false
	}
	if g, ms, isGroup := provider.FindGroup(ref); isGroup {
		if !groupAllowed(who, g, ms) {
			return access.SpentScope{}, false
		}
		return spentOfMembers(who, ms)
	}
	if sid, ok := provider.AutoStandIn(ref); ok {
		ref = sid
	}
	p, model, ok := provider.Resolve(ref)
	if !ok {
		return access.SpentScope{}, false
	}
	return spentFor(who, p, model)
}

// spentOfMembers is the spent limit of the key's that counts one of a
// routing group's members ms, the one that resets last when several do.
func spentOfMembers(who access.Identity, ms []provider.Member) (access.SpentScope, bool) {
	var out access.SpentScope
	found := false
	for _, m := range ms {
		if s, ok := spentFor(who, m.Provider, m.Model); ok && (!found || s.Reset.After(out.Reset)) {
			out, found = s, true
		}
	}
	return out, found
}

// keyGroupRefusal is keyModelRefusal for a routing group whose members ms
// a route found itself (an embedding, image or video group's, which
// provider.FindGroup's chat members leave out): its 429 when a spent limit
// counts one of them.
func keyGroupRefusal(w http.ResponseWriter, who access.Identity, ref string, ms []provider.Member) (int, string) {
	if s, ok := spentOfMembers(who, ms); ok {
		return spentRefusal(w, s)
	}
	return keyModelRefusal(w, who, ref)
}

// keyModelRefusal is the status and message a request for ref its key may not
// use is answered with: a 429 saying the limit and when it resets, with
// Retry-After, when a limit of the key's spent on it is why, else
// keyModelError's 403. It names no reset in remoteKeyLimitHeader: a
// magpie using this one as a remote rests only the model on it, as the
// key's other models are still served.
func keyModelRefusal(w http.ResponseWriter, who access.Identity, ref string) (int, string) {
	if g, _, ok := provider.FindGroup(ref); ok && groupBarred(who, g) {
		return http.StatusForbidden, groupKeysError(who, g)
	}
	if s, ok := spentRef(who, ref); ok {
		return spentRefusal(w, s)
	}
	return http.StatusForbidden, keyModelError(who, ref)
}

// groupKeysError is what a key is told of a routing group bound to other
// keys (groupBarred).
func groupKeysError(who access.Identity, g provider.Group) string {
	return fmt.Sprintf("The gateway key %q may not use %s: the routing group is bound to other gateway keys. Bind this key to it in the group's editor on magpie's Routing page, or use another model.",
		who.KeyName, g.Ref())
}

// spentRefusal is keyModelRefusal's 429 for the spent limit s.
func spentRefusal(w http.ResponseWriter, s access.SpentScope) (int, string) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(s.Reset.Sub(limitClock()).Seconds()+0.999))))
	w.Header().Set("X-Should-Retry", "false")
	return http.StatusTooManyRequests, s.Why
}

// spentAmong is the spent limit of the key's that counts one of cs, the
// one that resets last when several do.
func spentAmong(who access.Identity, cs []candidate) (access.SpentScope, bool) {
	var out access.SpentScope
	found := false
	for _, c := range cs {
		if s, ok := spentFor(who, c.p, c.model); ok && (!found || s.Reset.After(out.Reset)) {
			out, found = s, true
		}
	}
	return out, found
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

// allows is who.Allows, a found group named by either of its ids: a key
// held to auto/<model> (as the model lists give it, any spelling of the
// model) may use group/auto-<slug>, the id the gateway routes it by, and
// one held to group/auto-<slug> or group/* the auto/<model> an agent asks
// for, which the gateway takes back to that id (validateModel). A pattern
// of auto/ ids ("auto/*") holds to the groups' auto/<model>.
func allows(who access.Identity, ids ...string) bool {
	if who.Allows(ids...) {
		return true
	}
	for _, id := range ids {
		if !strings.HasPrefix(id, provider.GroupPrefix+"auto-") {
			continue
		}
		if pub := provider.PublicRef(id); pub != id && who.Allows(pub) {
			return true
		}
		for _, p := range who.Models {
			if !strings.Contains(p, "*") && strings.HasPrefix(p, provider.AutoPrefix) && strings.EqualFold(provider.CanonicalRef(p), id) {
				return true
			}
		}
	}
	return false
}

// modelAllowed says the key may use provider p's model. A model the user lists by an id of their own (provider.SetModelID) is
// the one model by either id, so a key held to one may use the other:
// one written before the id was given, or an agent's old ref to the
// provider's own id.
func modelAllowed(who access.Identity, p provider.Provider, model string) bool {
	if _, spent := spentFor(who, p, model); spent {
		return false // a limit of the key's that counts it is spent
	}
	return modelListed(who, p, model)
}

// modelListed is modelAllowed by the key's models alone.
func modelListed(who access.Identity, p provider.Provider, model string) bool {
	if allows(who, p.ID+"/"+model) {
		return true
	}
	if own := p.OwnModelID(model); own != model {
		return allows(who, p.ID+"/"+own)
	}
	if local := p.ListedModelID(model); local != model {
		return allows(who, p.ID+"/"+local)
	}
	return false
}

// memberAllowed says the key may use a routing group's member: the model,
// or a group it is reached through that the key names.
func memberAllowed(who access.Identity, m provider.Member) bool {
	return modelAllowed(who, m.Provider, m.Model) || len(m.Path) > 0 && allows(who, m.Path...)
}

// groupNamed says the key names the routing group g itself.
func groupNamed(who access.Identity, g provider.Group) bool {
	return g.ID != "" && (allows(who, provider.GroupPrefix+g.ID) || who.Allows(g.Ref()))
}

// groupAllowed says the key may use the routing group g: it names it, or
// g is a group magpie found (foundGroup) and the key may use every member
// of it. A group of the user's own that a key listing models doesn't name
// is not its, every member its or not: what the key lists is what it was
// given (kautism). A key that names accounts and no model was given every
// model of their providers (access.HeldModels, Linka), and a group of
// those alone is one of them, as before.
// Every route asks this: the model lists (entryAllowed), a request on
// each API, count_tokens, embeddings, images, videos, the decision
// groups, a subagent's and masking's route.
// A member a spent limit counts doesn't take the group away from the key:
// the request goes on to the others (groupKeeps).
func groupAllowed(who access.Identity, g provider.Group, ms []provider.Member) bool {
	if groupBarred(who, g) {
		return false
	}
	who.Spent = nil
	return groupNamed(who, g) || (foundGroup(g) || who.AccountModels) && membersAllowed(who, ms)
}

// groupBarred says the routing group g is bound to some gateway keys
// (provider.Group.Keys, Xerxes754 on X) and who's key is none of them,
// nor issued under one: such a key may neither use the group nor see it
// listed, whatever models it may use, a key held to none included. It
// is the one check a key not held to some models goes through too
// (groupGiven, keyAllowed, keyListed); a key held to some is asked it in
// groupAllowed. A request with no key — this computer's own, which key
// limits never hold either — is never barred.
func groupBarred(who access.Identity, g provider.Group) bool {
	if len(g.Keys) == 0 || who.KeyID == "" {
		return false
	}
	for k := &who; k != nil; k = k.Issuer {
		if k.KeyID != "" && slices.Contains(g.Keys, k.KeyID) {
			return false
		}
	}
	return true
}

// groupGiven says a request may use the routing group g: held to some
// models (keyHolds' who and held), as groupAllowed says; else unless g
// is bound to other keys than its (groupBarred). A call magpie makes for
// a model the user picked in its settings (magpieChose) is held by no
// key, as keyHolds says.
func groupGiven(r *http.Request, who access.Identity, held bool, g provider.Group, ms []provider.Member) bool {
	if held {
		return groupAllowed(who, g, ms)
	}
	if chose, _ := r.Context().Value(magpieChoseKey{}).(bool); chose {
		return true
	}
	return !groupBarred(access.Caller(r.Context()), g)
}

// withGatewayKey says r comes with a gateway key that a routing group bound to
// other keys bars (groupBarred): any key, but on a call magpie makes for
// a model the user picked (magpieChose).
func withGatewayKey(r *http.Request) bool {
	if chose, _ := r.Context().Value(magpieChoseKey{}).(bool); chose {
		return false
	}
	return access.Caller(r.Context()).KeyID != ""
}

// foundGroup says g is a group magpie found (one model several providers
// serve, auto/<model>), the user's changes to it kept or not.
func foundGroup(g provider.Group) bool {
	return g.Auto || strings.HasPrefix(g.ID, "auto-")
}

// keyGroupFor is provider.GroupFor for the request's gateway key: a bare
// name that is a routing group of the user's the key may not use, and a
// model it may use, is that model for the key, as it would be were there
// no such group — the key is held away from the group, not from the model
// (a group the user named gemini-3.8, a key given google/gemini-3.8
// asking for gemini-3.8). Every other name is GroupFor's.
func keyGroupFor(r *http.Request, id string) (string, bool) {
	gid, ok := provider.GroupFor(id)
	if !ok {
		return gid, ok
	}
	who, held := keyHolds(r)
	if !held && !withGatewayKey(r) {
		return gid, true
	}
	if g, ms, found := provider.FindGroup(gid); !found || groupGiven(r, who, held, g, ms) {
		return gid, true
	}
	if p, model, served := provider.Resolve(id); served && !provider.IsGroupRef(id) && (!held || modelAllowed(who, p, model)) {
		return "", false
	}
	return gid, true
}

// groupKeeps is the members of g a request through it may go to for the
// key: every one when it names g, else those it may use; neither one a
// spent limit of the key's counts (zola_xynb: a group goes on to the
// models left, midway too, as the next request finds the limit spent).
func groupKeeps(who access.Identity, g provider.Group, ms []provider.Member) map[string]bool {
	out := map[string]bool{}
	named := groupNamed(who, g)
	for _, m := range ms {
		if _, spent := spentFor(who, m.Provider, m.Model); spent {
			continue
		}
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

// keyList weighs a model list's entries for one request's key (#1762):
// the routing groups are read once for the list, not once an entry —
// each read is every provider's models, its files stat'ed again, and a
// list of 1,000 models with 500 groups took seconds — and each
// provider's accounts and keys once, not once a model. Nothing is kept
// past the request, so the next sees a group, account or key's models
// changed, as before.
type keyList struct {
	who  access.Identity
	find func(string) (provider.Group, []provider.Member, bool)
	refs map[string][]provider.AccountRef
}

func newKeyList(who access.Identity) *keyList {
	return &keyList{who: who, find: provider.GroupFinder(), refs: map[string][]provider.AccountRef{}}
}

// entryAllowed says the key is shown a model of the catalog — and some
// account or key it may use serves it (#905): one the provider's
// accounts are set not to serve (#474) is none of the key's.
func (k *keyList) entryAllowed(e provider.Entry) bool {
	who := k.who
	if !who.Restricted() {
		return true
	}
	if e.Group != "" {
		g, ms, ok := k.find(e.ID)
		if !ok || !groupAllowed(who, g, ms) {
			return false
		}
		for _, m := range ms {
			if k.accountServes(m.Provider, m.Model) {
				return true
			}
		}
		return false
	}
	if !modelAllowed(who, *e.Provider, e.Model) {
		return false
	}
	return k.accountServes(*e.Provider, e.Model)
}

// accountServes is accountServes with p's accounts and keys read once
// for the list.
func (k *keyList) accountServes(p provider.Provider, model string) bool {
	if len(k.who.Accounts) == 0 {
		return true
	}
	refs, ok := k.refs[p.ID]
	if !ok {
		refs = p.AccountIDs()
		k.refs[p.ID] = refs
	}
	return accountServesOn(k.who, p, model, refs)
}

// accountServes says some account or key the key may use serves the provider's
// model: an account of several set to serve other models only (#474) is
// one the key may use, and none of the key's.
func accountServes(who access.Identity, p provider.Provider, model string) bool {
	if len(who.Accounts) == 0 {
		return true
	}
	return accountServesOn(who, p, model, p.AccountIDs())
}

// accountServesOn is accountServes over refs, p's accounts and keys.
func accountServesOn(who access.Identity, p provider.Provider, model string, refs []provider.AccountRef) bool {
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
	who, held := keyListHolds(r)
	if !held {
		if !withGatewayKey(r) || !provider.GroupsBound() {
			return es
		}
		// a key held to no models still isn't shown a group bound to
		// other keys (groupBarred)
		find := provider.GroupFinder()
		return slices.DeleteFunc(slices.Clone(es), func(e provider.Entry) bool {
			if e.Group == "" {
				return false
			}
			g, _, ok := find(e.ID)
			return ok && groupBarred(who, g)
		})
	}
	k := newKeyList(who)
	out := make([]provider.Entry, 0, len(es))
	for _, e := range es {
		if k.entryAllowed(e) {
			out = append(out, e)
		}
	}
	return out
}

// keyListed is keyAllowed for a list given to a caller (shown, from
// CatalogFor or CatalogOf): a model a routing group listing alone keeps
// out of the lists (provider.GroupStandIns) is the group's there only for
// a caller the group is given. A key that may use none of the groups
// standing in for it is listed the model itself, when it may use it, as
// nothing stands in for it for the key (luckymanjc on Discord, 11-05: a
// key held to every Codex model and his three accounts, his Remote
// magpie's, was listed GPT-6-Luna alone, the one Codex model no group of
// his listed alone). off is the list's own picks (ModelOff), nil for
// none; a model they leave out stays out.
func keyListed(r *http.Request, shown []provider.Entry, off func(string) bool) []provider.Entry {
	who, held := keyListHolds(r)
	if !held && (!withGatewayKey(r) || !provider.GroupsBound()) {
		return shown
	}
	if ins := provider.GroupStandIns(); len(ins) > 0 {
		find := provider.GroupFinder()
		given := func(id string) bool {
			g, ms, ok := find(id)
			return ok && (held && groupAllowed(who, g, ms) || !held && !groupBarred(who, g))
		}
		in := make(map[string]bool, len(shown))
		for _, e := range shown {
			in[e.ID] = true
		}
		shown = slices.Clone(shown)
		for _, s := range ins {
			e := s.Entry
			if in[e.ID] || off != nil && off(e.ID) || held && !modelAllowed(who, *e.Provider, e.Model) || slices.ContainsFunc(s.Groups, given) {
				continue
			}
			// beside its provider's other models, where it would be
			// listed were no group listing alone
			at := len(shown)
			for i := len(shown) - 1; i >= 0; i-- {
				if shown[i].Group == "" && shown[i].Provider.ID == e.Provider.ID {
					at = i + 1
					break
				}
			}
			shown = slices.Insert(shown, at, e)
			in[e.ID] = true
		}
	}
	return keyAllowed(r, shown)
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
				w.Held, w.HeldBy = true, who.Group
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
	// nor is one held for its credits there for a reset to bring back, or
	// one past its cap for a conversation kept strictly on it
	gone := func(c candidate) bool {
		return !members[c.p.ID+"/"+c.model] && !modelAllowed(who, c.p, c.model) || !accountAllowed(who, c)
	}
	pl.held = slices.DeleteFunc(slices.Clone(pl.held), gone)
	pl.capped = slices.DeleteFunc(slices.Clone(pl.capped), gone)
	// nor one whose own list lacks the model, asked once those that list
	// it refused the model (unlistedNext)
	var unlisted []candidate
	var unlistedW []Weighed
	for i, c := range pl.unlisted {
		if !gone(c) {
			unlisted, unlistedW = append(unlisted, c), append(unlistedW, pl.unlistedW[i])
		}
	}
	pl.unlisted, pl.unlistedW = unlisted, unlistedW
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

// heldByGroup is r with its caller held to the routing group g's own
// accounts too (Group.Accounts, duanckham on X): a request through the
// group is held to them by everything that holds one to its gateway key's
// accounts (accountHolds, accountAllowed, onKeys, allowedKey), on every
// route, as one rule (access.HeldToGroup). A group with none is r.
func heldByGroup(r *http.Request, g provider.Group) *http.Request {
	if len(g.Accounts) == 0 {
		return r
	}
	who := access.Caller(r.Context()).HeldToGroup(cmp.Or(g.Name, g.ID), g.Accounts)
	return r.WithContext(access.WithIdentity(r.Context(), who))
}

// memberOn is r held to the one account m is sent on, when it names one
// (EastonXvii on X), for the paths that go over a member's accounts
// themselves (a drawing's, a video's): r for a member on any.
func memberOn(r *http.Request, g provider.Group, m provider.Member) *http.Request {
	if m.Account == "" {
		return r
	}
	if len(m.Via) > 0 {
		g = m.Via[len(m.Via)-1]
	}
	who := access.Caller(r.Context()).HeldToGroup(cmp.Or(g.Name, g.ID), []string{m.Account})
	return r.WithContext(access.WithIdentity(r.Context(), who))
}

// heldByGroupAt is heldByGroup for the routing group id names, as a
// request may name one (a bare group's name, auto/<model>); r when it
// names none.
func heldByGroupAt(r *http.Request, id string) *http.Request {
	if gid, ok := provider.GroupFor(id); ok {
		id = gid
	}
	if sid, ok := provider.AutoStandIn(id); ok {
		id = sid
	}
	if g, ms, ok := provider.FindGroup(id); ok {
		r = heldByGroup(r, g)
		// counted on the member it resolves to: on its own account alone
		// when it names one (EastonXvii on X), as planLevel sends it
		if p, model, ok := provider.Resolve(id); ok {
			if i := slices.IndexFunc(ms, func(m provider.Member) bool { return m.Provider.ID == p.ID && m.Model == model }); i >= 0 && ms[i].Account != "" {
				g := g
				if len(ms[i].Via) > 0 {
					g = ms[i].Via[len(ms[i].Via)-1]
				}
				who := access.Caller(r.Context()).HeldToGroup(cmp.Or(g.Name, g.ID), []string{ms[i].Account})
				r = r.WithContext(access.WithIdentity(r.Context(), who))
			}
		}
	}
	return r
}

// groupHeld is the name of the first of gs, routing groups a candidate is
// reached through, held to accounts (Group.Accounts) that leave its account
// or key out — by AllowsAccount, a gateway key's rule; "" for none.
func groupHeld(c candidate, gs []provider.Group) string {
	for _, g := range gs {
		if len(g.Accounts) > 0 && !(access.Identity{Accounts: g.Accounts}).AllowsAccount(c.p.ID, c.p.AccountID()) {
			return cmp.Or(g.Name, g.ID)
		}
	}
	return ""
}

// memberHeld is the name of g, the group m is a member of, when m names
// an account of its own (Member.Account, EastonXvii on X) other than c's;
// "" when it names none or c's.
func memberHeld(c candidate, m provider.Member, g provider.Group) string {
	if m.Account == "" || (access.Identity{Accounts: []string{m.Account}}).AllowsAccount(c.p.ID, c.p.AccountID()) {
		return ""
	}
	if len(m.Via) > 0 {
		g = m.Via[len(m.Via)-1] // the group in the group that names it
	}
	return cmp.Or(g.Name, g.ID)
}

// allowedKey is p sent on the first of its keys in use the calling key
// may use (#905), for the paths that send on one key of the provider
// rather than over its candidates — a Grok or Volcengine video, which is
// asked after on the first key again — in place of the first, which may
// be one the key may not: a key held to a later one is sent on it, and
// refused only when it may use none. A provider of an account keeps its
// one account, checked as it is; one with no account and no key (a local
// ollama) has nothing to hold. A key's own list of models (#474) is not
// weighed here; the paths that can go over the keys do (onKeys, a
// drawing's, the decision path's).
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

// onKeys is the keys or accounts of p a request for model that isn't a
// chat goes over (a drawing, a video, an embedding), as a chat's go over
// them (#1657): in its routing's order, a key or account whose own list
// of models (#474) leaves the model out never, one the vendor lists the
// model to another only after those that list it, and those the calling
// key may not use (#905) left out. p's fallback models are not among
// them. With none left it gives the status and what the caller is told:
// 403 when every one is set not to serve the model, or is one the calling
// key may not use. A provider with no key and no account (a local ollama)
// is itself.
func (s *Server) onKeys(r *http.Request, p provider.Provider, model, asked string) ([]candidate, int, string) {
	if p.Account == nil && p.Key == "" {
		return []candidate{{p: p, model: model, rest: p.ID}}, 0, ""
	}
	q := p
	q.Fallback = nil
	cs, pl := s.plan(q, model, provider.Chat)
	if len(cs) == 0 && slices.ContainsFunc(pl.left, func(w Weighed) bool { return w.Barred }) && !slices.ContainsFunc(pl.left, func(w Weighed) bool { return !w.Barred && !w.StandIn }) {
		return nil, http.StatusForbidden, barredError(asked, pl.left)
	}
	if keyWho, held := accountHolds(r); held {
		cs = slices.DeleteFunc(cs, func(c candidate) bool { return !accountAllowed(keyWho, c) })
		if len(cs) == 0 {
			return nil, http.StatusForbidden, keyAccountsError(keyWho, asked)
		}
	}
	if len(cs) == 0 {
		cs = []candidate{{p: p, model: model, rest: p.ID}}
	}
	return cs, 0, ""
}

// keyModelError is what a request for a model its key may not use is told.
func keyModelError(who access.Identity, model string) string {
	return fmt.Sprintf("The gateway key %q may not use %s; it may use %s. Change the key's models in magpie's Gateway page, or use a model it has.",
		who.KeyName, model, strings.Join(who.Models, ", "))
}

// keyAccountsError is what a request every candidate of which was an
// account or key its key may not use is told — or its routing group,
// held to some accounts (who.Group).
func keyAccountsError(who access.Identity, model string) string {
	if who.Group != "" {
		msg := fmt.Sprintf("The routing group %q is held to some accounts, and none of the accounts or keys behind %s is one of them; it may use %s. Change the group's accounts in magpie's Routing page",
			who.Group, model, keyAccountNames(who))
		if who.KeyName != "" {
			msg += fmt.Sprintf(" (and the gateway key %q's in its Gateway page)", who.KeyName)
		}
		return msg + ", or use an account it has."
	}
	return fmt.Sprintf("The gateway key %q is not allowed to use the accounts behind %s: no account or key behind it is one the key may use; it may use %s. Change the key's accounts in magpie's Gateway page, or use an account it has.",
		who.KeyName, model, keyAccountNames(who))
}

// groupsHeldError is what a request is told when every account or key
// left was one a routing group it went through — a group in the group, a
// group a fallback reached — is held away from (Weighed.HeldBy).
func groupsHeldError(model string, left []Weighed) string {
	var by []string
	for _, w := range left {
		if w.HeldBy != "" && !slices.Contains(by, w.HeldBy) {
			by = append(by, w.HeldBy)
		}
	}
	return fmt.Sprintf("Every account or key behind %s is one the routing group %s is held away from: it is held to other accounts, or names another account for the model. Change the group's accounts in magpie's Routing page, or use an account it has.",
		model, strings.Join(by, ", "))
}

// keyAccountNames is the key's accounts as an error message shows them:
// named as the accounts list names them, "<provider>/<user>" where a
// provider has the account now, the id alone otherwise.
func keyAccountNames(who access.Identity) string {
	accounts := who.ShownAccounts()
	if len(accounts) == 0 {
		return "none"
	}
	return access.AccountsShown(provider.AccountNames(), accounts)
}

// countHeld answers the 403 a gateway key held to some models (#882) gets
// for counting tokens on one it may not use, as it would be refused
// serving it: Anthropic's count_tokens and Gemini's :countTokens, id the
// model as each asks it, resolved — a bare group's name, an auto group's
// stand-in — and a group judged as a request to it is (groupAllowed).
// proto is the API the error is answered on.
func countHeld(w http.ResponseWriter, r *http.Request, proto provider.Protocol, id string) bool {
	keyWho, held := keyHolds(r)
	if !held && !withGatewayKey(r) {
		return false
	}
	if gid, ok := keyGroupFor(r, id); ok {
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
	if isGroup && !groupGiven(r, keyWho, held, g, ms) || held && !isGroup && !modelAllowed(keyWho, p, model) {
		st, msg := keyModelRefusal(w, keyWho, id)
		writeError(w, proto, st, msg)
		return true
	}
	return false
}

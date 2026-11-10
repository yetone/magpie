package provider

// A group's member may carry a reasoning effort of its own (#189):
// "provider/model:low". A request routed to that member asks its model for
// that much reasoning — whatever the agent asked for, and whatever the
// group's classifier picked for the turn — at the level the model has
// nearest to it. A member without one reasons as the group says (the
// agent's, or the classifier's pick).
//
// The effort is part of the member's id: the same model at two efforts is
// two members, which rules, stickiness and the trace tell apart, while the
// vendor is asked for the model's own id. It is stored as it is typed, so
// a group saved before reads the same, and a backup or a sync carries it
// as any other member.
//
// A member may instead carry several levels, joined by "+"
// ("provider/model:low+high", Garydesu on X): it follows the agent's
// effort, as a member without one does, but only among those levels — the
// group offers agents them for it (groupEntries), and a request asking
// for another is sent the nearest of them (Member.Levels). One level is a
// fixed effort as before, so a member saved before reads the same.
//
// A model's own id may have a colon in it — OpenRouter's
// "deepseek/deepseek-r1:free", Ollama's "qwen:7b", Bedrock's
// "anthropic.claude-…-v1:0" — so only a last part that is a level's name
// is taken as an effort, and only when the whole id isn't a model the
// provider has.

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"

	"github.com/yetone/magpie/internal/catalog"
)

// MemberEfforts are the levels a group's member may be fixed at, lowest
// first: the words agents and vendors use for reasoning effort.
var MemberEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// MemberEffort splits a group's member into the model it names and the
// effort fixed on it ("" for none): "a/m:high" is a/m at high, unless a
// has a model "m:high" of its own. A group in the group ("group/<id>")
// takes none; one typed with one comes back split, for SaveGroup to refuse.
func MemberEffort(id string) (model, effort string) {
	return memberEffortIn(nil, id)
}

// memberEffortIn is MemberEffort with the catalog's entries read already
// (nil to read the providers' models as needed). Several levels come
// back as MemberLevels reads them: each once, lowest first, joined by "+".
func memberEffortIn(entries []Entry, id string) (string, string) {
	id, _ = MemberAccount(id) // the account a member names comes after its effort (groupaccount.go)
	i := strings.LastIndex(id, ":")
	if i <= 0 {
		return id, ""
	}
	level := memberLevelsSpelled(id[i+1:])
	if level == "" {
		return id, "" // ":free", ":7b", ":0" — the model's own
	}
	if !strings.HasPrefix(id, GroupPrefix) && isModel(entries, id) {
		return id, ""
	}
	return id[:i], level
}

// memberLevelsSpelled is a member's effort suffix as it is kept: one
// level, or several joined by "+" each once, lowest first, lowercase; ""
// when a part isn't a level (the suffix is the model's own).
func memberLevelsSpelled(suffix string) string {
	parts := strings.Split(strings.ToLower(suffix), "+")
	for _, p := range parts {
		if !slices.Contains(MemberEfforts, p) {
			return ""
		}
	}
	return strings.Join(fixedLevels(parts), "+")
}

// MemberLevels splits the effort MemberEffort gives into the one a member
// is fixed at and the several the agent picks among (nil for one or none):
// "high" is fixed at high, "low+high" follows the agent among low and high.
func MemberLevels(effort string) (fixed string, levels []string) {
	if !strings.Contains(effort, "+") {
		return effort, nil
	}
	return "", strings.Split(effort, "+")
}

// Spelled is the member's model as the group names it: with the effort it
// is fixed at, or the levels it follows the agent among.
func (m Member) Spelled() string {
	return WithMemberEffort(m.Provider.ID+"/"+m.Model, cmp.Or(m.Effort, strings.Join(m.Levels, "+")))
}

// isModel reports whether id is a model a provider has as it is spelled,
// colon and all: a catalog id, a model the user picked for its provider,
// or one its provider lists.
func isModel(entries []Entry, id string) bool {
	if slices.ContainsFunc(entries, func(e Entry) bool { return e.ID == id || e.Model == id }) {
		return true
	}
	// what the providers have, looked at once while a request holds them
	// (XcodeFish, #2502): a group's members are expanded several times a
	// request, and each member at an effort found every provider again
	// (Find) and read its whole model list (Available), a third of
	// /v1/models' CPU
	return heldOf("has-model\x00"+id, func() bool { return providersHave(id) })
}

// providersLooked counts providersHave's looks, for tests.
var providersLooked atomic.Int64

// providersHave is isModel's look at the providers, past the catalog.
func providersHave(id string) bool {
	providersLooked.Add(1)
	pid, model, ok := strings.Cut(id, "/")
	var ps []Provider
	if ok {
		if p, err := Find(pid); err == nil {
			ps = []Provider{*p}
		}
	} else {
		model, ps = id, All()
	}
	for _, p := range ps {
		if slices.Contains(p.Models, model) || slices.ContainsFunc(p.Available(), func(m catalog.Model) bool { return m.ID == model }) {
			return true
		}
	}
	return false
}

// WithMemberEffort is the member id of model at effort: the model itself
// for "".
func WithMemberEffort(model, effort string) string {
	if effort == "" {
		return model
	}
	return model + ":" + effort
}

// cleanMember spells a member as it is kept: its effort lowercase, a found
// group by group/auto-<slug> (CanonicalRef), the account it names by its
// stable id (memberAccountKept).
func cleanMember(entries []Entry, id string) string {
	id, acct := MemberAccount(id)
	id = CanonicalRef(id) // a found group by the id magpie keeps it by
	m, e := memberEffortIn(entries, id)
	if acct != "" {
		acct, _ = memberAccountKept(m, acct)
	}
	return WithMemberAccount(WithMemberEffort(m, e), acct)
}

// fixedLevels are the efforts a group's members are fixed at, lowest first.
func fixedLevels(fixed []string) []string {
	var out []string
	for _, l := range MemberEfforts {
		if slices.Contains(fixed, l) {
			out = append(out, l)
		}
	}
	return out
}

// CleanLevels are the reasoning levels a group names for itself
// (Group.Levels) as they are kept: lowercase, each once, lowest first.
// Only the levels magpie knows are taken.
func CleanLevels(in []string) ([]string, error) {
	var want []string
	for _, l := range in {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" {
			continue
		}
		if !slices.Contains(Levels, l) {
			return nil, fmt.Errorf("%q is not a reasoning level magpie knows (they are %s)", l, strings.Join(Levels, ", "))
		}
		want = append(want, l)
	}
	var out []string
	for _, l := range Levels {
		if slices.Contains(want, l) {
			out = append(out, l)
		}
	}
	return out, nil
}

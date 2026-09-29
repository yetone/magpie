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
// A model's own id may have a colon in it — OpenRouter's
// "deepseek/deepseek-r1:free", Ollama's "qwen:7b", Bedrock's
// "anthropic.claude-…-v1:0" — so only a last part that is a level's name
// is taken as an effort, and only when the whole id isn't a model the
// provider has.

import (
	"slices"
	"strings"

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
// (nil to read the providers' models as needed).
func memberEffortIn(entries []Entry, id string) (string, string) {
	i := strings.LastIndex(id, ":")
	if i <= 0 {
		return id, ""
	}
	level := strings.ToLower(id[i+1:])
	if !slices.Contains(MemberEfforts, level) {
		return id, "" // ":free", ":7b", ":0" — the model's own
	}
	if !strings.HasPrefix(id, GroupPrefix) && isModel(entries, id) {
		return id, ""
	}
	return id[:i], level
}

// isModel reports whether id is a model a provider has as it is spelled,
// colon and all: a catalog id, a model the user picked for its provider,
// or one its provider lists.
func isModel(entries []Entry, id string) bool {
	if slices.ContainsFunc(entries, func(e Entry) bool { return e.ID == id || e.Model == id }) {
		return true
	}
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

// cleanMember spells a member as it is kept: its effort lowercase.
func cleanMember(entries []Entry, id string) string {
	m, e := memberEffortIn(entries, id)
	return WithMemberEffort(m, e)
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

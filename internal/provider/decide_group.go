package provider

import (
	"fmt"
	"slices"
	"strings"
)

// A decision group is a routing group of decision models (Jev and the
// like) and nothing else (ARNO on Discord: 给decision模型也加上group功能):
// a System One request naming it (/v1/systemone, "group/<id>" or the
// group's id or name) goes to its models as a conversation goes to a
// chat group's, the next asked when one fails. It holds no conversation,
// so it is kept out of the agents' catalog, a chat group can't hold it,
// and it takes nothing that shapes a conversation: no patterns, rules,
// classifier, effort, levels or fast mode.

// decidingMember reports whether a group's member names a decision model:
// "provider/model" whose provider answers model at its decision API, on
// or not. A group, or a member with an effort of its own, is not one.
func decidingMember(id string) bool {
	if IsGroupRef(id) {
		return false
	}
	if IsDecider(id) {
		return true
	}
	pid, model, ok := strings.Cut(id, "/")
	if !ok || model == "" {
		return false
	}
	p, err := Find(pid)
	return err == nil && p.DecidesModel(model)
}

// chattingMember reports whether a group's member names a decision model
// that holds conversations too (Provider.ChatsToo).
func chattingMember(id string) bool {
	if IsGroupRef(id) {
		return false
	}
	pid, model, ok := strings.Cut(id, "/")
	if !ok || model == "" {
		return false
	}
	p, err := Find(pid)
	return err == nil && p.ChatsToo(model)
}

// Decides reports whether g is a decision group: every model it names is
// a decision model.
func (g Group) Decides() bool {
	return len(g.Members) > 0 && !slices.ContainsFunc(g.Members, func(m string) bool { return !decidingMember(m) })
}

// DecisionGroup reports whether ms, a group's models as they resolve now,
// are decision models, every one: the group answers System One, not a
// conversation.
func DecisionGroup(ms []Member) bool {
	return len(ms) > 0 && !slices.ContainsFunc(ms, func(m Member) bool { return !m.Provider.DecidesModel(m.Model) })
}

// cleanDecisionGroup checks a decision group as SaveGroup is to keep it,
// and a chat group for decision models or decision groups in it; all is
// every group magpie has.
func cleanDecisionGroup(g *Group, all []Group) error {
	var deciders, others []string
	for _, m := range g.Members {
		model, _ := memberEffortIn(nil, m)
		switch {
		case chattingMember(model):
			// Ark's decision model holds conversations too: a chat
			// group may have it beside its other models, and a decision
			// group beside other decision models
		case decidingMember(model):
			deciders = append(deciders, m)
		default:
			others = append(others, m)
		}
	}
	if len(deciders) == 0 && (len(others) > 0 || len(g.Members) == 0) {
		// a chat group: none of its groups may be a decision group (one
		// of Ark's decision model alone is a decision group, as Decides
		// says)
		for _, m := range g.Members {
			if gid, ok := strings.CutPrefix(m, GroupPrefix); ok {
				if sub, ok := groupOf(all, gid); ok && sub.Decides() {
					return fmt.Errorf("%s holds decision models, which answer System One, not a conversation: it can be %s's classifier, not one of its models", sub.Name, g.Name)
				}
			}
		}
		return nil
	}
	if len(others) > 0 {
		return fmt.Errorf("%s is a decision model and %s holds conversations: a group is of decision models only, asked at /v1/systemone, or of models that hold conversations", deciders[0], others[0])
	}
	switch {
	case len(g.Match) > 0:
		return fmt.Errorf("%s is of decision models: it takes them by name, not by a pattern", g.Name)
	case len(g.Rules) > 0, g.Classifier != "", g.Effort != "":
		return fmt.Errorf("%s is of decision models, asked at /v1/systemone: it has no conversation for rules, a classifier or an effort to pick for", g.Name)
	case len(g.Levels) > 0, len(g.Fast) > 0:
		return fmt.Errorf("%s is of decision models: they take no reasoning level or fast mode", g.Name)
	}
	for _, m := range g.Members {
		if model, effort := memberEffortIn(nil, m); effort != "" {
			return fmt.Errorf("%s is a decision model: it takes no effort of its own (:%s)", model, effort)
		}
	}
	return nil
}

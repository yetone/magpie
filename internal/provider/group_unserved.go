package provider

import (
	"cmp"
	"slices"
	"sync"
)

// Why a routing group of the user's is not offered to an agent
// (UnservedGroup.Why). The GUI says each in the page's language.
const (
	WhyGroupOff       = "switched off on the Routing page"
	WhyGroupUnticked  = "its models are unticked in their providers' model lists"
	WhyGroupTurnedOff = "its models are turned off for every request"
	WhyGroupNoServer  = "no provider serves its models now"
	WhyGroupAgentCant = "this agent can't use its models"
	// the three below the gateway serves: only the agent's lists leave
	// it out, and picking it puts it back in them (ShowFor)
	WhyGroupNarrowed  = "this agent is shown other providers and groups only"
	WhyGroupNotPicked = "not picked for this agent, which is shown only the models picked for it"
	WhyGroupTakenOut  = "taken out of this agent's model list"
)

// UnservedGroup is a routing group of the user's that an agent's lists
// may not offer, and why: its picker shows one it lacks all the same, so
// that a group is never gone from it without a word (@CHENGJIAHEGOXIS on
// X: DeepSeek-API-Agents, on on the Routing page, was in no section of
// Codex's picker).
type UnservedGroup struct {
	Ref   string   // group/<id>, as agents are given it
	Name  string   // as the user named it
	Icons []string // its members' providers' icons, each once
	// Why is one of the Why… words, "" for a group the agent's lists
	// offer
	Why string
}

// Pickable reports whether the gateway serves the group, which only the
// agent's own lists leave out: picked, it is put back in them (ShowFor).
func (u UnservedGroup) Pickable() bool {
	return u.Why == WhyGroupNarrowed || u.Why == WhyGroupNotPicked || u.Why == WhyGroupTakenOut
}

// UnservedGroups are the user's own routing groups, in the Routing page's
// order, each with why agent's lists (CatalogFor) leave it out, "" for
// one they offer. A group magpie found is left out of these, as is one
// the user removed, one that answers System One alone (DecisionGroup),
// and every group while the user's groups aren't served (Groups has none
// then).
func UnservedGroups(agent string) []UnservedGroup {
	entries := providerEntries()
	all := sharedGroupsIn(entries)
	if !slices.ContainsFunc(all, func(g Group) bool { return !g.Auto && !g.Hidden }) {
		return nil
	}
	shown, hidden := CatalogFor(agent)
	in := func(list []Entry, gid string) (Entry, bool) {
		i := slices.IndexFunc(list, func(e Entry) bool { return e.Group == gid })
		if i < 0 {
			return Entry{}, false
		}
		return list[i], true
	}
	// read only for a group the agent's lists keep out
	listed := sync.OnceValue(func() []Entry { l, _ := ListedFor(agent); return l })
	_, only := PickedModels(agent)
	var out []UnservedGroup
	for _, g := range all {
		if g.Auto || g.Hidden {
			continue
		}
		u := UnservedGroup{Ref: GroupPrefix + g.ID, Name: g.Name}
		if e, ok := in(shown, g.ID); ok {
			u.Icons = e.Icons
			out = append(out, u)
			continue
		}
		if e, ok := in(hidden, g.ID); ok {
			u.Icons = e.Icons
			switch {
			case !slices.ContainsFunc(listed(), func(x Entry) bool { return x.Group == g.ID }):
				u.Why = WhyGroupNarrowed
			case only:
				u.Why = WhyGroupNotPicked
			default:
				u.Why = WhyGroupTakenOut
			}
			out = append(out, u)
			continue
		}
		u.Icons, u.Why = whyUnserved(entries, all, g)
		if u.Why == "" {
			continue // a decision group: no agent's conversation
		}
		out = append(out, u)
	}
	return out
}

// whyUnserved is why g, of the user's, has no entry in the catalog, and
// the icons of the providers its members name: "" when it is a decision
// group, which answers System One and is offered to no agent.
func whyUnserved(entries []Entry, all []Group, g Group) (icons []string, why string) {
	if g.Disabled {
		why = WhyGroupOff
	}
	if ms := membersIn(entries, all, g); len(ms) > 0 {
		if DecisionGroup(ms) {
			return nil, ""
		}
		for _, m := range ms {
			if !slices.Contains(icons, m.Provider.Icon) {
				icons = append(icons, m.Provider.Icon)
			}
		}
		return icons, cmp.Or(why, WhyGroupNoServer)
	}
	find := entriesBy(entries)
	off := disabledNow()
	unticked, turnedOff := false, false
	for _, id := range g.routes() {
		if IsGroupRef(id) {
			continue
		}
		model, _ := memberEffortIn(entries, id)
		p, m, ok := resolveIn(entries, model)
		if !ok {
			continue
		}
		if !slices.Contains(icons, p.Icon) {
			icons = append(icons, p.Icon)
		}
		// a model turned off is in no provider's entries, so it reads as
		// unticked too: asked first
		switch {
		case off(p.ID, m):
			turnedOff = true
		case unpicked(find, p, listedSpelling(entries, p.ID, m)):
			unticked = true
		}
	}
	switch {
	case why != "":
	case turnedOff:
		why = WhyGroupTurnedOff
	case unticked:
		why = WhyGroupUnticked
	default:
		why = WhyGroupNoServer
	}
	return icons, why
}

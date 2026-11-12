package provider

import (
	"slices"
	"strings"
)

// An agent's own subscription first (freefcw on X): Codex, signed in to
// ChatGPT and started by another app (Mirasim), asked for gpt-6-astra by
// its bare name. Devin and Cursor served it too, so the name was the group
// magpie found of it (auto-gpt-6-astra), which put the subscription that
// renews soonest first: Devin. With found groups off the name went to the
// first provider in the Providers order that served it, Devin again when
// it was added first.
//
// A bare model's id an agent asks for, which that agent's own subscription
// in magpie serves (a provider whose Account.Agent is the agent: Codex's
// ChatGPT accounts for Codex, Claude's for Claude Code), now goes to that
// subscription, found groups on or off. What stays as it was:
//   - a group the user made, or a found one they changed (no longer Auto),
//     named by the bare id: theirs, routed as they set it;
//   - a found group magpie set one of the agent's fields to (the gateway's
//     SetToGroup asks the agent's record): the user picked it for the agent;
//   - an id with a provider in it, or a group's ref (auto/<model>,
//     group/<id>): already where the user said;
//   - an agent with no subscription of its own in magpie, or one that
//     doesn't serve the model.
//
// With found groups off, a bare id several providers serve, none of them
// the asking agent's own subscription, is refused for an agent that has
// one in magpie (OffSpread): off, magpie picks no vendor for it.

// ownAgent is whether a provider is agent's own subscription: one of
// the agent's sign-ins that magpie serves. Plugins sign in as "plugin",
// never an agent's id.
func ownAgent(p Provider, agent string) bool {
	return agent != "" && agent != "plugin" && p.Account != nil && p.Account.Agent == agent
}

// IsOwn reports whether the provider pid is agent's own subscription.
func IsOwn(pid, agent string) bool {
	if agent == "" || agent == "plugin" {
		return false
	}
	p, err := findIn(sharedAll(), pid)
	return err == nil && ownAgent(*p, agent)
}

// bareModelID is id without Claude Code's [1m] mark, and whether it is a model's
// id with no provider in it and no group's ref.
func bareModelID(id string) (string, bool) {
	id = strings.TrimSuffix(strings.TrimSpace(id), "[1m]")
	if id == "" || strings.Contains(id, "/") || IsGroupRef(id) {
		return id, false
	}
	return id, true
}

// OwnRoute is where a bare model's id agent asks for goes on the agent's
// own subscription: its "provider/model". group is the found group's ref
// ("group/auto-…") the id named, which the request now goes past, "" when
// it named none (found groups off, or the group removed). ok is false when
// the id isn't bare, the user has a group of their own by it, or no
// subscription of the agent's serves it.
func OwnRoute(agent, id string) (to, group string, ok bool) {
	if agent == "" || agent == "plugin" {
		return "", "", false
	}
	if _, bare := bareModelID(id); !bare {
		return "", "", false
	}
	entries := providerEntries()
	return ownRouteIn(entries, groupsIn(entries), agent, id)
}

func ownRouteIn(entries []Entry, all []Group, agent, id string) (to, group string, ok bool) {
	id, bare := bareModelID(id)
	if !bare {
		return "", "", false
	}
	if gid, found := groupForIn(all, id); found {
		g, _ := groupOf(all, strings.TrimPrefix(gid, GroupPrefix))
		if !g.Auto {
			return "", "", false // the user's own group: routed as they set it
		}
		group = gid
	}
	own := ownEntry(entries, agent, id)
	if own == nil {
		return "", "", false
	}
	return own.ID, group, true
}

// ownEntry is the agent's own subscription's entry for the model id: by
// that id, else by the name vendors agree on (sameModel), as a found group
// merges it. nil for none.
func ownEntry(entries []Entry, agent, id string) *Entry {
	var alike *Entry
	k := sameModel(id)
	for i := range entries {
		e := &entries[i]
		if e.Group != "" || !ownAgent(*e.Provider, agent) {
			continue
		}
		if e.Model == id {
			return e
		}
		if alike == nil && sameModel(e.Model) == k {
			alike = e
		}
	}
	return alike
}

// OffSpread is the providers' ids ("provider/model") serving a bare model
// id that a request of agent's goes to none of, while found groups are
// off: several providers serve it, none of them the agent's own
// subscription, and the agent has one in magpie. Off, magpie picks none of
// them for the agent (freefcw on X); the request names one. nil when found
// groups are on, the id isn't bare or names a group of the user's, one
// provider serves it, or the agent has no subscription of its own here.
func OffSpread(agent, id string) []string {
	if agent == "" || agent == "plugin" || AutoGroupsOn() {
		return nil
	}
	if _, bare := bareModelID(id); !bare {
		return nil
	}
	if !slices.ContainsFunc(sharedAll(), func(p Provider) bool { return ownAgent(p, agent) }) {
		return nil
	}
	entries := providerEntries()
	return offSpreadIn(entries, groupsIn(entries), heldSettings().ModelSameAs, agent, id)
}

func offSpreadIn(entries []Entry, all []Group, same map[string]string, agent, id string) []string {
	id, _ = bareModelID(id)
	if _, found := groupForIn(all, id); found {
		return nil
	}
	if ownEntry(entries, agent, id) != nil {
		return nil
	}
	k := sameModel(id)
	var out []string
	var seen []string
	for _, e := range entries {
		if e.Group != "" || mergeKey(e.Provider.ID, e.Model, same) != k && e.Model != id {
			continue
		}
		if slices.Contains(seen, e.Provider.ID) {
			continue
		}
		seen = append(seen, e.Provider.ID)
		out = append(out, e.ID)
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

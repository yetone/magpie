package provider

import (
	"slices"
	"strings"
)

// PublicGroup is group id as ShareGroup shares it, for a link anyone can
// open on usemagpie.ai (internal/groupshare): a provider the user added
// themselves (a relay, an endpoint of their own) goes by "custom", as the
// usage stats name it (stats providerLabel), so the page never names the
// user's relay or what they called it. Its model ids stay: they are what
// another magpie matches the same model by (memberHere's AutoGroupOf). A
// built-in, a preset or a subscription goes by its built-in id.
func PublicGroup(id string) (SharedGroup, error) {
	s, err := sharedGroup(id)
	if err != nil {
		return s, err
	}
	g := &s.Group
	ref := publicMember
	for _, m := range slices.Clone(g.Members) {
		if to := ref(m); to != m {
			g.RenameMember(m, to)
		}
	}
	// a rule may send to a model one of its patterns matches
	for i := range g.Rules {
		g.Rules[i].MapUses(ref)
	}
	for i, m := range g.Match {
		g.Match[i] = ref(m)
	}
	g.MapClassifiers(ref)
	g.Members, g.Fast, g.Ultra, g.Off, g.Placed = cleanList(g.Members), cleanList(g.Fast), cleanList(g.Ultra), cleanList(g.Off), cleanList(g.Placed)
	return s, nil
}

// publicMember is member, pattern or classifier ref with its provider as
// PublicProvider names it; a group, or a provider this magpie hasn't, as
// it is.
func publicMember(ref string) string {
	pid, rest, ok := strings.Cut(strings.TrimSpace(ref), "/")
	if !ok || pid+"/" == GroupPrefix || strings.HasPrefix(ref, "re:") {
		return ref
	}
	p, err := Find(pid)
	if err != nil || p.ID != pid {
		return ref
	}
	return PublicProvider(*p) + "/" + rest
}

// PublicProvider is p's id as a page anyone reads names it: nothing the
// user typed. A plugin's provider Discover lists, a subscription, a
// preset go by their built-in ids; any other is "custom".
func PublicProvider(p Provider) string {
	switch {
	case p.IsPlugin():
		if id := p.PluginProvider(); Movable(id) || Preset(id) != nil {
			return id
		}
	case p.Account != nil:
		return p.Account.Agent
	case p.Preset != "":
		if d := Preset(p.Preset); d != nil {
			return d.ID
		}
	}
	return "custom"
}

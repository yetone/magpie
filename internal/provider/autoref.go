package provider

// The id agents are given for a group magpie found (ZeroClover on X): "auto/<model>",
// the model's own id as a vendor spells it (auto/claude-opus-5.5), so that a
// client reading the part after the slash as a model id — as models.dev keys
// its models — knows the model. It was "group/auto-<slug>" (Slug's, dots
// dashed), which nothing outside magpie could read, and that id is still
// served as it was, forever: agents' configs, gateway keys, saved groups and
// rules hold it.
//
// Inside magpie a found group stays "group/auto-<slug>": providers.json's
// members, rules and classifiers, the gateway's routing, a key's limits and
// the usage log keep that one id, so a group is counted under one name
// whichever an agent sent (CanonicalRef, taken back in the gateway before
// anything reads the request). The catalog lists the group by its public id
// (Group.Ref), so every list and config that is built from the catalog —
// /v1/models, the model lists written into agents, the pickers — names it so.
//
// "auto" is not a provider's id: Save and Rename refuse it, and a plugin
// that calls its provider auto is auto-plugin (PluginID). One of the user's
// own saved as "auto" before keeps its models: while it is there, auto/<x>
// is its model x, and found groups are listed as group/auto-<slug> as before
// (autoRefsOn).

import (
	"errors"
	"slices"
	"strings"
	"unicode"

	"github.com/yetone/magpie/internal/catalog"
)

// AutoPrefix starts the id agents are given for a group magpie found:
// "auto/<model>".
const AutoPrefix = "auto/"

// autoID is the provider id that would take auto/<model> for its own.
const autoID = "auto"

// errAutoID refuses "auto" as a new provider's id: auto/<model> names the
// groups magpie finds.
var errAutoID = errors.New(`"auto" starts the ids of the groups magpie finds (auto/<model>); pick another id`)

// autoRefsOn reports whether auto/<model> names found groups: unless the
// user has a provider of their own with the id "auto", saved before the
// id was kept for found groups, whose models it stays.
func autoRefsOn() bool { return !customIDs()[autoID] }

// AutoRefsOn is autoRefsOn, for the packages that spell a group's id.
func AutoRefsOn() bool { return autoRefsOn() }

// autoGroupOfRef is the group id auto/<model> names, model as any vendor
// spells it: the id magpie gives the group it finds for the model
// (AutoGroupID).
func autoGroupOfRef(model string) string { return AutoGroupID(model) }

// IsGroupRef reports whether ref names a routing group: group/<id>, or a
// found group's auto/<model>.
func IsGroupRef(ref string) bool {
	_, ok := GroupIDOf(ref)
	return ok
}

// CanonicalRef is ref as magpie keeps it: a found group's auto/<model> as
// group/auto-<slug>, an effort after it (":high") and Claude Code's [1m]
// mark kept; any other ref as it is. The gateway takes a request's model
// back to it before key limits, routing and the usage log see it, so the
// two ids of one group are one.
func CanonicalRef(ref string) string {
	if !strings.HasPrefix(strings.TrimSpace(ref), AutoPrefix) {
		return ref
	}
	return canonicalRef(ref, autoRefsOn())
}

// canonicalRef uses the ownership read by its caller, without reading settings.
func canonicalRef(ref string, enabled bool) string {
	s := strings.TrimSpace(ref)
	s, marked := strings.CutSuffix(s, "[1m]")
	m, ok := strings.CutPrefix(s, AutoPrefix)
	if !ok || strings.TrimSpace(m) == "" || !enabled {
		return ref
	}
	level := ""
	if i := strings.LastIndex(m, ":"); i > 0 && slices.Contains(MemberEfforts, strings.ToLower(m[i+1:])) {
		m, level = m[:i], ":"+strings.ToLower(m[i+1:])
	}
	gid := autoGroupOfRef(m)
	if gid == "auto-" {
		return ref
	}
	out := GroupPrefix + gid + level
	if marked {
		out += "[1m]"
	}
	return out
}

// canonicalRefs spells every model or group g names as magpie keeps it
// (CanonicalRef): a found group a picker listed as auto/<model> is stored
// as group/auto-<slug>, so its members, off and fast lists, weights, rules
// and classifier name it by one id. g's lists are its own (Group.own).
func (g *Group) canonicalRefs() {
	for _, l := range [][]string{g.Members, g.Off, g.Fast, g.Ultra} {
		for i, v := range l {
			l[i] = CanonicalRef(v)
		}
	}
	for k, v := range g.Weights {
		if c := CanonicalRef(k); c != k {
			delete(g.Weights, k)
			g.Weights[c] = v
		}
	}
	g.EffortMaps = renamedEffortMaps(g.EffortMaps, CanonicalRef)
	for i := range g.Rules {
		g.Rules[i].MapUses(CanonicalRef)
	}
	g.Classifier = CanonicalRef(g.Classifier)
}

// SameRef reports whether a and b name the same model or group, a found
// group by either of its ids.
func SameRef(a, b string) bool {
	return a == b || CanonicalRef(a) == CanonicalRef(b)
}

// Ref is the id agents pick the group by: auto/<model> for a group magpie
// found (one with an auto-… id whose members are that model), its model
// spelt as a vendor spells it; group/<id> for any other, and for every
// group while a provider of the user's is called auto.
func (g Group) Ref() string {
	if g.Spelled != "" && autoRefsOn() {
		return AutoPrefix + g.Spelled
	}
	return GroupPrefix + g.ID
}

// PublicRef is the id agents are given for ref: a found group's
// group/auto-<slug>, or an auto/<model> spelt otherwise than it is listed
// now (auto/glm-5-3-flash for auto/glm-5.3-flash), as its auto/<model>
// (Group.Ref), its effort and [1m] mark kept; any other ref as it is, a
// found group magpie has none of now too.
func PublicRef(ref string) string {
	s := strings.TrimSpace(ref)
	if !strings.HasPrefix(s, AutoPrefix) && !strings.HasPrefix(s, GroupPrefix+"auto-") {
		return ref
	}
	if !autoRefsOn() {
		return ref
	}
	return publicRef(ref, func(gid string) string {
		g, _ := groupOf(sharedGroups(), gid)
		return g.Spelled
	})
}

// PublicRefs snapshots the found groups' public spellings for a whole usage
// read. The returned mapper reads no files or shared catalog; nil means the
// public namespace is inactive or no found group has a public spelling.
func PublicRefs() func(string) string {
	groups := usageGroupNames()
	if len(groups) == 0 {
		return nil
	}
	return func(ref string) string {
		return publicRef(ref, func(gid string) string { return groups[gid] })
	}
}

// publicRef uses a group's spelling read by the caller. Usage takes a snapshot
// before its locks, so naming each row never reads providers or settings.
func publicRef(ref string, spelling func(string) string) string {
	s := strings.TrimSpace(ref)
	if strings.HasPrefix(s, AutoPrefix) {
		s = canonicalRef(s, true)
	}
	s, marked := strings.CutSuffix(s, "[1m]")
	gid, ok := strings.CutPrefix(s, GroupPrefix)
	if !ok || !strings.HasPrefix(gid, "auto-") {
		return ref
	}
	level := ""
	if i := strings.LastIndex(gid, ":"); i > 0 {
		gid, level = gid[:i], gid[i:]
	}
	model := spelling(gid)
	if model == "" {
		return ref
	}
	out := AutoPrefix + model + level
	if marked {
		out += "[1m]"
	}
	return out
}

// spelledIn is how the model of a found group's id is spelt by a vendor
// (Group.Spelled): the spelling models.dev gives the model most
// (catalog.Spelling: glm-5.3-flash whether the members serve glm-5-3-flash
// or glm-5.3-flash, XYenon on Discord), so the id doesn't follow the
// order the providers were added in, and the group's name is the one
// models.dev gives that spelling (autoGroups). Failing that, of the
// members whose model merges into the group by its own id (mergeKey), the
// first models.dev lists by that very spelling, else the first; failing
// those, the model the user said a member is the same as (settings'
// ModelSameAs). Without a vendor's prefix ("anthropic/"). "" for a group
// whose id is not auto-…, or none of whose members is its model: one the
// user made, whatever it is called, stays group/<id>. Every spelling it
// had before is still the group (CanonicalRef).
func spelledIn(g Group, same map[string]string) string {
	if !strings.HasPrefix(g.ID, "auto-") {
		return ""
	}
	var own, alike []string
	for _, id := range g.Members {
		if strings.HasPrefix(id, GroupPrefix) || IsPattern(id) {
			continue
		}
		model, _ := MemberEffort(id)
		pid, m, ok := strings.Cut(model, "/")
		if !ok || m == "" {
			continue
		}
		if v := same[pid+"/"+m]; v != "" {
			if "auto-"+Slug(sameModel(v)) == g.ID {
				alike = append(alike, lastPart(v))
			}
			continue
		}
		if "auto-"+Slug(sameModel(m)) == g.ID {
			own = append(own, lastPart(m))
		}
	}
	// an id a relay lists as its display name ("GPT-6 Sol") is no id to
	// put in an agent's config: the one vendors agree on is, below
	spaced := func(s string) bool { return strings.ContainsFunc(s, unicode.IsSpace) }
	for _, m := range slices.Concat(own, alike) {
		if s := catalog.Spelling(m); s != "" && !spaced(s) && "auto-"+Slug(sameModel(s)) == g.ID {
			return s
		}
	}
	spacedOnly := len(own)+len(alike) > 0
	own, alike = slices.DeleteFunc(own, spaced), slices.DeleteFunc(alike, spaced)
	for _, s := range own {
		if catalog.Lists(s) {
			return s
		}
	}
	if len(own) > 0 {
		return own[0]
	}
	if len(alike) > 0 {
		return alike[0]
	}
	if spacedOnly {
		return strings.TrimPrefix(g.ID, "auto-")
	}
	return ""
}

// lastPart is a model's id without a vendor's prefix: what follows its
// last slash, trimmed.
func lastPart(m string) string {
	m = strings.TrimSpace(m)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	return m
}

package provider

// A routing group may name its members by pattern as well as by id (#766):
// "openrouter/*:free" is every free model OpenRouter lists, today's and
// next week's, where a list of ids goes stale as the vendor's catalog
// changes and a member left out is lost unsaid.
//
// The patterns are kept beside the members (Group.Match), never written
// into them: what they match is worked out each time the groups are read
// (groupsIn), from the models magpie serves now, so a model the vendor adds
// joins the group and one it drops leaves it without the group being saved
// again. Every reader — the gateway's routing, /v1/models, rules, the
// Routing view, the TUI and the CLI — gets the group from there, so all of
// them see the same members: those the user named, in their order, then
// those the patterns match, in the catalog's order. A model named and
// matched both keeps the place it was named at.

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// RegexpPrefix starts a member that is a regular expression over the whole
// "provider/model" id: "re:openrouter/.*:(free|beta)".
const RegexpPrefix = "re:"

// IsPattern reports whether a member as typed is a pattern rather than a
// model's id: a regular expression ("re:…") or a glob with "*" in it. No
// vendor's model id has a "*" in it, so a glob can't be taken for one.
func IsPattern(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, RegexpPrefix) || strings.Contains(s, "*")
}

// compilePattern is a pattern as the regexp it means. A glob's "*" is any
// run of characters, "/" too, and the rest is the id as spelt, in any case
// ("openrouter/*:free", "*-free"); a regexp must match the whole id.
func compilePattern(p string) (*regexp.Regexp, error) {
	if re, ok := strings.CutPrefix(p, RegexpPrefix); ok {
		if strings.TrimSpace(re) == "" {
			return nil, fmt.Errorf("%q has no regular expression after %s", p, RegexpPrefix)
		}
		x, err := regexp.Compile("^(?:" + re + ")$")
		if err != nil {
			return nil, fmt.Errorf("%q is not a regular expression magpie can read: %v", p, err)
		}
		return x, nil
	}
	parts := strings.Split(p, "*")
	for i, s := range parts {
		parts[i] = regexp.QuoteMeta(s)
	}
	return regexp.Compile("(?i)^" + strings.Join(parts, ".*") + "$")
}

// CleanPatterns are a group's patterns as they are kept: trimmed, each
// once, every one one magpie can read.
func CleanPatterns(in []string) ([]string, error) {
	var out []string
	for _, p := range in {
		p = strings.TrimPrefix(strings.TrimSpace(p), "magpie/")
		if p == "" || slices.Contains(out, p) {
			continue
		}
		if !IsPattern(p) {
			return nil, fmt.Errorf("%q is a model's id, not a pattern: a pattern has a * in it, or starts with %s", p, RegexpPrefix)
		}
		if _, err := compilePattern(p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// PatternMatches reports whether a pattern matches a "provider/model" id.
func PatternMatches(pattern, id string) bool {
	x, err := compilePattern(pattern)
	return err == nil && x.MatchString(id)
}

// matchesIn are the models g's patterns match among entries — the models
// served now, never a group — in the catalog's order, but those the group
// names itself: a model named at an effort of its own is not matched again
// at the group's.
func matchesIn(entries []Entry, g Group) []string {
	if len(g.Match) == 0 {
		return nil
	}
	var res []*regexp.Regexp
	for _, p := range g.Match {
		if x, err := compilePattern(p); err == nil {
			res = append(res, x)
		}
	}
	named := map[string]bool{}
	for _, id := range g.Members {
		m, _ := memberEffortIn(entries, id)
		named[m] = true
	}
	var out []string
	for _, e := range entries {
		if named[e.ID] || slices.Contains(out, e.ID) {
			continue
		}
		if slices.ContainsFunc(res, func(x *regexp.Regexp) bool { return x.MatchString(e.ID) }) {
			out = append(out, e.ID)
		}
	}
	return out
}

// withMatches is a stored group as it is read: the members its patterns
// match now after those it names (Matched says which they are).
func withMatches(entries []Entry, g Group) Group {
	if len(g.Match) == 0 {
		return g
	}
	g.Matched = matchesIn(entries, g)
	g.Members = append(slices.Clone(g.Members), g.Matched...)
	return g
}

// ownMembers are the members a group names itself and its patterns, from a
// group as it was read: the members its patterns matched (Matched) are
// left out, so that saving a group never writes today's matches into it,
// and a pattern typed among the members is taken as one.
func ownMembers(g Group) (members, match []string) {
	match = slices.Clone(g.Match)
	for _, m := range g.Members {
		switch m = strings.TrimSpace(m); {
		case slices.Contains(g.Matched, m):
		case IsPattern(m):
			match = append(match, m)
		default:
			members = append(members, m)
		}
	}
	return members, match
}

// stored is the group as providers.json keeps it: without what its
// patterns matched when it was read.
func (g Group) stored() Group {
	if len(g.Matched) == 0 {
		return g
	}
	g.Members = slices.DeleteFunc(slices.Clone(g.Members), func(m string) bool { return slices.Contains(g.Matched, m) })
	g.Matched = nil
	return g
}

// PatternHit is one of a group's patterns and how many of the models
// served now it matches.
type PatternHit struct {
	Pattern string `json:"pattern"`
	Models  int    `json:"models"`
}

// PatternHits are the group's patterns, each with the number of models it
// matches now — those the group names itself as well — so that one that
// matches nothing is said, not left an empty group.
func PatternHits(g Group) []PatternHit {
	if len(g.Match) == 0 {
		return nil
	}
	entries := providerEntries()
	out := make([]PatternHit, 0, len(g.Match))
	for _, p := range g.Match {
		h := PatternHit{Pattern: p}
		if x, err := compilePattern(p); err == nil {
			for _, e := range entries {
				if x.MatchString(e.ID) {
					h.Models++
				}
			}
		}
		out = append(out, h)
	}
	return out
}

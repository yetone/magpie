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
// those the patterns match, in the catalog's order, or in the order the
// user put them in (Group.Placed). A model named and matched both keeps
// the place it was named at.

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
)

// RegexpPrefix starts a member that is a regular expression over the whole
// "provider/model" id, in any case as a glob is: "re:openrouter/.*:(free|beta)".
// "(?-i)" in it makes case matter from there on: "re:(?-i)openrouter/.*:FREE".
const RegexpPrefix = "re:"

// IsPattern reports whether a member as typed is a pattern rather than a
// model's id: a regular expression ("re:…") or a glob with "*" in it. No
// vendor's model id has a "*" in it, so a glob can't be taken for one.
func IsPattern(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, RegexpPrefix) || strings.Contains(s, "*")
}

// matcher is a compiled pattern.
type matcher interface{ MatchString(string) bool }

// patternTimeout bounds one backtracking match over one id: a pattern that
// takes longer matches nothing rather than holding the catalog's read.
const patternTimeout = 100 * time.Millisecond

// backtracking is a pattern Go's regexp can't read, read by regexp2.
type backtracking struct{ x *regexp2.Regexp }

func (b backtracking) MatchString(s string) bool {
	ok, err := b.x.MatchString(s)
	return err == nil && ok
}

// compilePattern is a pattern as the regexp it means. A glob's "*" is any
// run of characters, "/" too, and the rest is the id as spelt, in any case
// ("openrouter/*:free", "*-free"); a regexp must match the whole id, in any
// case as a glob does unless it says "(?-i)" (usrname1833 on Discord).
func compilePattern(p string) (matcher, error) {
	if re, ok := strings.CutPrefix(p, RegexpPrefix); ok {
		if strings.TrimSpace(re) == "" {
			return nil, fmt.Errorf("%q has no regular expression after %s", p, RegexpPrefix)
		}
		x, err := regexp.Compile("(?i)^(?:" + re + ")$")
		if err == nil {
			return x, nil
		}
		// What Go's regexp can't read, a lookahead most often (maer8824 on
		// Discord: "re:(?!.*flash).*glm.*", glm but not flash), is read as
		// the GUI's preview reads it, with backtracking: a pattern Go reads
		// is never read this way, so its matches stay what they were.
		y, err2 := regexp2.Compile("(?i)^(?:"+re+")\\z", regexp2.None)
		if err2 != nil {
			return nil, fmt.Errorf("%q is not a regular expression magpie can read: %v", p, err)
		}
		y.MatchTimeout = patternTimeout
		return backtracking{y}, nil
	}
	parts := strings.Split(p, "*")
	for i, s := range parts {
		parts[i] = regexp.QuoteMeta(s)
	}
	return regexp.MustCompile("(?i)^" + strings.Join(parts, ".*") + "$"), nil
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
	var res []matcher
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
		if slices.ContainsFunc(res, func(x matcher) bool { return x.MatchString(e.ID) }) {
			out = append(out, e.ID)
		}
	}
	return out
}

// withMatches is a stored group as it is read: the members its patterns
// match now after those it names (Matched says which they are), or in
// the order the user put them in (Placed).
func withMatches(entries []Entry, g Group) Group {
	if len(g.Match) == 0 {
		return g
	}
	g.Matched = matchesIn(entries, g)
	g.Members = placedIn(append(slices.Clone(g.Members), g.Matched...), g.Placed)
	return g
}

// placedIn are members in the order placed has them: those it names
// first, as it has them, then the rest in their own order — a model a
// pattern matches since it was placed, at the end. One placed that isn't
// a member now is left out.
func placedIn(members, placed []string) []string {
	if len(placed) == 0 {
		return members
	}
	in := make(map[string]bool, len(members))
	for _, m := range members {
		in[m] = true
	}
	out := make([]string, 0, len(members))
	for _, p := range placed {
		if in[p] {
			out = append(out, p)
			delete(in, p)
		}
	}
	for _, m := range members {
		if in[m] {
			out = append(out, m)
			delete(in, m)
		}
	}
	return out
}

// placedOver is the Placed a group is saved with: its members (named
// first, then the matched) in the order placed before (the caller's, or
// the one stored), with those the caller names (given) put in the places
// those have, in the caller's order. So the named are reordered as the
// caller says and the pattern's stay where they were placed; a model
// named since goes after the last one named, one matched since last.
// nil when that is the members' own order.
func placedOver(members, before, given []string) []string {
	if len(before) == 0 {
		return nil
	}
	sent := map[string]bool{}
	var order []string
	for _, m := range given {
		if !sent[m] && slices.Contains(members, m) {
			sent[m] = true
			order = append(order, m)
		}
	}
	var seq []string
	for _, m := range before {
		if slices.Contains(members, m) && !slices.Contains(seq, m) {
			seq = append(seq, m)
		}
	}
	for _, m := range order {
		if !slices.Contains(seq, m) {
			at := len(seq)
			for at > 0 && !sent[seq[at-1]] {
				at--
			}
			seq = slices.Insert(seq, at, m)
		}
	}
	seq = placedIn(members, seq)
	for i, j := 0, 0; i < len(seq); i++ {
		if sent[seq[i]] {
			seq[i] = order[j]
			j++
		}
	}
	if slices.Equal(seq, members) {
		return nil
	}
	return seq
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

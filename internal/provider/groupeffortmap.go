package provider

// A group's member may send another level than the one asked for
// (Group.EffortMaps, LuckymanJC on Discord): a group relaying a task
// between vendors' models finds one vendor's "high" is another's
// "xhigh", so the member that reasons less at the same word is sent a
// level up. The map goes from the level asked — by the agent, the
// model's name, or the group's classifier — to the level sent; a level
// it doesn't name is sent as asked. It is kept beside Members, as
// Weights is, so a group without one reads and routes as before, and a
// member fixed at an effort (or following the agent among several) has
// none: its own effort already says what it is sent.

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// MemberEffortMap is the levels the group's member id sends in place of
// the ones asked for; nil when it sends them as asked.
func (g Group) MemberEffortMap(id string) map[string]string { return g.EffortMaps[id] }

// SetMemberEffortMap has the group's member id send to[l] when l is
// asked; an empty map sends every level as asked.
func (g *Group) SetMemberEffortMap(id string, to map[string]string) {
	g.EffortMaps = renamedEffortMaps(g.EffortMaps, func(m string) string {
		if m == id {
			return ""
		}
		return m
	})
	if to = cleanEffortMap(to); len(to) > 0 {
		if g.EffortMaps == nil {
			g.EffortMaps = map[string]map[string]string{}
		}
		g.EffortMaps[id] = to
	}
}

// renamedEffortMaps is maps with each member as to spells it now, those
// it gives "" dropped; nil when none is left. Two members spelled as one
// keep the map of the one already spelled so, else the first's in the
// order of their spelling: a group shared from a magpie with OpenAI's
// model on two providers, imported where one serves both, keeps that
// one's own.
func renamedEffortMaps(m map[string]map[string]string, to func(string) string) map[string]map[string]string {
	var out map[string]map[string]string
	keys := slices.Sorted(maps.Keys(m))
	for _, same := range []bool{true, false} {
		for _, k := range keys {
			n := to(k)
			if n == "" || len(m[k]) == 0 || (n == k) != same {
				continue
			}
			if _, ok := out[n]; ok {
				continue
			}
			if out == nil {
				out = map[string]map[string]string{}
			}
			out[n] = maps.Clone(m[k])
		}
	}
	return out
}

// cleanEffortMap is to with its levels lowercase, the ones sent as asked
// left out; nil when none is left. A word that isn't a level is kept for
// checkEffortMap to refuse.
func cleanEffortMap(to map[string]string) map[string]string {
	var out map[string]string
	for from, l := range to {
		from, l = strings.ToLower(strings.TrimSpace(from)), strings.ToLower(strings.TrimSpace(l))
		if from == "" || l == "" || from == l {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[from] = l
	}
	return out
}

// checkEffortMap refuses a map naming a word that isn't a level.
func checkEffortMap(id string, to map[string]string) error {
	for from, l := range to {
		for _, w := range []string{from, l} {
			if !slices.Contains(MemberEfforts, w) {
				return fmt.Errorf("%s maps %s to %s: a reasoning level is one of %s", id, from, l, strings.Join(MemberEfforts, ", "))
			}
		}
	}
	return nil
}

// cleanEffortMaps keeps g.EffortMaps to its members as Members spells
// them that follow the group's effort: one fixed at an effort, or among
// several, sends that, so a map on it is left out. A level that isn't
// one is refused.
func cleanEffortMaps(entries []Entry, g *Group) error {
	var err error
	g.EffortMaps = renamedEffortMaps(g.EffortMaps, func(m string) string {
		id, _ := memberFastIn(entries, strings.TrimSpace(m))
		id = cleanMember(entries, id)
		to := cleanEffortMap(g.EffortMaps[m])
		if e := checkEffortMap(id, to); e != nil && err == nil {
			err = e
		}
		if _, effort := memberEffortIn(entries, id); effort != "" || len(to) == 0 || strings.HasPrefix(id, GroupPrefix) || !slices.Contains(g.Members, id) {
			return ""
		}
		return id
	})
	for id, to := range g.EffortMaps {
		g.EffortMaps[id] = cleanEffortMap(to)
	}
	return err
}

// mappedLevels are a model's efforts with the levels its member's map is
// asked for besides, in MemberEfforts' order, the model's own words that
// aren't one ("ultra") after them as it lists them.
func mappedLevels(efforts []string, to map[string]string) []string {
	var out []string
	for _, l := range MemberEfforts {
		if _, asked := to[l]; asked || slices.Contains(efforts, l) {
			out = append(out, l)
		}
	}
	for _, l := range efforts {
		if !slices.Contains(MemberEfforts, l) {
			out = append(out, l)
		}
	}
	return out
}

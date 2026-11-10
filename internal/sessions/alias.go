package sessions

import (
	"slices"
	"sort"
)

// A session's models are counted under the names the user gave them
// (provider.UsageAlias, `magpie model alias`): two ids of one model are one
// row. Each was priced by its own id before; the merged row adds those
// costs and is priced only when every id in it was. Changed rows use new
// slices, so what List and Stats keep for the next read is never changed.

// aliasModels is ms with the models alias names alike made one, the most
// used first as assembleListed sorts them.
func aliasModels(ms []Model, alias func(string) string) []Model {
	if !slices.ContainsFunc(ms, func(m Model) bool { return alias(m.Model) != m.Model }) {
		return ms
	}
	out := make([]Model, 0, len(ms))
	at := make(map[string]int, len(ms))
	for _, m := range ms {
		name := alias(m.Model)
		i, ok := at[name]
		if !ok {
			at[name] = len(out)
			m.Model = name
			out = append(out, m)
			continue
		}
		o := &out[i]
		o.Tokens.add(m.Tokens)
		o.Cost += m.Cost
		o.Priced = o.Priced && m.Priced
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Input+a.Output != b.Input+b.Output {
			return a.Input+a.Output > b.Input+b.Output
		}
		return a.Model < b.Model
	})
	return out
}

// aliasSession counts s's models under their aliases. When rows merged,
// Unpriced is the merged rows without a price, as it counted them before.
func aliasSession(s *Session, alias func(string) string) {
	if len(s.Models) == 0 {
		return
	}
	n := len(s.Models)
	s.Models = aliasModels(s.Models, alias)
	if len(s.Models) < n {
		s.Unpriced = 0
		for _, m := range s.Models {
			if !m.Priced {
				s.Unpriced++
			}
		}
	}
	s.ModelEffort = aliasEffort(s.ModelEffort, alias)
}

// aliasManaged names the management page's sessions after their own pricing.
// Its mapper is read before the session lock, like List's and Stats'.
func aliasManaged(ms []Managed, alias func(string) string) {
	if alias != nil {
		for i := range ms {
			aliasSession(&ms[i].Session, alias)
		}
	}
}

// aliasStats is st with each day's usage and each session's models counted
// under their aliases.
func aliasStats(st Stats, alias func(string) string) Stats {
	changed := slices.ContainsFunc(st.Days, func(d Day) bool {
		return slices.ContainsFunc(d.Usage, func(u Usage) bool { return alias(u.Model) != u.Model })
	}) || slices.ContainsFunc(st.Sessions, func(s Summary) bool {
		return slices.ContainsFunc(s.Models, func(m string) bool { return alias(m) != m }) ||
			slices.ContainsFunc(s.ModelEffort, func(e ModelEffort) bool { return alias(e.Model) != e.Model })
	})
	if !changed {
		return st
	}
	days := make([]Day, len(st.Days))
	for i, d := range st.Days {
		d.Usage = aliasUsage(d.Usage, alias)
		days[i] = d
	}
	st.Days = days
	sums := make([]Summary, len(st.Sessions))
	for i, s := range st.Sessions {
		s.Models = aliasNames(s.Models, alias)
		s.ModelEffort = aliasEffort(s.ModelEffort, alias)
		sums[i] = s
	}
	st.Sessions = sums
	return st
}

// aliasUsage is a day's usage with the rows of one agent, folder and
// aliased model made one, in the order they first came.
func aliasUsage(us []Usage, alias func(string) string) []Usage {
	if !slices.ContainsFunc(us, func(u Usage) bool { return alias(u.Model) != u.Model }) {
		return us
	}
	type key struct{ agent, cwd, model string }
	out := make([]Usage, 0, len(us))
	at := make(map[key]int, len(us))
	for _, u := range us {
		u.Model = alias(u.Model)
		k := key{u.Agent, u.Cwd, u.Model}
		i, ok := at[k]
		if !ok {
			at[k] = len(out)
			out = append(out, u)
			continue
		}
		o := &out[i]
		o.Tokens.add(u.Tokens)
		o.Cost += u.Cost
		o.Priced = o.Priced && u.Priced
	}
	return out
}

// aliasNames is names under their aliases, each once, in the order they
// first came (the most used first stays first).
func aliasNames(names []string, alias func(string) string) []string {
	if !slices.ContainsFunc(names, func(n string) bool { return alias(n) != n }) {
		return names
	}
	out := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		n = alias(n)
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// aliasEffort is a session's model and effort rows with those of one
// aliased model and effort made one, in the order they first came.
func aliasEffort(es []ModelEffort, alias func(string) string) []ModelEffort {
	if !slices.ContainsFunc(es, func(e ModelEffort) bool { return alias(e.Model) != e.Model }) {
		return es
	}
	type key struct{ model, effort string }
	out := make([]ModelEffort, 0, len(es))
	at := make(map[key]int, len(es))
	for _, e := range es {
		e.Model = alias(e.Model)
		k := key{e.Model, e.Effort}
		i, ok := at[k]
		if !ok {
			at[k] = len(out)
			out = append(out, e)
			continue
		}
		o := &out[i]
		o.Tokens.add(e.Tokens)
		o.Reasoning += e.Reasoning
		o.Cost += e.Cost
		o.Priced = o.Priced && e.Priced
	}
	return out
}

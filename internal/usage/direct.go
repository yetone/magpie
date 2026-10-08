package usage

import (
	"sort"
	"time"
)

// Direct is a period's calls the agents made on their own, which the gateway
// never saw: those their session files record (Codex, Claude Code, Claude
// Desktop…), summed up as Summarize sums the gateway's, by agent, model and
// account. The Usage page's Requests tab and the tray panel count them
// beside the gateway's; magpie usage and the TUI show them in a part of
// their own (Kumo31 on Discord: Codex used outside magpie was on the
// window's Usage tab but not in magpie usage).
func Direct(p Period) Summary {
	now := Clock()
	return direct(p, now, LedgerOfAt(p, Filter{}, now).Rows)
}

// Summaries is Summarize(p) and Direct(p) asked at one moment, for a page
// that shows the two together: asked one after the other, midnight could
// fall between them and set one day's calls through magpie beside the next
// day's calls not through it.
func Summaries(p Period) (Summary, Summary) {
	now := Clock()
	return indexedSummary(p, now), direct(p, now, LedgerOfAt(p, Filter{}, now).Rows)
}

func direct(p Period, now time.Time, rows []Row) Summary {
	s := Summary{Period: p, Since: p.Since(now), Bucket: "day", Agents: []Group{}, Models: []Group{}, ProviderKeys: []Group{}, Accounts: []Group{}, CallerKeys: []Group{}, Sessions: []Group{}, Series: []Point{}}
	if p != Today && p != Week && p != Month {
		s.Period = All
	}
	agents, models, accounts, sessions := map[string]*Group{}, map[string]*Group{}, map[string]*Group{}, map[string]*Group{}
	put := func(m map[string]*Group, k string, g Group, r Row) {
		if m[k] == nil {
			m[k] = &g
		}
		m[k].addRow(r)
	}
	for _, r := range rows {
		if r.Source != "log" || r.IsRejected() || r.Time.Before(s.Since) {
			continue
		}
		s.Totals.addRow(r)
		id := AgentOf(r.Agent)
		put(agents, id, Group{ID: id}, r)
		put(models, r.Provider+"/"+r.Model, Group{ID: r.Provider + "/" + r.Model, Provider: r.Provider, Model: r.Model}, r)
		if who := r.Account(); who != "" {
			put(accounts, r.Provider+"@"+who, Group{ID: r.Provider + "@" + who, Provider: r.Provider, Account: who}, r)
		}
		if r.Session != "" {
			put(sessions, id+"|"+r.Session, Group{ID: r.Session, Agent: id}, r)
		}
	}
	list := func(m map[string]*Group) []Group {
		gs := []Group{}
		for _, g := range m {
			gs = append(gs, *g)
		}
		sort.SliceStable(gs, func(i, j int) bool {
			if a, b := gs[i].Tokens(), gs[j].Tokens(); a != b {
				return a > b
			}
			if gs[i].Calls != gs[j].Calls {
				return gs[i].Calls > gs[j].Calls
			}
			return gs[i].ID < gs[j].ID
		})
		return gs
	}
	s.Agents, s.Models, s.Accounts, s.Sessions = list(agents), list(models), list(accounts), list(sessions)
	return s
}

package usage

import (
	"sort"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
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
	return direct(p, now, directRows(p, now))
}

// Summaries is Summarize(p) and Direct(p) asked at one moment, for a page
// that shows the two together: asked one after the other, midnight could
// fall between them and set one day's calls through magpie beside the next
// day's calls not through it. The gateway's sessions are its top
// summaryCacheSessions (SummarizeTop), of its SessionCount: a page lists a
// few, and the summary of a long history is then kept rather than worked
// out again.
func Summaries(p Period) (Summary, Summary) {
	now := Clock()
	return topSummary(p, now, summaryCacheSessions), direct(p, now, directRows(p, now))
}

// directRows are the rows of LedgerOfAt(p, Filter{}, now) that Direct sums,
// the calls the agents' session files record that the gateway did not see,
// worked out without the rest: the gateway's own calls are only matched
// against them, from the blocks of the ledger's index as they are, not each
// made into a priced row. Over a long history (Vantiboo1ean, ~750k
// requests) those were most of the TUI's and magpie usage's wait.
func directRows(p Period, now time.Time) []Row {
	since, until := p.Since(now), p.Until(now)
	reader := LogCalls
	if reader == nil {
		reader = sessions.Calls
	}
	logs, others := ownCalls(reader(since)), sharedRecords(since)
	matched := map[int]bool{}
	actors := map[int]string{}
	_, _, compactions := duplicateCodexCalls(logs)
	if len(logs) > 0 {
		local := &rowChunk{}
		for i, c := range logs {
			r := logRecord(c)
			r.Agent = c.Agent
			local.add(Row{Record: r, Source: "log", codexActor: codexActor(c)}, c.Msg, int64(i), c.Error != "")
		}
		var gateways []*rowChunk
		for _, b := range readLogSnapshot().blocks {
			if !since.IsZero() && b.Latest.Before(since.Add(-24*time.Hour)) {
				continue
			}
			if c := b.unpack(); c != nil {
				gateways = append(gateways, c)
			}
		}
		duplicates := foldCodexRows([]*rowChunk{local})
		pairs, _ := codexChunkResponsePairs(gateways, []*rowChunk{local}, duplicates)
		for ref := range matchedBlocks(gateways, []*rowChunk{local}, duplicates, pairs, since, false).all() {
			matched[ref.Index] = true
		}
		for ref := range duplicates.all() {
			matched[ref.Index] = true
		}
		for i, call := range logs {
			if i < local.codexActors.len() && local.codexActors.at(i) != call.ThreadID {
				actors[i] = local.codexActors.at(i)
			}
		}
	}
	rows, _, _, _ := ledgerMatched(since, until, Filter{}, nil, logs, others, matched, nil, actors, compactions)
	return rows
}

func direct(p Period, now time.Time, rows []Row) Summary {
	s := Summary{Period: p, Since: p.Since(now), Bucket: "day", Agents: []Group{}, Models: []Group{}, ProviderKeys: []Group{}, Accounts: []Group{}, CallerKeys: []Group{}, Sessions: []Group{}, Series: []Point{}}
	s.Period = p.shown()
	until := p.Until(now)
	agents, models, accounts, sessions := map[string]*Group{}, map[string]*Group{}, map[string]*Group{}, map[string]*Group{}
	put := func(m map[string]*Group, k string, g Group, r Row) {
		if m[k] == nil {
			m[k] = &g
		}
		m[k].addRow(r)
	}
	alias := provider.UsageAlias() // each row priced by its own id already
	for _, r := range rows {
		if r.Source != "log" || r.IsRejected() || r.Time.Before(s.Since) || after(until, r.Time) {
			continue
		}
		s.Totals.addRow(r)
		id := ReportedAs(r.Agent)
		put(agents, id, Group{ID: id}, r)
		model := r.Model
		if alias != nil {
			model = alias(model)
		}
		put(models, r.Provider+"/"+model, Group{ID: r.Provider + "/" + model, Provider: r.Provider, Model: model}, r)
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
	s.SessionCount = len(s.Sessions)
	return s
}

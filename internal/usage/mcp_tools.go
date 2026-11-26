package usage

import (
	"cmp"
	"slices"
	"time"
)

// ToolCalls is one MCP tool's calls among the rows a page's filter keeps:
// how many, how many failed, and how often (tomtanir and lyl50478600 on
// X: the request log listed each call, but not how many times, or how
// often, each tool was called).
type ToolCalls struct {
	Server string    `json:"server"`
	Tool   string    `json:"tool"`
	Calls  int       `json:"calls"`
	Failed int       `json:"failed,omitempty"`
	Last   time.Time `json:"last"`
	// PerDay is Calls over the days from the tool's first call in the
	// period to the period's end (now for one that hasn't ended), one at
	// least; over the one day a page of a day is of
	PerDay float64 `json:"per_day"`
	first  time.Time
}

// mcpTally adds up MCP tool calls by server and tool.
type mcpTally map[[2]string]*ToolCalls

func (m mcpTally) add(r *Row) {
	k := [2]string{r.Host, r.Requested}
	c := m[k]
	if c == nil {
		c = &ToolCalls{Server: r.Host, Tool: r.Requested, first: r.Time, Last: r.Time}
		m[k] = c
	}
	c.Calls++
	if r.Error != "" {
		c.Failed++
	}
	if r.Time.Before(c.first) {
		c.first = r.Time
	}
	if r.Time.After(c.Last) {
		c.Last = r.Time
	}
}

// list is the tools, the most called first, each's PerDay of the days
// from its first call to end, or of one day for a page of a day.
func (m mcpTally) list(end time.Time, day bool) []ToolCalls {
	out := make([]ToolCalls, 0, len(m))
	for _, c := range m {
		days := 1.0
		if !day {
			days = max(1, end.Sub(c.first).Hours()/24)
		}
		c.PerDay = float64(c.Calls) / days
		out = append(out, *c)
	}
	slices.SortFunc(out, func(a, b ToolCalls) int {
		return cmp.Or(cmp.Compare(b.Calls, a.Calls), cmp.Compare(a.Server, b.Server), cmp.Compare(a.Tool, b.Tool))
	})
	return out
}

// picksMCP is whether f lists only MCP tool calls: only then does a page
// count its tools, so no other page reads the calls again.
func (f Filter) picksMCP() bool {
	return f.Purpose != "" && f.Purpose == PurposeOf(KindMCP)
}

// mcpTools is the MCP tool calls the filter keeps of chunks from since
// to until (zero: now), by tool; nil unless f picks them (picksMCP). The
// calls are in the probes' chunk alone (probeChunk): a call is made
// on this computer and never synced, and made no chunk of its own, so
// only chunks without a serial, which are unpacked, are read.
func mcpTools(f Filter, since, until, now time.Time, sets ...[]*rowChunk) []ToolCalls {
	if !f.picksMCP() {
		return nil
	}
	m := mcpTally{}
	for _, set := range sets {
		for _, c := range set {
			if c == nil || c.serial != 0 {
				continue
			}
			for i := range c.Rows {
				t := c.Rows[i].Time()
				if t.Before(since) || !until.IsZero() && !t.Before(until) {
					continue
				}
				r := c.row(i)
				if r.Kind == KindMCP && f.keepsRow(&r) {
					m.add(&r)
				}
			}
		}
	}
	return m.list(toolsEnd(until, now), f.Day != "")
}

// toolsEnd is when a period ending at until (zero: not ended) ends, read
// at now.
func toolsEnd(until, now time.Time) time.Time {
	if !until.IsZero() && until.Before(now) {
		return until
	}
	return now
}

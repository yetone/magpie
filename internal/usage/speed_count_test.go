package usage

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// A group's speed says how many replies it is of, and a group none of whose
// replies was measured says the least they wrote at (Fallmonkey on Discord:
// gpt-6.1-sol showed a tok/s from the one reply of 260 that called no tool,
// beside "260 requests", while Claude Code, with dozens of floors on the
// Requests tab, showed none). Breakdown and the sums count the measured
// replies (DecodeN) and the floors (FloorOut over FloorMs, FloorN) apart.
func TestGroupSpeedSaysHowManyRepliesItIsOf(t *testing.T) {
	at := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	var rows []Row
	// sol: 260 Codex calls read from its session files; only a turn of one
	// call is timed, 500 tokens over the 4 s after the first, 125 tok/s; the
	// rest, calls in turns of several, wrote 1,500 tokens over 30 s each, a
	// floor of 50 tok/s
	for i := 0; i < 260; i++ {
		r := Row{Source: "log", Record: Record{Time: at, Agent: "codex", Model: "gpt-6.1-sol", Input: 9000, Output: 1500, Millis: 30000}}
		if i == 0 {
			r.Output, r.Millis, r.TTFT = 500, 5000, 1000
		}
		rows = append(rows, r)
	}
	// Claude Code: 40 calls from its session file, none timed; 30 wrote
	// 2,000 tokens over 20 s (a floor of 100 tok/s), 10 are too short to tell
	for i := 0; i < 40; i++ {
		r := Row{Source: "log", Record: Record{Time: at, Agent: "claude", Model: "claude-opus-5-5", Input: 5000, Output: 2000, Millis: 20000}}
		if i >= 30 {
			r.Output = 300
		}
		rows = append(rows, r)
	}
	// a failed Claude Code call and one through the gateway tell no floor
	rows = append(rows,
		Row{Source: "log", Record: Record{Time: at, Agent: "claude", Model: "claude-opus-5-5", Output: 2000, Millis: 20000, Error: "overloaded"}},
		Row{Record: Record{Time: at, Agent: "claude", Model: "claude-opus-5-5", Output: 2000, Millis: 20000, Status: 200}},
	)
	by := map[string]Share{}
	for _, s := range Breakdown(rows, "agent") {
		by[s.ID] = s
	}
	sol := by["codex"]
	if sol.Calls != 260 || sol.DecodeN != 1 || sol.Speed() != 125 || sol.FloorN != 259 || sol.Floor() != 50 {
		t.Fatalf("sol: %d calls, %d measured at %v tok/s, %d floors at %v; want 260, 1 at 125, 259 at 50", sol.Calls, sol.DecodeN, sol.Speed(), sol.FloorN, sol.Floor())
	}
	cc := by["claude"]
	if cc.Calls != 42 || cc.DecodeN != 0 || cc.Speed() != 0 || cc.FloorN != 30 || cc.FloorOut != 60000 || cc.FloorMs != 600000 || cc.Floor() != 100 {
		t.Fatalf("Claude Code: %d calls, %d measured at %v, %d floors %d/%d ms at %v; want 42, none, 30 at 100", cc.Calls, cc.DecodeN, cc.Speed(), cc.FloorN, cc.FloorOut, cc.FloorMs, cc.Floor())
	}
	// the page's own sums count both the same way
	var sum Totals
	for _, r := range rows {
		sum.addRow(r)
	}
	if sum.DecodeN != 1 || sum.FloorN != 289 || sum.Speed() != 125 {
		t.Fatalf("sum: %+v", sum)
	}
	// a timed Claude Code row tells its own speed, not a floor
	timed := Row{Source: "log", Record: Record{Agent: "claude", Output: 2000, Millis: 20000, TTFT: 4000}}
	if n, ms := timed.Floor(); ms != 0 || n != 0 {
		t.Fatalf("timed row's floor %d/%d", n, ms)
	}
	// records through the gateway count their measured replies too
	recs := []Record{
		{Time: at, Provider: "openai", Model: "gpt-6.1-sol", Output: 1200, Millis: 15000, TTFT: 3000, Status: 200},
		{Time: at, Provider: "openai", Model: "gpt-6.1-sol", Output: 1200, Millis: 15000, Status: 200},
	}
	m := summarize(Today, at.Add(time.Hour), recs).Models[0]
	if m.Calls != 2 || m.DecodeN != 1 || m.Speed() != 100 {
		t.Fatalf("summarized: %+v", m.Totals)
	}
}

// Codex rows tell a floor too (Fallmonkey on Discord): a call in a turn of
// several, timed from what asked for it, by the same rule as Claude Code's;
// a turn of one call that reasoned, whose first text isn't in the file, by
// its answer over the time after its first token, never its whole output
// over it, which counts reasoning written before the first token, when that
// is a second or more and 100 tokens or more; else over the whole turn, by
// the same 1,000-token rule as Claude Code's whole-time floor.
func TestCodexRowsTellAFloor(t *testing.T) {
	row := func(out, reasoning int, ms, ttft int64) Row {
		return Row{Source: "log", Record: Record{Agent: "codex", Model: "gpt-6-luna", Output: out, Reasoning: reasoning, Millis: ms, TTFT: ttft}}
	}
	for _, c := range []struct {
		name string
		r    Row
		n    int
		ms   int64
	}{
		// 272 answer tokens 827 ms after a 10.2 s first token could be one
		// push, and the whole turn, 496 tokens, is under 1,000: none
		{"one call that reasoned, its answer under a second", row(496, 224, 11055, 10228), 0, 0},
		// 1,100 answer tokens 500 ms after an 11 s first token: the whole
		// turn, 1,300 over 11.5 s
		{"one call that reasoned, a long answer in one push", row(1300, 200, 11500, 11000), 1300, 11500},
		{"one call that reasoned, all of it after its first token", row(1200, 200, 12000, 2000), 1000, 10000},
		{"one call that reasoned, an answer of 60 over 3 s", row(260, 200, 5000, 2000), 0, 0},
		{"one call that didn't reason tells its own speed", row(500, 0, 5000, 1000), 0, 0},
		{"one call whose answer came too fast to tell", row(500, 100, 2050, 2000), 0, 0},
		{"one call that only reasoned", row(300, 300, 9000, 2000), 0, 0},
		{"one call that only reasoned, 1,000 tokens", row(1000, 1000, 9000, 2000), 1000, 9000},
		{"a call of several, 1,500 over 30 s", row(1500, 600, 30000, 0), 1500, 30000},
		{"a call of several too short to tell", row(800, 100, 20000, 0), 0, 0},
	} {
		n, ms := c.r.Floor()
		if n != c.n || ms != c.ms {
			t.Errorf("%s: floor %d/%d ms, want %d/%d", c.name, n, ms, c.n, c.ms)
		}
	}
	// through the gateway a Codex row is measured, never a floor
	gw := row(496, 224, 11055, 10228)
	gw.Source = ""
	if n, ms := gw.Floor(); n != 0 || ms != 0 {
		t.Errorf("gateway row: floor %d/%d", n, ms)
	}
}

// Fallmonkey on Discord: an 11-token astra answer over 2.75 s, its window
// after the first token too short, read ≥4 tok/s over the whole turn, while
// an 815-token Claude Code reply, the same formula, told nothing. A Codex
// whole-turn floor has the same 1,000-token gate.
func TestCodexWholeTurnFloorNeedsAThousandTokens(t *testing.T) {
	astra := Row{Source: "log", Record: Record{Agent: "codex", Model: "gpt-6-astra", Output: 11, Millis: 2750, TTFT: 2600}}
	if n, ms := astra.Floor(); n != 0 || ms != 0 {
		t.Errorf("astra's 11 tokens over 2.75 s: floor %d/%d ms, want none", n, ms)
	}
	claude := Row{Source: "log", Record: Record{Agent: "claude", Model: "claude-opus-5-5", Output: 815, Millis: 18000}}
	if n, ms := claude.Floor(); n != 0 || ms != 0 {
		t.Errorf("Claude Code's 815 tokens over 18 s: floor %d/%d ms, want none", n, ms)
	}
	for _, r := range []Row{
		{Source: "log", Record: Record{Agent: "codex", Model: "gpt-6-astra", Output: 1000, Millis: 12000, TTFT: 11800}},
		{Source: "log", Record: Record{Agent: "claude", Model: "claude-opus-5-5", Output: 1000, Millis: 12000}},
	} {
		if n, ms := r.Floor(); n != 1000 || ms != 12000 {
			t.Errorf("%s's 1,000 tokens over 12 s: floor %d/%d ms, want 1000/12000", r.Agent, n, ms)
		}
	}
}

// Fallmonkey on Discord: a Codex turn of one call whose first token came at
// 6.8 s, the model working, and its 380 tokens 0.2 s later in one push read
// 1,900 tok/s. A Codex turn of one call from its session file is measured
// only when the window after its first token is a second or more and its
// body 100 tokens or more; else it tells the least it wrote at over the
// whole turn, when that is 1,000 tokens or more over a second or more, as
// Claude Code's whole-time floor is. The gateway's own rows, and other
// agents', keep their speed after a short wait, and tell none 0.2 s after a
// long one (HeldWindowMs).
func TestCodexTurnMeasuredOnlyOverASecondAndAHundredTokens(t *testing.T) {
	row := func(out int, ms, ttft int64) Row {
		return Row{Source: "log", Record: Record{Agent: "codex", Model: "gpt-6.1-sol", Output: out, Millis: ms, TTFT: ttft}}
	}
	for _, c := range []struct {
		name    string
		r       Row
		dn      int
		dw      int64
		floorN  int
		floorMs int64
	}{
		{"one push 0.2 s after a 6.8 s first token", row(380, 7000, 6800), 0, 0, 0, 0},
		{"1,200 tokens in one push 0.5 s after a 9.5 s first token", row(1200, 10000, 9500), 0, 0, 1200, 10000},
		{"60 tokens over 2 s", row(60, 3000, 1000), 0, 0, 0, 0},
		{"500 tokens over 4 s", row(500, 5000, 1000), 500, 4000, 0, 0},
		{"100 tokens over exactly a second", row(100, 7800, 6800), 100, 1000, 0, 0},
		{"99 tokens over a second", row(99, 7800, 6800), 0, 0, 0, 0},
		{"400 tokens over 999 ms", row(400, 7799, 6800), 0, 0, 0, 0},
		{"1,000 tokens over 999 ms", row(1000, 7799, 6800), 0, 0, 1000, 7799},
	} {
		if n, w := c.r.Decode(); n != c.dn || w != c.dw {
			t.Errorf("%s: measured %d/%d ms, want %d/%d", c.name, n, w, c.dn, c.dw)
		}
		if n, ms := c.r.Floor(); n != c.floorN || ms != c.floorMs {
			t.Errorf("%s: floor %d/%d ms, want %d/%d", c.name, n, ms, c.floorN, c.floorMs)
		}
	}
	// the same reply through the gateway, or Claude Code's, tells none
	// either: 0.2 s after a 6.8 s wait is a reply let go whole
	// (HeldWindowMs, pluviophile714 on Discord); after a 0.3 s first token,
	// they keep their speed
	for _, r := range []Row{
		{Record: Record{Agent: "codex", Output: 380, Millis: 7000, TTFT: 6800}},
		{Source: "log", Record: Record{Agent: "claude", Output: 380, Millis: 7000, TTFT: 6800}},
	} {
		if n, w := r.Decode(); n != 0 || w != 0 {
			t.Errorf("%s/%q: measured %d/%d ms, want none", r.Agent, r.Source, n, w)
		}
		r.Millis, r.TTFT = 500, 300
		if n, w := r.Decode(); n != 380 || w != 200 {
			t.Errorf("%s/%q after a short wait: measured %d/%d ms, want 380/200", r.Agent, r.Source, n, w)
		}
	}
	// a group and the ledger's sum count it as a floor, not 1,900 tok/s
	var g Totals
	g.addRow(row(380, 7000, 6800))
	g.addRow(row(1200, 10000, 9500))
	g.addRow(row(500, 5000, 1000))
	if g.DecodeN != 1 || g.Speed() != 125 || g.FloorN != 1 || g.FloorOut != 1200 || g.FloorMs != 10000 {
		t.Fatalf("group: %+v", g)
	}
	at := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	_, sum, _, _ := ledgerWith(time.Time{}, Filter{}, nil, []sessions.Call{{Time: at, Agent: "codex", Model: "gpt-6.1-sol", Tokens: sessions.Tokens{Output: 380}, Millis: 7000, TTFT: 6800}})
	if sum.DecodeN != 0 || sum.DecodeMs != 0 || sum.Timed != 1 || sum.TTFT != 6800 {
		t.Fatalf("sum: %+v", sum)
	}
	var p Part
	p.add(row(380, 7000, 6800))
	if p.DecodeMs != 0 || p.Timed != 1 {
		t.Fatalf("part: %+v", p)
	}
}

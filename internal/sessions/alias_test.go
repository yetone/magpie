package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// aliasMsg is a line of a Claude Code reply by model.
func aliasMsg(id, model, at string, in int) string {
	return `{"type":"assistant","isSidechain":false,"message":{"model":"` + model + `","id":"` + id + `","role":"assistant","content":[],"usage":{"input_tokens":` + itoa(in) + `,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}},"timestamp":"` + at + `","cwd":"/work/alias","sessionId":"44444444-2222-3333-4444-555555555555"}` + "\n"
}

// A model counted under another id (magpie model alias) is one row of a
// session's models, of a day's usage and of a session's summary, and each
// keeps the cost of the id it was made under (SlouchyQuill507 on Discord).
func TestSessionsCountAliasedModelsAsOne(t *testing.T) {
	inZone(t, 0)
	claude, _ := setup(t)
	dir := filepath.Join(claude, "projects", "-work-alias")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := aliasMsg("m1", "claude-opus-5-5", "2026-09-25T10:00:00Z", 1000000) +
		aliasMsg("m2", "gpt-6-astra", "2026-09-25T10:01:00Z", 1000000)
	if err := os.WriteFile(filepath.Join(dir, "44444444-2222-3333-4444-555555555555.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	find := func() Session {
		t.Helper()
		for _, s := range List(0) {
			if s.Cwd == "/work/alias" {
				return s
			}
		}
		t.Fatal("session not listed")
		return Session{}
	}
	day := func(st Stats) []Usage {
		var out []Usage
		for _, d := range st.Days {
			for _, u := range d.Usage {
				if u.Cwd == "/work/alias" {
					out = append(out, u)
				}
			}
		}
		return out
	}
	// off by default: two rows
	if s := find(); len(s.Models) != 2 {
		t.Fatalf("with no alias: %+v", s.Models)
	}
	if u := day(statsAt(0, statsNow)); len(u) != 2 {
		t.Fatalf("with no alias: %+v", u)
	}

	if _, err := provider.SetUsageAlias("gpt-6-astra", "claude-opus-5-5"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = provider.SetUsageAlias("gpt-6-astra", "") })
	// $4 for opus's 1M in, $10 for astra's at astra's price: not $8
	want := 4.0 + 10.0
	s := find()
	if len(s.Models) != 1 || s.Models[0].Model != "claude-opus-5-5" || s.Models[0].Input != 2000000 || !s.Models[0].Priced || !near(s.Models[0].Cost, want) {
		t.Fatalf("aliased session: %+v", s.Models)
	}
	if !near(s.Cost, want) || s.Unpriced != 0 {
		t.Fatalf("aliased session cost %v unpriced %d", s.Cost, s.Unpriced)
	}
	if g, ok := Get(s.Agent + ":" + s.ID); !ok || len(g.Models) != 1 {
		t.Fatalf("Get: %+v", g.Models)
	}
	if f, ok := Find(s.Agent, s.ID); !ok || len(f.Models) != 1 {
		t.Fatalf("Find: %+v", f.Models)
	}
	st := statsAt(0, statsNow)
	u := day(st)
	if len(u) != 1 || u[0].Model != "claude-opus-5-5" || u[0].Input != 2000000 || !u[0].Priced || !near(u[0].Cost, want) {
		t.Fatalf("aliased day: %+v", u)
	}
	for _, sum := range st.Sessions {
		if sum.Cwd == "/work/alias" && (len(sum.Models) != 1 || sum.Models[0] != "claude-opus-5-5") {
			t.Fatalf("aliased summary models: %v", sum.Models)
		}
	}
	// the Stats kept for the next read are the aliased ones themselves:
	// no second copy of every summary beside them (#2278)
	if k, ok := keptStats.ranges[0]; !ok || len(k.Sessions) == 0 || &k.Sessions[0] != &st.Sessions[0] {
		t.Fatalf("an aliased read keeps a copy of its summaries beside the kept ones")
	}
	// what List and Stats keep for the next read is not changed: with the
	// alias gone, the next read has two rows again
	if _, err := provider.SetUsageAlias("gpt-6-astra", ""); err != nil {
		t.Fatal(err)
	}
	if s := find(); len(s.Models) != 2 {
		t.Fatalf("after reset: %+v", s.Models)
	}
	if u := day(statsAt(0, statsNow)); len(u) != 2 {
		t.Fatalf("after reset: %+v", u)
	}
}

// Aliasing a Stats made for the read never writes into the slices its days
// and sessions were given: a memo's copy stays as it was.
func TestAliasOwnedLeavesSharedSlicesAlone(t *testing.T) {
	alias := provider.UsageAliasIn(map[string]string{"b-model": "a-model"})
	in := Stats{
		Days: []Day{{Date: "2026-09-25", Usage: []Usage{
			{Agent: "x", Cwd: "/w", Model: "a-model", Tokens: Tokens{Input: 1}, Cost: 1, Priced: true},
			{Agent: "x", Cwd: "/w", Model: "b-model", Tokens: Tokens{Input: 2}, Cost: 2, Priced: false},
			{Agent: "x", Cwd: "/v", Model: "b-model", Tokens: Tokens{Input: 4}, Cost: 4, Priced: true},
		}}},
		Sessions: []Summary{{Key: "x:1", Models: []string{"b-model", "c-model", "a-model"}, ModelEffort: []ModelEffort{
			{Model: "a-model", Effort: "high", Tokens: Tokens{Input: 1}, Reasoning: 1, Cost: 1, Priced: true},
			{Model: "b-model", Effort: "high", Tokens: Tokens{Input: 2}, Reasoning: 2, Cost: 2, Priced: true},
		}}},
	}
	memo := Stats{Days: []Day{in.Days[0]}, Sessions: []Summary{in.Sessions[0]}}
	out := in
	aliasOwned(&out, alias)
	in = memo
	u := out.Days[0].Usage
	if len(u) != 2 || u[0].Model != "a-model" || u[0].Input != 3 || u[0].Cost != 3 || u[0].Priced || u[1].Cwd != "/v" || u[1].Model != "a-model" {
		t.Fatalf("usage: %+v", u)
	}
	if m := out.Sessions[0].Models; len(m) != 2 || m[0] != "a-model" || m[1] != "c-model" {
		t.Fatalf("models: %v", m)
	}
	if e := out.Sessions[0].ModelEffort; len(e) != 1 || e[0].Input != 3 || e[0].Reasoning != 3 || !e[0].Priced {
		t.Fatalf("efforts: %+v", e)
	}
	if in.Days[0].Usage[1].Model != "b-model" || in.Days[0].Usage[0].Input != 1 || in.Sessions[0].Models[0] != "b-model" || in.Sessions[0].ModelEffort[0].Input != 1 {
		t.Fatalf("input changed: %+v", in)
	}
}

package usage

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/sessions"
)

// Antigravity local-session calls recorded under its Orchestrator role
// ids (guyng35955 on X: tokens and cache shown, cost "—") are priced in the
// ledger's rows, its total and the agent's own view as the Gemini model
// each id is. The price is taken as the rows are read, so calls recorded
// before are priced too.
func TestAntigravityOrchestratorCallsPriced(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"google":{"id":"google","models":{
	    "gemini-4-argon":{"id":"gemini-4-argon","cost":{"input":2,"output":10,"cache_read":0.1}},
	    "gemini-4-carbon":{"id":"gemini-4-carbon","cost":{"input":2.5,"output":12,"cache_read":0.25}},
	    "gemini-4-hydrogen":{"id":"gemini-4-hydrogen","cost":{"input":1.5,"output":7.5,"cache_read":0.075}},
	    "gemini-4-neon":{"id":"gemini-4-neon","cost":{"input":3,"output":15,"cache_read":0.15}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	now := holdClock(t, time.Date(2026, 11, 14, 12, 0, 0, 0, time.Local))
	tok := sessions.Tokens{Input: 100000, Output: 20000, CacheRead: 400000}
	old := LogCalls
	LogCalls = func(time.Time) []sessions.Call {
		return []sessions.Call{
			{Time: now.Add(-time.Hour), Agent: "antigravity", Session: "a1", Model: "gemini-pro-orchestrator-argon", Tokens: tok},
			{Time: now.Add(-2 * time.Hour), Agent: "antigravity", Session: "a2", Model: "gemini-pro-orchestrator-carbon", Tokens: tok},
			{Time: now.Add(-3 * time.Hour), Agent: "antigravity", Session: "a2", Model: "gemini-flash-worker-agy", Tokens: tok},
			// agy's Orchestrator (Argon) (mu33410 on X)
			{Time: now.Add(-4 * time.Hour), Agent: "agy", Session: "g1", Model: "gemini-pro-orchestrator-g1-pro", Tokens: tok},
			// agy's Orchestrator (Neon) (mu33410 on X)
			{Time: now.Add(-5 * time.Hour), Agent: "agy", Session: "g2", Model: "gemini-pro-orchestrator-neon", Tokens: tok},
		}
	}
	t.Cleanup(func() { LogCalls = old })

	cost := func(in, out, cr float64) float64 { return (100000*in + 20000*out + 400000*cr) / 1e6 }
	want := map[string]float64{
		// Argon and Carbon at Antigravity's original $4 / $20 (cache read
		// the catalog's 0.25 where that is higher)
		"gemini-pro-orchestrator-argon":  cost(4, 20, 0.2),
		"gemini-pro-orchestrator-carbon": cost(4, 20, 0.25),
		"gemini-flash-worker-agy":        cost(1.5, 7.5, 0.075),
		"gemini-pro-orchestrator-g1-pro": cost(4, 20, 0.2),
		"gemini-pro-orchestrator-neon":   cost(3, 15, 0.15),
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	rows, total, _ := Ledger(Month, Filter{})
	if len(rows) != 5 {
		t.Fatalf("rows %d", len(rows))
	}
	var sum float64
	for _, r := range rows {
		if !r.Priced || !near(r.Cost, want[r.Model]) {
			t.Errorf("%s: priced %v cost %v, want %v", r.Model, r.Priced, r.Cost, want[r.Model])
		}
		sum += want[r.Model]
	}
	if total.Unpriced != 0 || !near(total.Cost, sum) {
		t.Errorf("total: cost %v unpriced %d, want %v and 0", total.Cost, total.Unpriced, sum)
	}
	// the agent's own view
	_, agent, _ := Ledger(Month, Filter{Agent: "antigravity"})
	agyCost := want["gemini-pro-orchestrator-g1-pro"] + want["gemini-pro-orchestrator-neon"]
	if app := sum - agyCost; agent.Unpriced != 0 || !near(agent.Cost, app) {
		t.Errorf("antigravity: cost %v unpriced %d, want %v and 0", agent.Cost, agent.Unpriced, app)
	}
	_, agy, _ := Ledger(Month, Filter{Agent: "agy"})
	if g := agyCost; agy.Unpriced != 0 || !near(agy.Cost, g) {
		t.Errorf("agy: cost %v unpriced %d, want %v and 0", agy.Cost, agy.Unpriced, g)
	}
}

// A call of agy's or the Antigravity app's own session log on Gemini 4
// Carbon is counted at Antigravity's original price (ZhouYimingsjtu on X:
// Carbon : Neon : Hydrogen measured 1.64 : 1 : 0.39 on an Ultra 20x plan),
// not the introductory one models.dev lists; the same model in another
// agent's log (the Gemini API's) keeps the catalog's.
func TestAntigravityLogCarbonAtOriginalPrice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"google":{"id":"google","models":{
	    "gemini-4-carbon":{"id":"gemini-4-carbon","cost":{"input":2,"output":10,"cache_read":0.1}},
	    "gemini-4-hydrogen":{"id":"gemini-4-hydrogen","cost":{"input":1.5,"output":7.5,"cache_read":0.075}},
	    "gemini-4-neon":{"id":"gemini-4-neon","cost":{"input":3,"output":15,"cache_read":0.15}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	now := holdClock(t, time.Date(2026, 11, 19, 12, 0, 0, 0, time.Local))
	tok := sessions.Tokens{Input: 100000, Output: 20000, CacheRead: 400000}
	old := LogCalls
	LogCalls = func(time.Time) []sessions.Call {
		return []sessions.Call{
			{Time: now.Add(-1 * time.Hour), Agent: "antigravity", Session: "a1", Model: "gemini-4-carbon", Tokens: tok},
			{Time: now.Add(-2 * time.Hour), Agent: "agy", Session: "g1", Model: "gemini-4-carbon-high", Tokens: tok},
			{Time: now.Add(-3 * time.Hour), Agent: "agy", Session: "g2", Model: "gemini-4-neon-high", Tokens: tok},
			{Time: now.Add(-4 * time.Hour), Agent: "agy", Session: "g3", Model: "gemini-4-hydrogen-low", Tokens: tok},
			// the Gemini CLI's call on the same model is the Gemini API's
			{Time: now.Add(-5 * time.Hour), Agent: "gemini", Session: "x1", Model: "gemini-4-carbon", Tokens: tok},
		}
	}
	t.Cleanup(func() { LogCalls = old })
	cost := func(in, out, cr float64) float64 { return (100000*in + 20000*out + 400000*cr) / 1e6 }
	want := map[string]float64{
		"antigravity/gemini-4-carbon": cost(4, 20, 0.2), "agy/gemini-4-carbon-high": cost(4, 20, 0.2),
		"agy/gemini-4-neon-high": cost(3, 15, 0.15), "agy/gemini-4-hydrogen-low": cost(1.5, 7.5, 0.075),
		"gemini/gemini-4-carbon": cost(2, 10, 0.1),
	}
	rows, _, _ := Ledger(Month, Filter{})
	if len(rows) != len(want) {
		t.Fatalf("rows %d, want %d", len(rows), len(want))
	}
	for _, r := range rows {
		w := want[r.Agent+"/"+r.Model]
		if !r.Priced || math.Abs(r.Cost-w) > 1e-9 {
			t.Errorf("%s %s: priced %v cost %v, want %v", r.Agent, r.Model, r.Priced, r.Cost, w)
		}
	}
}

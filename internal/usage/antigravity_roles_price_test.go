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
	    "gemini-4-hydrogen":{"id":"gemini-4-hydrogen","cost":{"input":1.5,"output":7.5,"cache_read":0.075}}}}}`), 0o644)
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
		}
	}
	t.Cleanup(func() { LogCalls = old })

	cost := func(in, out, cr float64) float64 { return (100000*in + 20000*out + 400000*cr) / 1e6 }
	want := map[string]float64{
		"gemini-pro-orchestrator-argon":  cost(2, 10, 0.1),
		"gemini-pro-orchestrator-carbon": cost(2.5, 12, 0.25),
		"gemini-flash-worker-agy":        cost(1.5, 7.5, 0.075),
		"gemini-pro-orchestrator-g1-pro": cost(2, 10, 0.1),
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	rows, total, _ := Ledger(Month, Filter{})
	if len(rows) != 4 {
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
	if app := sum - want["gemini-pro-orchestrator-g1-pro"]; agent.Unpriced != 0 || !near(agent.Cost, app) {
		t.Errorf("antigravity: cost %v unpriced %d, want %v and 0", agent.Cost, agent.Unpriced, app)
	}
	_, agy, _ := Ledger(Month, Filter{Agent: "agy"})
	if g := want["gemini-pro-orchestrator-g1-pro"]; agy.Unpriced != 0 || !near(agy.Cost, g) {
		t.Errorf("agy: cost %v unpriced %d, want %v and 0", agy.Cost, agy.Unpriced, g)
	}
}

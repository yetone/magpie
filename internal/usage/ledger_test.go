package usage

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// The ledger lists a period's calls newest first, each with the model
// asked for, sent and served, marked when another answered, and its cost
// at list price; it filters by agent, failure and text; a record written
// before the requested and served models were kept still loads; the CSV
// has one row a call.
func TestLedger(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	// a models.dev catalog pricing relay's sol: $2 in, $8 out, $0.5 a
	// cached read, $2.5 a cache write, per million
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"vendor":{"id":"vendor","models":{"sol":{"id":"sol","cost":{"input":2,"output":8,"cache_read":0.5,"cache_write":2.5}}}}}`), 0o644)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example/v1", Catalog: "vendor"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	old := `{"t":"` + now.Add(-3*time.Hour).Format(time.RFC3339Nano) + `","agent":"claude","provider":"relay","host":"relay.example","model":"sol","in":1000,"out":100,"ms":900,"status":200}`
	os.MkdirAll(filepath.Dir(Path()), 0o755)
	if err := os.WriteFile(Path(), []byte(old+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	Append(Record{Time: now.Add(-2 * time.Hour), Agent: "codex", Provider: "relay", Host: "relay.example", Model: "sol", Requested: "fast", Served: "luna",
		Input: 2000, Output: 500, CacheRead: 4000, CacheWrite: 1000, Effort: "high", Millis: 3200, TTFT: 400, Status: 200, Session: "s1"})
	Append(Record{Time: now.Add(-1 * time.Hour), Agent: "codex", Provider: "relay", Host: "relay.example", Model: "sol", Requested: "relay/sol", Served: "sol-2026-01-01",
		Input: 10, Output: 1, Millis: 100, Status: 200})
	Append(Record{Time: now.Add(-30 * time.Minute), Agent: "codex", Provider: "relay", Host: "relay.example", Model: "sol", Requested: "fast", Millis: 50, Status: 429})
	Append(Record{Time: now.Add(-40 * 24 * time.Hour), Agent: "codex", Provider: "relay", Model: "sol", Input: 1}) // before the month

	rows, sum, agents := Ledger(Month, Filter{})
	if len(rows) != 4 || sum.Calls != 4 || sum.Errors != 1 || strings.Join(agents, ",") != "claude,codex" {
		t.Fatalf("rows %d, %+v, agents %v", len(rows), sum, agents)
	}
	if rows[0].Status != 429 || rows[3].Agent != "claude" {
		t.Fatalf("not newest first: %+v", rows)
	}
	sw := rows[2]
	if sw.Requested != "fast" || sw.Model != "sol" || sw.Served != "luna" || !sw.Swapped || !sw.Priced {
		t.Fatalf("swapped row %+v", sw)
	}
	if want := (2000*2 + 500*8 + 4000*0.5 + 1000*2.5) / 1e6; sw.Cost != want {
		t.Fatalf("cost %v, want %v", sw.Cost, want)
	}
	if rows[1].Swapped || rows[1].Served != "sol-2026-01-01" {
		t.Fatalf("a dated name is the same model: %+v", rows[1])
	}
	if rows[3].Requested != "" || rows[3].Served != "" || rows[3].Input != 1000 || !rows[3].Priced {
		t.Fatalf("old record %+v", rows[3])
	}
	if rows[0].Priced || rows[0].Cost != 0 {
		t.Fatalf("a call with no tokens has no cost: %+v", rows[0])
	}

	if rows, _, _ := Ledger(Month, Filter{Agent: "claude"}); len(rows) != 1 || rows[0].Agent != "claude" {
		t.Fatalf("agent filter: %+v", rows)
	}
	if rows, s, _ := Ledger(Month, Filter{Failed: true}); len(rows) != 1 || rows[0].Status != 429 || s.Calls != 1 {
		t.Fatalf("failed filter: %+v", rows)
	}
	if rows, _, _ := Ledger(Month, Filter{Query: "LUNA"}); len(rows) != 1 || rows[0].Served != "luna" {
		t.Fatalf("query filter: %+v", rows)
	}
	if rows, _, _ := Ledger(All, Filter{}); len(rows) != 5 {
		t.Fatalf("all: %d", len(rows))
	}

	var b strings.Builder
	if err := WriteCSV(&b, rows[1:3]); err != nil {
		t.Fatal(err)
	}
	want := strings.Join(CSVHeader, ",") + "\n" +
		rows[1].Time.Format(time.RFC3339) + ",codex,relay/sol,relay,relay.example,sol,sol-2026-01-01,false,,10,1,0,0,0,0.000028,100,,200,false,,\n" +
		rows[2].Time.Format(time.RFC3339) + ",codex,fast,relay,relay.example,sol,luna,true,high,2000,500,1000,4000,0,0.012500,3200,400,200,false,s1,\n"
	if b.String() != want {
		t.Fatalf("csv:\n%s\nwant:\n%s", b.String(), want)
	}
}

// A call a subscription served — Codex's ChatGPT account, Copilot — or a
// relay with no models.dev id of its own is priced at its model's maker's
// list price, as a Claude account's is at Anthropic's (#224); a model
// named with its maker's path is the same model; one no maker lists stays
// unpriced.
func TestSubscriptionListPrice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{
	  "openai":{"id":"openai","models":{
	    "gpt-6-astra":{"id":"gpt-6-astra","cost":{"input":10,"output":50,"cache_read":1,"cache_write":12.5}},
	    "gpt-6-luna":{"id":"gpt-6-luna","cost":{"input":0.1,"output":0.5,"cache_read":0.01,"cache_write":0.125}}}},
	  "google":{"id":"google","models":{
	    "gemini-3.8-flash":{"id":"gemini-3.8-flash","cost":{"input":0.75,"output":3.75,"cache_read":0.075}}}},
	  "anthropic":{"id":"anthropic","models":{
	    "claude-opus-5-5":{"id":"claude-opus-5-5","cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	calls := []struct {
		prov, model string
		want        float64
	}{
		{"codex", "gpt-6-astra", (1e6*10 + 1e5*50 + 1e6*1) / 1e6},
		{"codex", "gpt-6-luna", (1e6*0.1 + 1e5*0.5 + 1e6*0.01) / 1e6},
		{"copilot", "gemini-3.8-flash", (1e6*0.75 + 1e5*3.75 + 1e6*0.075) / 1e6},
		{"copilot", "gpt-6-luna", (1e6*0.1 + 1e5*0.5 + 1e6*0.01) / 1e6},
		{"claude", "claude-opus-5-5", (1e6*4 + 1e5*20 + 1e6*0.2) / 1e6},
		{"relay", "openai/gpt-6-astra", (1e6*10 + 1e5*50 + 1e6*1) / 1e6},
		{"relay", "mystery-1", 0},
	}
	for i, c := range calls {
		Append(Record{Time: now.Add(-time.Duration(len(calls)-i) * time.Minute), Agent: "codex", Provider: c.prov, Model: c.model,
			Input: 1_000_000, Output: 100_000, CacheRead: 1_000_000, Status: 200})
	}
	rows, sum, _ := Ledger(Month, Filter{})
	if len(rows) != len(calls) {
		t.Fatalf("rows %d", len(rows))
	}
	total := 0.0
	for i, c := range calls {
		r := rows[len(calls)-1-i]
		if r.Model != c.model || r.Priced != (c.want > 0) || math.Abs(r.Cost-c.want) > 1e-9 {
			t.Errorf("%s/%s: priced=%v cost=%v, want %v", c.prov, c.model, r.Priced, r.Cost, c.want)
		}
		total += c.want
	}
	s := Summarize(Month)
	if s.Unpriced != 1 || math.Abs(s.Cost-total) > 1e-9 || math.Abs(sum.Cost-total) > 1e-9 {
		t.Fatalf("summary cost %v unpriced %d, ledger %v; want %v and 1", s.Cost, s.Unpriced, sum.Cost, total)
	}
	for _, m := range s.Models {
		if m.Model != "mystery-1" && (m.Unpriced != 0 || m.Cost == 0) {
			t.Errorf("model %s unpriced: %+v", m.ID, m.Totals)
		}
	}
}

// A call is counted at the price the user set for that provider and model,
// all four token tiers included: the ledger is where a wrong price becomes a
// wrong number, and a tier left at the catalogue's price would not show in
// the input and output columns alone.
func TestLedgerUsesTheStatedPrice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"id":"openai","models":{"sol":{"id":"sol",`+
		`"cost":{"input":2,"output":8,"cache_read":0.5,"cache_write":2.5}}}}}`), 0o644)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
	// all four parts distinct from the catalogue's, so a tier counted at the
	// catalogue's price instead would move the total
	if err := settings.Save(settings.Settings{ModelPrices: map[string]settings.ModelPrice{
		"relay/sol": {Input: new(float64(0.2)), Output: new(float64(1)),
			CacheRead: new(float64(0.05)), CacheWrite: new(float64(0.25))},
	}}); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(Path()), 0o755)
	Append(Record{Time: time.Now(), Agent: "codex", Provider: "relay", Host: "relay.example",
		Model: "sol", Input: 1000, Output: 100, CacheRead: 2000, CacheWrite: 400, Status: 200})

	rows, sum, _ := Ledger(Month, Filter{})
	if len(rows) != 1 || !rows[0].Priced {
		t.Fatalf("rows %+v", rows)
	}
	// (1000*0.2 + 100*1 + 2000*0.05 + 400*0.25) / 1e6
	const want = 0.0005
	if math.Abs(rows[0].Cost-want) > 1e-12 || math.Abs(sum.Cost-want) > 1e-12 {
		t.Fatalf("row cost %v, total %v; want %v", rows[0].Cost, sum.Cost, want)
	}
}

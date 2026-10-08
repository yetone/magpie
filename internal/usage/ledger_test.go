package usage

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
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
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	// a maker's API list price for sol: $2 in, $8 out, $0.5 a
	// cached read, $2.5 a cache write, per million
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"id":"anthropic","models":{"sol":{"id":"sol","cost":{"input":2,"output":8,"cache_read":0.5,"cache_write":2.5}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
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
		Input: 2000, Output: 500, CacheRead: 4000, CacheWrite: 1000, Effort: "high", Millis: 3200, TTFT: 400, Status: 200, Session: "s1", RouteID: 123})
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
	if sw.RouteID != 123 || sw.Requested != "fast" || sw.Model != "sol" || sw.Served != "luna" || !sw.Swapped || !sw.Priced {
		t.Fatalf("swapped row %+v", sw)
	}
	if want := (2000*2 + 500*8 + 4000*0.5 + 1000*2.5) / 1e6; sw.Cost != want {
		t.Fatalf("cost %v, want %v", sw.Cost, want)
	}
	if rows[1].Swapped || rows[1].Served != "sol-2026-01-01" {
		t.Fatalf("a dated name is the same model: %+v", rows[1])
	}
	if rows[3].RouteID != 0 || rows[3].Requested != "" || rows[3].Served != "" || rows[3].Input != 1000 || !rows[3].Priced {
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
		rows[1].Time.Format(time.RFC3339) + ",codex,relay/sol,relay,relay.example,sol,sol-2026-01-01,false,,10,1,0,0,0,0.000028,100,,200,false,,,,,,,,,,,,false,,,false,,,,,0,0\n" +
		rows[2].Time.Format(time.RFC3339) + ",codex,fast,relay,relay.example,sol,luna,true,high,2000,500,1000,4000,0,0.012500,3200,400,200,false,s1,,,,,123,,,,,,false,,,false,,,,,1000,0\n"
	if b.String() != want {
		t.Fatalf("csv:\n%s\nwant:\n%s", b.String(), want)
	}
}

// Calls use the provider catalog when available, falling back to the maker
// for subscriptions and unknown routes. A reseller-only model is still priced
// for that reseller, while a model absent from both catalogs stays unpriced.
func TestSubscriptionListPrice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
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
	    "claude-opus-5-5":{"id":"claude-opus-5-5","cost":{"input":4,"output":20,"cache_read":0.2,"cache_write":5}}}},
	  "discount":{"id":"discount","models":{
	    "gpt-6-astra":{"id":"gpt-6-astra","cost":{"input":1,"output":2,"cache_read":0.01}},
	    "mystery-1":{"id":"mystery-1","cost":{"input":99,"output":99}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "https://relay.example/v1", Catalog: "discount"}); err != nil {
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
		{"relay", "openai/gpt-6-astra", (1e6*1 + 1e5*2 + 1e6*0.01) / 1e6},
		{"relay", "gpt-6-astra", (1e6*1 + 1e5*2 + 1e6*0.01) / 1e6},
		{UnknownProvider, "gpt-6-astra", (1e6*10 + 1e5*50 + 1e6*1) / 1e6},
		{"relay", "mystery-1", (1e6*99 + 1e5*99) / 1e6},
		{UnknownProvider, "mystery-1", 0},
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

// The ledger adds the calls an agent's session file records that the
// gateway didn't log — a Claude Desktop turn on its own sign-in, say — as
// rows marked "log", priced by their model; leaves out those the gateway
// did log (the same session, a request that spans the file's time); counts
// and filters a failure the file tells, which has no status; and the
// failures the gateway logged keep what the vendor said.
func TestLedgerWithSessionLogCalls(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	// a models.dev catalog pricing Claude Sonnet 5: $3 in, $15 out, $0.3 a cached read, $3.75 a cache write, per million
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"anthropic":{"id":"anthropic","models":{"claude-sonnet-5":{"id":"claude-sonnet-5","cost":{"input":3,"output":15,"cache_read":0.3,"cache_write":3.75}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)

	// noon of the day Clock is held at, so that what the test lists a few
	// hours before it is that day's
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	// the gateway's: an answered request of session s1 that began at -10m and
	// took 20 s, and a failure of another with what the vendor said
	Append(Record{Time: now.Add(-10 * time.Minute), Agent: "claude", Provider: "claude", Model: "claude-sonnet-5", Input: 5, Output: 5,
		Millis: 20000, Status: 200, Session: "s1", RequestID: "req_gw", Endpoint: "/v1/messages"})
	Append(Record{Time: now.Add(-5 * time.Minute), Agent: "codex", Provider: "relay", Model: "sol", Millis: 40, Status: 429, Session: "s9",
		Error: "Relay: slow down", ErrType: "rate_limit_error", RequestID: "req_bad", Endpoint: "/v1/responses"})

	calls := []sessions.Call{
		// in s1, 8 s into the request the gateway logged: the same call
		{Time: now.Add(-10*time.Minute + 8*time.Second), Agent: "claude", Session: "s1", Model: "claude-sonnet-5", RequestID: "req_gw", Tokens: sessions.Tokens{Input: 5, Output: 5}},
		// in s1 an hour on: a call the gateway didn't see
		{Time: now.Add(-1 * time.Hour), Agent: "claude-desktop", Session: "s1", Model: "claude-sonnet-5", RequestID: "req_log",
			Tokens: sessions.Tokens{Input: 1000, Output: 200, CacheRead: 4000, CacheWrite: 500}},
		// a failure the file tells
		{Time: now.Add(-2 * time.Hour), Agent: "claude-desktop", Session: "s2", Error: "rate_limit", ErrorText: "You've hit your limit", RequestID: "req_lim"},
		// a Codex turn of a session the gateway never saw, a month ago
		{Time: now.Add(-20 * 24 * time.Hour), Agent: "codex", Session: "s3", Model: "gpt-6-luna", Tokens: sessions.Tokens{Input: 10, Output: 2}, TTFT: 700, Millis: 2100},
	}
	old := LogCalls
	LogCalls = func(time.Time) []sessions.Call { return calls }
	t.Cleanup(func() { LogCalls = old })

	rows, sum, agents := Ledger(Month, Filter{})
	if len(rows) != 5 || sum.Calls != 5 || sum.Errors != 2 || strings.Join(agents, ",") != "claude,claude-desktop,codex" {
		t.Fatalf("rows %d, %+v, agents %v", len(rows), sum, agents)
	}
	// newest first, the two logs interleaved by time
	var order []string
	for _, r := range rows {
		order = append(order, r.RequestID)
	}
	if strings.Join(order, ",") != "req_bad,req_gw,req_log,req_lim," {
		t.Fatalf("order %v", order)
	}
	if rows[0].Source != "" || rows[0].Error != "Relay: slow down" || rows[0].ErrType != "rate_limit_error" || rows[0].Endpoint != "/v1/responses" || !rows[0].Failed() {
		t.Fatalf("gateway failure %+v", rows[0])
	}
	lg := rows[2]
	if lg.Source != "log" || lg.Status != 0 || lg.Agent != "claude-desktop" || lg.Provider != UnknownProvider || !lg.Priced || lg.Failed() {
		t.Fatalf("logged call %+v", lg)
	}
	if want := (1000*3 + 200*15 + 4000*0.3 + 500*3.75) / 1e6; lg.Cost != want {
		t.Fatalf("cost %v, want %v", lg.Cost, want)
	}
	// Claude Code's file names the model the API answered with; a swap is
	// told against what was asked for, which it doesn't say
	if lg.Served != "claude-sonnet-5" || lg.Requested != "" || lg.Swapped {
		t.Fatalf("Claude's model is the served one: %+v", lg)
	}
	lim := rows[3]
	if lim.Source != "log" || !lim.Failed() || lim.Error != "You've hit your limit" || lim.ErrType != "rate_limit" || lim.Priced {
		t.Fatalf("failure the file tells %+v", lim)
	}
	// Codex's file names the model it asked for, and none that answered
	if cx := rows[4]; cx.Requested != "gpt-6-luna" || cx.Served != "" || cx.Swapped {
		t.Fatalf("Codex's model is the requested one: %+v", cx)
	}
	if cx := rows[4]; cx.Provider != UnknownProvider || cx.TTFT != 700 || cx.Millis != 2100 || cx.Priced {
		t.Fatalf("codex call %+v", cx)
	}

	if rows, s, _ := Ledger(Month, Filter{Failed: true}); len(rows) != 2 || s.Calls != 2 || s.Errors != 2 {
		t.Fatalf("failed filter: %d rows, %+v", len(rows), s)
	}
	if rows, _, _ := Ledger(Month, Filter{Agent: "claude-desktop"}); len(rows) != 2 {
		t.Fatalf("agent filter: %+v", rows)
	}
	if rows, _, _ := Ledger(Today, Filter{}); len(rows) != 4 {
		t.Fatalf("today: %d", len(rows)) // the month-old Codex turn is out of it
	}
	var b strings.Builder
	if err := WriteCSV(&b, rows[2:4]); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{",req_log,,,,log,false,,,false,,,,,500,0\n", ",true,s2,,,,,,req_lim,,You've hit your limit,rate_limit,log,false,,,false,,,,,0,0\n"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("csv lacks %q:\n%s", want, b.String())
		}
	}
}

// The rows of a period, summed for the chart over them: a point a hour of
// today, a day of a week, a week (from a Monday) once the time is long, each
// with its calls, failures, tokens and cost; a row of before the period is
// in none.
func TestLedgerSeries(t *testing.T) {
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	row := func(at time.Time, in, out, cr, cw int, cost float64, priced bool, status int, errText string) Row {
		return Row{Record: Record{Time: at, Provider: "p", Input: in, Output: out, CacheRead: cr, CacheWrite: cw, Status: status, Error: errText}, Cost: cost, Priced: priced}
	}
	rows := []Row{
		row(midnight.Add(10*time.Minute), 10, 2, 100, 5, 0.5, true, 200, ""),
		row(midnight.Add(50*time.Minute), 20, 3, 200, 0, 0.25, true, 500, "boom"),
		row(midnight.Add(-2*time.Hour), 1, 1, 0, 0, 0.1, true, 200, ""),          // yesterday
		row(midnight.Add(20*time.Minute), 4, 4, 0, 0, 0, false, 0, "rate_limit"), // unpriced, and a session file's failure
	}

	bucket, pts := LedgerSeries(Today, rows)
	if bucket != "hour" || len(pts) != 24 {
		t.Fatalf("today: %s, %d points", bucket, len(pts))
	}
	h := pts[0]
	if h.Calls != 3 || h.Errors != 2 || h.Input != 34 || h.Output != 9 || h.CacheRead != 300 || h.CacheWrite != 5 || h.Cost != 0.75 || h.Unpriced != 1 {
		t.Fatalf("the first hour: %+v", h.Totals)
	}
	var calls int
	for _, p := range pts {
		calls += p.Calls
	}
	if calls != 3 {
		t.Fatalf("yesterday's row is in today's chart: %d calls", calls)
	}

	bucket, pts = LedgerSeries(Week, rows)
	if bucket != "day" || len(pts) != 7 || pts[6].Calls != 3 || pts[5].Calls != 1 || pts[5].Cost != 0.1 {
		t.Fatalf("week: %s, %d points, %+v %+v", bucket, len(pts), pts[5].Totals, pts[6].Totals)
	}

	// a long time is by the week, from a Monday, and all the rows are in it
	rows = append(rows, row(midnight.AddDate(0, 0, -100), 7, 7, 0, 0, 1, true, 200, ""))
	bucket, pts = LedgerSeries(All, rows)
	if bucket != "week" || pts[0].Time.Weekday() != time.Monday || len(pts)*7 < 100 {
		t.Fatalf("all: %s, %d points from %v", bucket, len(pts), pts[0].Time)
	}
	calls = 0
	for _, p := range pts {
		calls += p.Calls
	}
	if calls != len(rows) || pts[0].Calls != 1 {
		t.Fatalf("all: %d of %d rows in the weeks, %d in the first", calls, len(rows), pts[0].Calls)
	}

	// no rows: the empty timeline, so the chart has its place
	if bucket, pts := LedgerSeries(Month, nil); bucket != "day" || len(pts) != 30 {
		t.Fatalf("no rows: %s, %d points", bucket, len(pts))
	}
}

// A point's part of each model says how fast that model's replies came,
// as the point's own sums do, for a chart of speed to tell the models
// apart (#860): a failed call and one too fast to time count in neither.
func TestLedgerSeriesSpeed(t *testing.T) {
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	at := time.Date(now.Year(), now.Month(), now.Day(), 0, 10, 0, 0, now.Location())
	row := func(model string, out int, ms, ttft int64, status int) Row {
		return Row{Record: Record{Time: at, Provider: "p", Model: model, Output: out, Millis: ms, TTFT: ttft, Status: status}}
	}
	rows := []Row{
		row("fast", 1000, 2500, 500, 200), // 1000 tokens in 2 s
		row("fast", 300, 1500, 500, 200),  // 300 in 1 s
		row("slow", 100, 6000, 1000, 200), // 100 in 5 s
		row("slow", 900, 9000, 2000, 500), // failed
		row("slow", 50, 1001, 1000, 200),  // 1 ms: no speed, its TTFT counts
	}
	_, pts := LedgerSeries(Today, rows)
	h := pts[0]
	fast, slow := h.By["model"]["fast"], h.By["model"]["slow"]
	if fast.Timed != 2 || fast.TTFT != 1000 || fast.DecodeMs != 3000 || fast.DecodeOut != 1300 {
		t.Fatalf("fast: %+v", fast)
	}
	if slow.Calls != 3 || slow.Timed != 2 || slow.TTFT != 2000 || slow.DecodeMs != 5000 || slow.DecodeOut != 100 {
		t.Fatalf("slow: %+v", slow)
	}
	if h.DecodeMs != fast.DecodeMs+slow.DecodeMs || h.DecodeOut != fast.DecodeOut+slow.DecodeOut || h.Timed != 4 {
		t.Fatalf("the point's own: %+v", h.Totals)
	}
}

// Calls are told apart by provider, agent and model: a filter to one
// provider, the providers that had calls (whatever the filter), the sums of
// each with the most tokens first, and the parts of each point of the
// timeline for a chart to stack — the ones with the most tokens over the
// period, the rest left to the point's own sums.
func TestLedgerByProvider(t *testing.T) {
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	at := time.Date(now.Year(), now.Month(), now.Day(), 0, 10, 0, 0, now.Location())
	rec := func(agent, prov, model string, in, cw int, mins int) Record {
		return Record{Time: at.Add(time.Duration(mins) * time.Minute), Agent: agent, Provider: prov, Model: model, Input: in, CacheRead: cw, Output: 1, Status: 200}
	}
	recs := []Record{
		rec("codex", "relay", "sol", 100, 1000, 0),
		rec("claude", "anthropic", "sonnet", 10, 50, 1),
		rec("codex", "relay", "luna", 5, 0, 2),
		rec("claude", "anthropic", "opus", 300, 3000, 90), // the next hour
	}
	since := at.Add(-time.Hour)

	rows, sum, _, providers := ledgerWith(since, Filter{Provider: "relay"}, recs, nil)
	if len(rows) != 2 || sum.Calls != 2 || strings.Join(providers, ",") != "anthropic,relay" {
		t.Fatalf("one provider: %d rows, %+v, providers %v", len(rows), sum, providers)
	}
	if _, _, agents := ledger(since, Filter{Provider: "nobody"}, recs); len(agents) != 2 {
		t.Fatalf("the agents are those that called, whatever the filter: %v", agents)
	}

	all, _, _, _ := ledgerWith(since, Filter{}, recs, nil)
	by := Breakdown(all, "provider")
	if len(by) != 2 || by[0].ID != "anthropic" || by[0].Calls != 2 || by[0].AllTokens() != 10+50+1+300+3000+1 || by[1].ID != "relay" || by[1].Input != 105 {
		t.Fatalf("by provider, the most tokens first: %+v", by)
	}
	if by := Breakdown(all, "model"); len(by) != 4 || by[0].ID != "opus" || by[3].ID != "luna" {
		t.Fatalf("by model: %+v", by)
	}
	if by := Breakdown(all, "agent"); len(by) != 2 || by[0].ID != "claude" {
		t.Fatalf("by agent: %+v", by)
	}

	_, pts := LedgerSeries(Today, all)
	first := pts[0].By["provider"]
	if len(pts) != 24 || first["relay"].Calls != 2 || first["relay"].Tokens != 100+1000+1+5+1 || first["anthropic"].Calls != 1 || pts[1].By["provider"]["anthropic"].Tokens != 300+3000+1 {
		t.Fatalf("parts of the hours: %+v %+v", pts[0].By["provider"], pts[1].By["provider"])
	}
	// a cost is a part's when the row has one
	for i := range all {
		if all[i].Model == "sol" {
			all[i].Cost, all[i].Priced = 0.5, true
		}
	}
	_, pts = LedgerSeries(Today, all)
	if got := pts[0].By["model"]["sol"].Cost + pts[1].By["model"]["sol"].Cost; got != 0.5 {
		t.Fatalf("cost of a model: %v", got)
	}

	// more models than a point tells apart: those with the most tokens
	var many []Row
	for i := 0; i < seriesKeep+6; i++ {
		many = append(many, Row{Record: Record{Time: at, Model: fmt.Sprintf("m%02d", i), Input: 1000 - i, Status: 200}})
	}
	_, pts = LedgerSeries(Today, many)
	kept := pts[0].By["model"]
	if len(kept) != seriesKeep || kept["m00"].Calls != 1 || kept[fmt.Sprintf("m%02d", seriesKeep)].Calls != 0 {
		t.Fatalf("kept %d models", len(kept))
	}
	if pts[0].Calls != seriesKeep+6 {
		t.Fatalf("the point's own sums have them all: %d", pts[0].Calls)
	}
}

// A call read from a session file has the model asked for, the one sent (as
// asked, with nothing between, less the size of the context Claude Code names it
// by) and the one that answered: the 1M-context model answered under its plain
// name is no swap, another model is; Codex's file has the model asked for only.
func TestLedgerLogModels(t *testing.T) {
	now := time.Now()
	logs := []sessions.Call{
		{Time: now.Add(-time.Minute), Agent: "claude-desktop", Session: "a", Model: "claude-opus-5", Requested: "claude-opus-5[1m]", Tokens: sessions.Tokens{Input: 1, Output: 1}, Millis: 4200},
		{Time: now.Add(-2 * time.Minute), Agent: "claude", Session: "b", Model: "claude-sonnet-5-5", Requested: "claude-opus-5-5", Tokens: sessions.Tokens{Input: 1, Output: 1}},
		{Time: now.Add(-3 * time.Minute), Agent: "claude", Session: "c", Model: "claude-opus-5-5", Tokens: sessions.Tokens{Input: 1, Output: 1}}, // no note before it
		{Time: now.Add(-4 * time.Minute), Agent: "codex", Session: "d", Model: "gpt-6-luna", Tokens: sessions.Tokens{Input: 1, Output: 1}},
	}
	rows, _, _, _ := ledgerWith(now.Add(-time.Hour), Filter{}, nil, logs)
	type m struct {
		req, sent, served string
		swapped           bool
	}
	var got []m
	for _, r := range rows {
		got = append(got, m{r.Requested, r.Model, r.Served, r.Swapped})
	}
	want := []m{
		{"claude-opus-5[1m]", "claude-opus-5", "claude-opus-5", false},
		{"claude-opus-5-5", "claude-opus-5-5", "claude-sonnet-5-5", true},
		{"", "claude-opus-5-5", "claude-opus-5-5", false},
		{"gpt-6-luna", "gpt-6-luna", "", false},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
	if rows[0].Millis != 4200 {
		t.Fatalf("the time the file's stamps tell is kept: %+v", rows[0])
	}
	for _, c := range [][2]string{{"claude-opus-5[1m]", "claude-opus-5"}, {"Claude-Opus-5[200K]", "claude-opus-5"}, {"claude-opus-5", "claude-opus-5-20261001"}} {
		if Swapped(c[0], c[1]) {
			t.Errorf("%s answered as %s is the same model", c[0], c[1])
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
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"id":"openai","models":{"sol":{"id":"sol",`+
		`"cost":{"input":2,"output":8,"cache_read":0.5,"cache_write":2.5}}}}}`), 0o644)
	catalog.Reset()
	t.Cleanup(catalog.Reset)
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

// Overrides are isolated by provider and refresh cached pages when changed.
func TestLedgerConfiguredPricesAndRefresh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0o755)
	os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"models":{"sol":{"id":"sol","cost":{"input":2,"output":8}}}},"discount":{"models":{"sol":{"id":"sol","cost":{"input":1,"output":2}}}}}`), 0o644)
	price := func(v float64) settings.ModelPrice {
		return settings.ModelPrice{Input: new(v), Output: new(v), CacheRead: new(v), CacheWrite: new(v)}
	}
	if err := provider.Save(provider.Provider{ID: "b", Name: "Relay", Key: "k", Chat: "https://relay.example/v1", Catalog: "discount"}); err != nil {
		t.Fatal(err)
	}
	cfg := settings.Settings{ModelPrices: map[string]settings.ModelPrice{"a/sol": price(0), "a/*": price(9), "b/*": price(3)}}
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(Path()), 0o755)
	for _, id := range []string{"a", "b", "unknown"} {
		Append(Record{Time: time.Now(), Provider: id, Model: "sol", Input: 1000000, Status: 200})
	}
	check := func(want map[string]float64) {
		t.Helper()
		rows, sum, _ := Ledger(Month, Filter{})
		page := QueryPage(Month, Filter{}, 0, 20)
		for _, got := range [][]Row{rows, page.Rows} {
			if len(got) != 3 {
				t.Fatalf("rows: %+v", got)
			}
			for _, r := range got {
				if !r.Priced || r.Cost != want[r.Provider] {
					t.Fatalf("%s priced=%v cost=%v want=%v", r.Provider, r.Priced, r.Cost, want[r.Provider])
				}
			}
		}
		total := want["a"] + want["b"] + want["unknown"]
		if sum.Cost != total || page.Sum.Cost != total {
			t.Fatalf("totals: ledger=%v page=%v want=%v", sum.Cost, page.Sum.Cost, total)
		}
	}
	check(map[string]float64{"a": 0, "b": 3, "unknown": 2})
	cfg.ModelPrices["a/sol"] = price(4)
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	check(map[string]float64{"a": 4, "b": 3, "unknown": 2})
	delete(cfg.ModelPrices, "a/sol")
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	check(map[string]float64{"a": 9, "b": 3, "unknown": 2})
	delete(cfg.ModelPrices, "a/*")
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	check(map[string]float64{"a": 2, "b": 3, "unknown": 2})
	delete(cfg.ModelPrices, "b/*")
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	// Removing an override falls back to the relay's catalog, not the maker.
	check(map[string]float64{"a": 2, "b": 1, "unknown": 2})
}

// A row is judged against the id the call went out under, which on an
// Antigravity account is the variant of the model the effort picks and not
// the family it is one level of: a reply naming that variant is the model
// that was asked for, and a name given for the whole provider asks each
// level by its own id, which is the very name the reply names back. Read
// against the family instead, every level of it reads as another model
// having swapped in — in a report a user sets beside a bill.
func TestLedgerJudgesAnAntigravityCallByTheVariantItWentOutUnder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	// Antigravity's ids of one model at three levels, as its fetch left them
	var raw []catalog.Model
	for _, id := range []string{"flash-9-low", "flash-9-medium", "flash-9-high"} {
		raw = append(raw, catalog.Model{ID: id})
	}
	if err := catalog.SaveLive("antigravity", "", raw); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	if err := settings.Save(settings.Settings{ModelWires: map[string]string{"antigravity/*": "vendor-c/*"}}); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(Path()), 0o755)
	now := time.Now()
	for i, c := range []struct{ effort, level string }{
		{"high", "high"}, {"low", "low"},
		// with no effort asked for the family thinks at its default, which is
		// the highest of it
		{"", "high"},
	} {
		Append(Record{Time: now.Add(time.Duration(i) * time.Minute), Provider: "antigravity",
			Host: "antigravity.example", Model: "flash-9", Effort: c.effort,
			Served: "vendor-c/flash-9-" + c.level, Input: 10, Output: 1, Status: 200})
	}
	// a level the call did not go out at is another model answering
	Append(Record{Time: now.Add(4 * time.Minute), Provider: "antigravity",
		Host: "antigravity.example", Model: "flash-9", Effort: "high",
		Served: "vendor-c/flash-9-low", Input: 10, Output: 1, Status: 200})

	rows, sum, _ := Ledger(All, Filter{})
	page := QueryPage(All, Filter{}, 0, 100)
	for _, got := range [][]Row{rows, page.Rows} {
		if len(got) != 4 || !got[0].Swapped || got[1].Swapped || got[2].Swapped || got[3].Swapped {
			t.Fatalf("ledger/page lost effort-aware model comparison: %+v", got)
		}
	}
	if len(rows) != 4 || sum.Calls != 4 {
		t.Fatalf("rows %d, %+v", len(rows), rows)
	}
	// newest first: the swap, then the three levels
	if !rows[0].Swapped {
		t.Errorf("a relay that answered with another level's id is read as no swap: %+v", rows[0])
	}
	for _, row := range rows[1:] {
		if row.Swapped {
			t.Errorf("the call went out at %q and was answered with %q, which is that very model: %+v", row.Effort, row.Served, row)
		}
	}
}

// Antigravity answers a call that went out as a level of a model with the
// model's own name in modelVersion (#462: gemini-3.8-flash at medium went
// out as gemini-3.8-flash-medium and the reply named gemini-3.8-flash),
// which is the model asked for at the level asked for, not another one
// swapped in; a reply naming another level still is.
func TestLedgerAntigravityReplyNamingTheFamilyIsNoSwap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	var raw []catalog.Model
	for _, id := range []string{"flash-9-low", "flash-9-medium", "flash-9-high", "flash-9-extra-low"} {
		raw = append(raw, catalog.Model{ID: id})
	}
	if err := catalog.SaveLive("antigravity", "", raw); err != nil {
		t.Fatal(err)
	}
	catalog.Reset()
	t.Cleanup(catalog.Reset)
	os.MkdirAll(filepath.Dir(Path()), 0o755)
	now := time.Now()
	for i, c := range []struct{ effort, served string }{
		{"medium", "flash-9"}, {"minimal", "flash-9"}, {"", "models/flash-9"}, {"low", "flash-9-low"},
		{"medium", "flash-9-high"}, // another level answered: a swap
	} {
		Append(Record{Time: now.Add(time.Duration(i) * time.Minute), Provider: "antigravity",
			Host: "antigravity.example", Model: "flash-9", Effort: c.effort,
			Served: c.served, Input: 10, Output: 1, Status: 200})
	}
	rows, _, _ := Ledger(All, Filter{})
	page := QueryPage(All, Filter{}, 0, 100)
	for _, got := range [][]Row{rows, page.Rows} {
		if len(got) != 5 {
			t.Fatalf("rows %d: %+v", len(got), got)
		}
		if !got[0].Swapped {
			t.Errorf("another level answering is a swap: %+v", got[0])
		}
		for _, row := range got[1:] {
			if row.Swapped {
				t.Errorf("flash-9 at %q answered as %q is the model asked for: %+v", row.Effort, row.Served, row)
			}
		}
	}
	if !Swapped("flash-9-medium", "flash-8") || !Swapped("flash-9-medium", "flash-9-high") {
		t.Error("another model or level is still a swap")
	}
}

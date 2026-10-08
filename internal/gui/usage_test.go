package gui

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// holdUsageClock stops usage.Clock at at for the test and gives at back, for
// the test to stamp its calls by: midnight then never falls between a call
// and the period it is asked for in.
func holdUsageClock(t *testing.T, at time.Time) time.Time {
	t.Helper()
	old := usage.Clock
	usage.Clock = func() time.Time { return at }
	t.Cleanup(func() { usage.Clock = old })
	return at
}

func TestUsageLedgerDayRoutes(t *testing.T) {
	home := sandboxHome(t)
	if err := os.MkdirAll(filepath.Join(home, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := usage.Today.Since(holdUsageClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))).AddDate(0, 0, -1)
	for i, at := range []time.Time{start.Add(-time.Second), start, start.Add(12 * time.Hour), start.AddDate(0, 0, 1)} {
		usage.Append(usage.Record{Time: at, Agent: "codex", Provider: "relay", Model: "m", Input: 10 + i, Output: 1, Status: 200})
	}
	mux := http.NewServeMux()
	usageRoutes(mux, folderOnly{})
	q := url.Values{"period": {"7d"}, "day": {start.Format(time.DateOnly)}, "limit": {"1"}, "offset": {"1"}, "provider": {"relay"}}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/usage/requests?"+q.Encode(), nil))
	var l ledgerJSON
	if err := json.Unmarshal(w.Body.Bytes(), &l); w.Code != 200 || err != nil {
		t.Fatalf("day: %d %s", w.Code, w.Body)
	}
	if l.Day != q.Get("day") || l.Total != 2 || l.Calls != 2 || l.Input != 23 || len(l.Rows) != 1 || l.Rows[0].Input != 11 {
		t.Fatalf("day's page: %+v", l)
	}
	var calls int
	for _, p := range l.Series {
		calls += p.Calls
	}
	if l.Bucket != "day" || len(l.Series) != 7 || calls != 4 || len(l.ChartBy["provider"]) != 1 || l.ChartBy["provider"][0].Calls != 4 {
		t.Fatalf("the whole chart: %+v", l)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/usage/requests.csv?"+q.Encode(), nil))
	csvData := w.Body.String()
	rows, err := csv.NewReader(w.Body).ReadAll()
	if w.Code != 200 || err != nil || len(rows) != 3 {
		t.Fatalf("day's CSV: %d %s (%v)", w.Code, w.Body, err)
	}
	want := "magpie-requests-day-" + l.Day
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="`+want+`.csv"` {
		t.Fatalf("day's CSV filename: %s", got)
	}
	for _, suffix := range []string{"", "-2"} {
		w = httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/usage/requests/export?"+q.Encode(), nil))
		var out struct {
			Path string
			Rows int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != 200 || err != nil || out.Rows != 2 || out.Path != filepath.Join("~", "Downloads", want+suffix+".csv") {
			t.Fatalf("day's export: %d %s", w.Code, w.Body)
		}
		b, err := os.ReadFile(filepath.Join(home, "Downloads", want+suffix+".csv"))
		if err != nil || string(b) != csvData {
			t.Fatalf("saved CSV differs from the download: %v", err)
		}
	}
	for _, row := range rows[1:] {
		at, err := time.Parse(time.RFC3339Nano, row[0])
		if err != nil || at.In(time.Local).Format(time.DateOnly) != l.Day {
			t.Fatalf("CSV included another day: %v", row)
		}
	}
}

// The Usage page's Requests: a page of the ledger at a time, newest first,
// with the filters' rows counted on every page, and Export CSV writing all
// of them to Downloads, never over an earlier file.
func TestUsageLedgerRoutes(t *testing.T) {
	home := sandboxHome(t)
	if err := os.MkdirAll(filepath.Join(home, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := holdUsageClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	at := usage.Today.Since(now).Add(time.Second)
	for i, r := range []usage.Record{
		{RouteID: 123, Agent: "codex", Provider: "relay", Model: "gpt-6-sol", Requested: "sol", Served: "gpt-6-luna", Input: 10, Output: 1, Status: 200},
		{Agent: "claude", Provider: "anthropic", Model: "claude-sonnet-5", Requested: "sonnet", Input: 20, Output: 2, Status: 200},
		{RouteID: 123, Agent: "codex", Provider: "relay", Model: "gpt-6-sol", Requested: "sol", Status: 429},
	} {
		r.Time = at.Add(time.Duration(i) * time.Second)
		usage.Append(r)
	}
	mux := http.NewServeMux()
	usageRoutes(mux, folderOnly{})
	get := func(q string) ledgerJSON {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/usage/requests?"+q, nil))
		var l ledgerJSON
		if err := json.Unmarshal(w.Body.Bytes(), &l); w.Code != 200 || err != nil {
			t.Fatalf("%s: %d %s", q, w.Code, w.Body)
		}
		return l
	}

	l := get("period=all&route=123")
	if l.Total != 2 || l.Rows[0].RouteID != 123 || l.Rows[1].RouteID != 123 {
		t.Fatalf("route attempts: %+v", l)
	}
	if l = get("period=all&route=999"); l.Total != 0 {
		t.Fatalf("missing route: %+v", l)
	}
	l = get("period=today&offset=1&limit=1")
	if l.Total != 3 || l.Offset != 1 || len(l.Rows) != 1 || l.Rows[0].Requested != "sonnet" {
		t.Fatalf("second page: %+v", l)
	}
	if len(l.Agents) != 2 || l.Calls != 3 || l.Errors != 1 {
		t.Fatalf("agents and totals: %+v", l)
	}
	l = get("period=today")
	if len(l.Rows) != 3 || l.Rows[0].Status != 429 || !l.Rows[2].Swapped || l.Rows[2].Served != "gpt-6-luna" {
		t.Fatalf("newest first, the swap marked: %+v", l.Rows)
	}
	// the chart over them: every row of the filter, by the hour, however the page is cut
	series := func(l ledgerJSON) (calls, in int) {
		for _, p := range l.Series {
			calls, in = calls+p.Calls, in+p.Input
		}
		return
	}
	paged := get("period=today&offset=1&limit=1")
	if calls, in := series(paged); paged.Bucket != "hour" || len(paged.Series) != 24 || calls != 3 || in != 30 {
		t.Fatalf("the chart's hours: %s, %d points, %d calls, %d in", paged.Bucket, len(paged.Series), calls, in)
	}
	if calls, _ := series(get("period=today&agent=claude")); calls != 1 {
		t.Fatalf("the chart follows the filter: %d calls", calls)
	}
	// switching to a provider: its rows and its chart, and every provider still
	// there to switch to, in the filter and in the ranking by provider
	l = get("period=today&provider=relay")
	if l.Total != 2 || l.Calls != 2 || len(l.Providers) != 2 || l.Providers[1].Name != "relay" || l.Providers[1].Icon != "generic" {
		t.Fatalf("one provider: %+v", l)
	}
	if calls, _ := series(l); calls != 2 || len(l.By["provider"]) != 2 || len(l.By["agent"]) != 1 || l.By["agent"][0].ID != "codex" {
		t.Fatalf("its chart and rankings: %d calls, %+v", calls, l.By)
	}
	if by := get("period=today").By; len(by["provider"]) != 2 || by["provider"][0].ID != "anthropic" || by["provider"][0].Calls != 1 || len(by["model"]) != 2 {
		t.Fatalf("the rankings: %+v", by)
	}
	if l = get("period=today&provider=nobody"); l.Total != 0 || len(l.Providers) != 2 || len(l.By["provider"]) != 2 {
		t.Fatalf("an unknown provider: %+v", l)
	}
	if l = get("period=today&failed=1"); l.Total != 1 || l.Rows[0].Status != 429 {
		t.Fatalf("failed only: %+v", l)
	}
	if l = get("period=today&agent=claude"); l.Total != 1 || l.Rows[0].Agent != "claude" {
		t.Fatalf("one agent: %+v", l)
	}
	if l = get("period=today&q=LUNA"); l.Total != 1 || l.Rows[0].Served != "gpt-6-luna" {
		t.Fatalf("search: %+v", l)
	}
	if l = get("period=today&offset=99"); l.Total != 3 || len(l.Rows) != 0 {
		t.Fatalf("past the end: %+v", l)
	}

	export := func() (string, int) {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/usage/requests/export?period=today&agent=codex", strings.NewReader("{}")))
		var out struct {
			Path string
			Rows int
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != 200 || err != nil {
			t.Fatalf("export: %d %s", w.Code, w.Body)
		}
		return out.Path, out.Rows
	}
	p1, n := export()
	p2, _ := export()
	day := now.Format("2006-01-02")
	if n != 2 || p1 != filepath.Join("~", "Downloads", "magpie-requests-today-"+day+".csv") || p2 != filepath.Join("~", "Downloads", "magpie-requests-today-"+day+"-2.csv") {
		t.Fatalf("export: %s %s %d", p1, p2, n)
	}
	b, err := os.ReadFile(filepath.Join(home, "Downloads", "magpie-requests-today-"+day+".csv"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "time,agent,requested_model,") || !strings.Contains(lines[2], ",sol,relay,,gpt-6-sol,gpt-6-luna,true,") {
		t.Fatalf("csv:\n%s", b)
	}
}

// What was said in a request comes from the agent's session file when the
// row is opened: a call the file has by its time, or a gateway request by the
// span it took; and why not when it can't be told — no session named, an agent
// whose files magpie doesn't read, a call the files don't have.
func TestUsageRequestContent(t *testing.T) {
	home := sandboxHome(t)
	dir := filepath.Join(home, ".claude", "projects", "-work-app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	at := func(s int) string { return base.Add(time.Duration(s) * time.Second).Format("2006-01-02T15:04:05.000Z") }
	lines := []string{
		`{"type":"user","timestamp":"` + at(1) + `","sessionId":"sess1","message":{"role":"user","content":"What is 2+2?"}}`,
		`{"type":"assistant","timestamp":"` + at(3) + `","sessionId":"sess1","requestId":"req_a","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"text","text":"Four."}],"usage":{"input_tokens":5,"output_tokens":2}}}`,
	}
	if err := os.WriteFile(filepath.Join(dir, "sess1.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	usageRoutes(mux, folderOnly{})
	get := func(agent, session string, from, to time.Time) (contentJSON, int) {
		t.Helper()
		q := url.Values{"agent": {agent}, "session": {session}, "from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/usage/requests/content?"+q.Encode(), nil))
		var c contentJSON
		if w.Code == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
				t.Fatalf("%s", w.Body)
			}
		}
		return c, w.Code
	}
	call := base.Add(3 * time.Second)
	// a call of the file, by its own time
	c, _ := get("claude-desktop", "sess1", call.Add(-time.Millisecond), call.Add(time.Millisecond))
	if !c.Found || c.Model != "claude-opus-5-5" || len(c.Input) != 1 || c.Input[0].Text != "What is 2+2?" || len(c.Output) != 1 || c.Output[0].Text != "Four." {
		t.Fatalf("a call by its time: %+v", c)
	}
	// a request the gateway logged, began a little before the file wrote the call
	if c, _ = get("claude", "sess1", base.Add(time.Second), base.Add(35*time.Second)); !c.Found || c.Output[0].Text != "Four." {
		t.Fatalf("by the span of the request: %+v", c)
	}
	for _, x := range []struct {
		agent, session, why string
		from, to            time.Time
	}{
		{"claude", "", "session", call, call},
		{"Bob", "sess1", "agent", call, call},
		{"claude", "nobody", "missing", call, call},
		{"claude", "sess1", "missing", call.Add(time.Hour), call.Add(2 * time.Hour)},
	} {
		if c, _ = get(x.agent, x.session, x.from, x.to); c.Found || c.Why != x.why || c.Input == nil || c.Output == nil {
			t.Errorf("%s/%s: found %v, why %q, want %q", x.agent, x.session, c.Found, c.Why, x.why)
		}
	}
	if _, code := get("claude", "sess1", call, call); code != 200 {
		t.Fatal(code)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/usage/requests/content?agent=claude&session=s&from=now&to=never", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("times that are not: %d", w.Code)
	}
}

// Provider/agent facets come from the same snapshot as rows, even with both
// filters set. Calls without recorded routing are shown as local sessions.
func TestLedgerSingleSnapshotAndLocalSession(t *testing.T) {
	sandboxHome(t)
	now := holdUsageClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	before := usage.LogCalls
	reads := 0
	usage.LogCalls = func(time.Time) []sessions.Call {
		reads++
		return []sessions.Call{{Time: now, Agent: "claude", Session: "s", Model: "claude-sonnet-5", Tokens: sessions.Tokens{Input: 10, Output: 2}}}
	}
	t.Cleanup(func() { usage.LogCalls = before })
	l := ledgerPage(usage.Today, usage.Filter{Provider: usage.UnknownProvider, Agent: "claude"}, 0, 1)
	if reads != 1 {
		t.Fatalf("logs loaded %d times", reads)
	}
	if l.Total != 1 || l.Rows[0].ProviderName != "Local session" || len(l.Providers) != 1 || l.Providers[0].Name != "Local session" || l.By["provider"][0].Name != "Local session" {
		t.Fatalf("local session names %+v", l)
	}
	if l.Rows[0].Provider != usage.UnknownProvider || l.Rows[0].Host != "" {
		t.Fatalf("model must not establish route/account: %+v", l.Rows[0])
	}
}

// A model at a provider is named by both, with the provider's icon, so
// one model's speed at each provider reads apart (inaction on Discord).
func TestLedgerNamesModelAtProvider(t *testing.T) {
	sandboxHome(t)
	now := holdUsageClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	before := usage.LogCalls
	usage.LogCalls = func(time.Time) []sessions.Call {
		return []sessions.Call{{Time: now, Agent: "claude", Session: "s", Model: "claude-sonnet-5", Tokens: sessions.Tokens{Input: 10, Output: 2}}}
	}
	t.Cleanup(func() { usage.LogCalls = before })
	l := ledgerPage(usage.Today, usage.Filter{}, 0, 1)
	at := l.By["modelAt"]
	if len(at) != 1 || at[0].ID != usage.ModelAtKey(usage.UnknownProvider, "claude-sonnet-5") || at[0].Name != "claude-sonnet-5 · Local session" {
		t.Fatalf("model at provider %+v", at)
	}
}

func TestCSVStamp(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	for _, day := range []string{"", "2026-02-30", "../../other", "2026-09-30\""} {
		want := "magpie-requests-7d-2026-09-30"
		if got := csvStamp(usage.Period("7d"), day, now); got != want {
			t.Fatalf("%q: %s", day, got)
		}
	}
}

// midnightUsageClock sets usage.Clock to read a tenth of a second before
// midnight the first time and a tenth of a second after it from then on, as
// when midnight falls while an answer is worked out. It is set again for
// each answer.
func midnightUsageClock(t *testing.T, midnight time.Time) {
	t.Helper()
	var read atomic.Bool
	old := usage.Clock
	usage.Clock = func() time.Time {
		if read.Swap(true) {
			return midnight.Add(100 * time.Millisecond)
		}
		return midnight.Add(-100 * time.Millisecond)
	}
	t.Cleanup(func() { usage.Clock = old })
}

// A period's CSV asked for as midnight falls is named for the day of the
// calls in it, downloaded or saved to Downloads: its rows and its name are
// read at one moment, not the rows of one day under the next day's name.
func TestUsageCSVAtMidnight(t *testing.T) {
	home := sandboxHome(t)
	if err := os.MkdirAll(filepath.Join(home, "Downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	midnight := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)
	usage.Append(usage.Record{Time: midnight.Add(-time.Minute), Agent: "codex", Provider: "relay", Model: "m", Input: 10, Output: 1, Status: 200})
	mux := http.NewServeMux()
	usageRoutes(mux, folderOnly{})
	want := "magpie-requests-today-2026-09-30.csv"

	midnightUsageClock(t, midnight)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/usage/requests.csv?period=today", nil))
	rows, err := csv.NewReader(w.Body).ReadAll()
	if got := w.Header().Get("Content-Disposition"); w.Code != 200 || err != nil || len(rows) != 2 || got != `attachment; filename="`+want+`"` {
		t.Errorf("download: %d, %s with %d rows (%v), want %s with 2", w.Code, got, len(rows), err, want)
	}

	midnightUsageClock(t, midnight)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/usage/requests/export?period=today", nil))
	var out struct {
		Path string
		Rows int
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != 200 || err != nil || out.Rows != 1 || out.Path != filepath.Join("~", "Downloads", want) {
		t.Errorf("export: %d %s, want %s with 1 row", w.Code, w.Body, want)
	}
}

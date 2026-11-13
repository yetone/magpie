package usage

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/probelog"
	"github.com/yetone/magpie/internal/provider"
)

// ledgerSpy is an Observer that only counts the rows Append hands it.
type ledgerSpy struct{ rows []Record }

func (*ledgerSpy) Serve() (stop func()) { return func() {} }
func (*ledgerSpy) Bodies() Bodies       { var b Bodies; return b }
func (*ledgerSpy) Begin(Request) Watch  { return nil }
func (s *ledgerSpy) Ledger(r Record)    { s.rows = append(s.rows, r) }

// A request magpie sends a provider on its own (the editor's Test, Detect
// APIs, a model list) is a row of the request log, marked as magpie's own
// with what it was for, and counts in nothing: no total, source, group,
// summary, Load, LastSeen, shared day or observer (neiko, gys619).
func TestProbeRowsListedNotCounted(t *testing.T) {
	reportedHome(t)
	spy := &ledgerSpy{}
	t.Cleanup(Observe(spy))
	now := time.Now().Add(-time.Hour)
	Append(Record{Time: now, Agent: "claude", Provider: "anthropic", Model: "claude-opus-5-5", Input: 1000, Output: 100, Status: 200})
	probelog.Sink(probelog.Row{Time: now.Add(time.Second), Kind: probelog.KindTest, Provider: "relay", Account: "me", Model: "gpt-9",
		Host: "relay.example", Endpoint: "/v1/chat/completions", Status: 401, Millis: 120, Error: strings.Repeat("页", 400)})
	probelog.Sink(probelog.Row{Time: now.Add(2 * time.Second), Kind: probelog.KindList, Provider: "relay", Host: "relay.example", Endpoint: "GET /v1/models", Status: 502, Millis: 30, Error: "bad gateway"})

	page := QueryPage(All, Filter{}, 0, 50)
	if page.Total != 3 || len(page.Rows) != 3 {
		t.Fatalf("total %d, %d rows: %+v", page.Total, len(page.Rows), page.Rows)
	}
	var probes []Row
	for _, r := range page.Rows {
		if r.Source == SourceProbe {
			probes = append(probes, r)
		}
	}
	if len(probes) != 2 {
		t.Fatalf("probe rows %+v", page.Rows)
	}
	test := probes[1] // newest first
	if probes[0].Kind != probelog.KindList || test.Kind != probelog.KindTest || test.Agent != "magpie" || test.Provider != "relay" ||
		test.ProviderAccount != "me" || test.Model != "gpt-9" || test.Status != 401 || test.Endpoint != "/v1/chat/completions" {
		t.Fatalf("probe rows %+v", probes)
	}
	if len(test.Error) > 504 || !strings.HasSuffix(test.Error, "…") || !strings.HasPrefix(test.Error, "页") {
		t.Fatalf("error not clipped at a character: %d bytes", len(test.Error))
	}
	if page.Sum.Calls != 1 || page.Sum.Errors != 0 || page.Through.Calls != 1 || page.Direct.Calls != 0 || page.Reported.Calls != 0 {
		t.Fatalf("sum %+v through %+v direct %+v", page.Sum, page.Through, page.Direct)
	}
	for dim, shares := range page.By {
		for _, s := range shares {
			if s.ID == "relay" || s.ID == "magpie" || s.ID == "gpt-9" {
				t.Fatalf("%s counts a probe: %+v", dim, shares)
			}
		}
	}
	if s := Summarize(All); s.Calls != 1 || s.Errors != 0 {
		t.Fatalf("summary %+v", s.Totals)
	}
	if rs := Load(time.Time{}); len(rs) != 1 {
		t.Fatalf("Load %+v", rs)
	}
	if !LastSeen("magpie").IsZero() {
		t.Fatal("a probe is a request from agent magpie")
	}
	n := 0
	for _, calls := range OwnDays(now.Add(-time.Hour)) {
		n += len(calls)
	}
	if n != 1 {
		t.Fatalf("%d shared calls", n)
	}
	if len(spy.rows) != 1 || spy.rows[0].IsProbe() {
		t.Fatalf("observer saw %+v", spy.rows)
	}
	// a filter on the probe's provider lists it and still counts nothing
	if p := QueryPage(All, Filter{Provider: "relay"}, 0, 50); p.Total != 2 || p.Sum.Calls != 0 {
		t.Fatalf("provider filter: total %d sum %+v", p.Total, p.Sum)
	}
}

// ferock on Discord: Usage > Requests filled with "Model list" rows, 0
// tokens and 200, one every 10 minutes for a Remote magpie, that no agent
// sent. A model list that was answered is no row, neither written nor
// read back from a log that has it from before; one that failed is still
// listed (it is why models are missing), and so is every Test.
func TestAnsweredModelListIsNoRow(t *testing.T) {
	reportedHome(t)
	now := time.Now().Add(-time.Hour)
	list := func(at time.Duration, status int, err string) probelog.Row {
		return probelog.Row{Time: now.Add(at), Kind: probelog.KindList, Provider: "remote", Host: "10.0.0.2:3425",
			Endpoint: "GET /v1/models", Status: status, Millis: 757, Error: err}
	}
	probelog.Sink(list(time.Second, 200, ""))
	probelog.Sink(list(2*time.Second, 304, ""))
	probelog.Sink(list(3*time.Second, 503, "upstream down"))
	probelog.Sink(list(4*time.Second, 0, "dial tcp 10.0.0.2:3425: connection refused"))
	probelog.Sink(probelog.Row{Time: now.Add(5 * time.Second), Kind: probelog.KindTest, Provider: "remote", Host: "10.0.0.2:3425",
		Endpoint: "/v1/chat/completions", Model: "m", Status: 200, Millis: 900})
	// a log written before: its answered lists are left out as it is read
	Append(Record{Time: now.Add(6 * time.Second), Agent: "magpie", Provider: "remote", Host: "10.0.0.2:3425",
		Endpoint: "GET /v1/models", Kind: probelog.KindList, Status: 200, Millis: 700, Source: SourceProbe})

	b, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), `"kind":"model_list"`); n != 3 {
		t.Fatalf("%d model list lines written, want the 2 that failed and the old one:\n%s", n, b)
	}
	page := QueryPage(All, Filter{}, 0, 50)
	var got []string
	for _, r := range page.Rows {
		got = append(got, r.Kind+" "+strconv.Itoa(r.Status))
	}
	want := []string{"provider_test 200", "model_list 0", "model_list 503"}
	if page.Total != 3 || !slices.Equal(got, want) {
		t.Fatalf("total %d, rows %v, want %v", page.Total, got, want)
	}
	if p := QueryPage(All, Filter{Failed: true}, 0, 50); p.Total != 2 {
		t.Fatalf("failed filter: total %d", p.Total)
	}
}

// The request page asks for the probes on every poll; while none was
// made and no host or rename changed, the chunk made last time is the
// answer, and any of those changing makes it again (#2278).
func TestProbeChunkMadeOncePerChange(t *testing.T) {
	at := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	rows := probeRows{}
	for i := range 3 {
		rows.add(Record{Time: at.Add(time.Duration(i) * time.Minute), Agent: "magpie", Provider: "p", Host: "api.example.com", Status: 500, Source: SourceProbe}, int64(i))
	}
	blocks := rows.done()
	hosts := map[string]string{"api.example.com": "p"}
	first := probeChunk(blocks, at, provider.Renames{}, hosts)
	if first == nil || first.Count != 3 {
		t.Fatalf("first: %+v", first)
	}
	if again := probeChunk(slices.Clone(blocks), at, provider.Renames{}, map[string]string{"api.example.com": "p"}); again != first {
		t.Fatal("the same probes, since, renames and hosts made the chunk again")
	}
	if c := probeChunk(blocks, at.Add(time.Minute), provider.Renames{}, hosts); c == first || c.Count != 2 {
		t.Fatalf("a later since kept the old chunk: %+v", c)
	}
	if c := probeChunk(blocks, at, provider.Renames{}, map[string]string{"api.example.com": "q"}); c == first {
		t.Fatal("a changed host kept the old chunk")
	}
	more := probeRows{blocks: slices.Clone(blocks)}
	more.add(Record{Time: at.Add(5 * time.Minute), Agent: "magpie", Provider: "p", Host: "api.example.com", Status: 500, Source: SourceProbe}, 3)
	if c := probeChunk(more.done(), at, provider.Renames{}, hosts); c == first || c.Count != 4 {
		t.Fatalf("a new probe kept the old chunk: %+v", c)
	}
}

// The probe chunk is let go when the pages rest and after the warm-up:
// it was held for the process's life, ~5 MB of unpacked rows.
func TestProbeChunkLetGoAtRest(t *testing.T) {
	at := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	rows := probeRows{}
	rows.add(Record{Time: at, Agent: "magpie", Provider: "p", Host: "api.example.com", Status: 500, Source: SourceProbe}, 0)
	blocks := rows.done()
	for name, release := range map[string]func(){"ReleaseScratch": ReleaseScratch, "ReleaseMatchScratch": ReleaseMatchScratch} {
		probeChunk(blocks, at, provider.Renames{}, nil)
		release()
		probeMemo.Lock()
		held := probeMemo.last != nil
		probeMemo.Unlock()
		if held {
			t.Fatalf("%s kept the probe chunk", name)
		}
	}
}

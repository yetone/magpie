package usage

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

func pageHome(t *testing.T) {
	sessionHome(t)
	sessions.Reset()
	catalog.Reset()
	// a page's last read can start a write of its chunks into the HOME's
	// .cache/magpie; it ends before TempDir's cleanup removes the folder
	// (registered after it, so run before it), or the removal finds the
	// folder not empty (#4390)
	t.Cleanup(func() { requestDiskSaves.Wait(); sessions.Reset(); catalog.Reset() })
	os.MkdirAll(filepath.Dir(catalog.CachePath()), 0700)
	os.WriteFile(catalog.CachePath(), []byte(`{"openai":{"id":"openai","models":{"m":{"id":"m","cost":{"input":2,"output":8,"cache_read":0.5,"cache_write":2.5}}}}}`), 0600)
}
func equalPage(t *testing.T, got, want RequestPage) {
	t.Helper()
	// Summation order changes without a global sort; tolerate float round-off.
	var normalize func(reflect.Value)
	normalize = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() {
				normalize(v.Elem())
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if v.Field(i).CanSet() {
					normalize(v.Field(i))
				}
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				normalize(v.Index(i))
			}
		case reflect.Map:
			for _, k := range v.MapKeys() {
				x := reflect.New(v.Type().Elem()).Elem()
				x.Set(v.MapIndex(k))
				normalize(x)
				v.SetMapIndex(k, x)
			}
		case reflect.Float64:
			v.SetFloat(math.Round(v.Float()*1e9) / 1e9)
		}
	}
	normalize(reflect.ValueOf(&got).Elem())
	normalize(reflect.ValueOf(&want).Elem())
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		t.Fatalf("page differs\ngot %s\nwant %s", a, b)
	}
}

func TestCompactPageMatchesLedger(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	var recs []Record
	var logs []sessions.Call
	for i := 0; i < 180; i++ {
		at := Today.Since(now).AddDate(0, 0, -i%80).Add(time.Duration(i%24) * time.Hour)
		r := Record{Time: at, Agent: []string{"claude", "codex", "opencode"}[i%3], Provider: []string{"a", "b"}[i%2], Model: "m", Input: 10 + i, Output: 2, CacheRead: 3, Status: 200, FirstText: 7, TTFT: 5, Millis: 100, Session: fmt.Sprint(i), RequestID: fmt.Sprintf("r%d", i), NativeSession: fmt.Sprint(i)}
		if i%9 == 0 {
			r.Status = 429
			r.Error = "limited"
		}
		if i%19 == 0 {
			r.Rejected = true
		}
		recs = append(recs, r)
		c := sessions.Call{Time: at.Add(time.Second), Agent: r.Agent, Session: r.Session, Model: "m", Tokens: sessions.Tokens{Input: r.Input, Output: r.Output, CacheRead: r.CacheRead}, File: "/session", To: int64(i + 1)}
		switch i % 5 {
		case 0:
			c.RequestID = r.RequestID
		case 1:
			c.RequestID = ""
		case 2:
			c.RequestID = "different"
		default:
			c.Session = "local"
		}
		if r.Failed() {
			c.Error = "rate_limit"
			c.ErrorText = "limited"
		}
		logs = append(logs, c)
	}
	// Same timestamp keeps gateway rows first, newest file order within locals.
	logs = append(logs, sessions.Call{Time: recs[0].Time, Agent: "claude", Session: "tied", Model: "m", Tokens: sessions.Tokens{Input: 1}, File: "/session", To: 999})
	slices.SortStableFunc(logs, func(a, b sessions.Call) int {
		if n := b.Time.Compare(a.Time); n != 0 {
			return n
		}
		return int(b.To - a.To)
	})
	price := Pricer()
	pack := func(r Record, source string) Row {
		x := Row{Record: r, Source: source, Swapped: r.Served != "" && Swapped(r.Model, r.Served)}
		if p := price(r); p != nil && r.Input+r.Output > 0 {
			x.Cost = p.Cost(r.Input, r.Output, r.CacheRead, r.CacheWrite)
			x.Priced = true
		}
		x.Agent = AgentOf(r.Agent)
		return x
	}
	gateway := &rowChunk{}
	for i, r := range recs {
		gateway.add(pack(r, ""), "", int64(i), false)
	}
	local := &rowChunk{Source: sessions.CallSource{Path: "/session"}}
	for _, c := range logs {
		local.add(pack(logRecord(c), "log"), "", c.To, c.Error != "")
	}
	for _, period := range []Period{Today, Week, Month, All} {
		since := period.Since(now)
		gs := since
		if !gs.IsZero() {
			gs = gs.Add(-24 * time.Hour)
		}
		rs := slices.DeleteFunc(slices.Clone(recs), func(r Record) bool { return r.Time.Before(gs) })
		cs := slices.DeleteFunc(slices.Clone(logs), func(c sessions.Call) bool { return c.Time.Before(since) })
		rows, sum, agents, providers := ledgerWith(since, Filter{}, rs, cs)
		all := Ledgered{rows, sum, agents, providers}
		day := now.AddDate(0, 0, -1).Format(time.DateOnly)
		for _, f := range []Filter{{}, {Agent: "claude"}, {Provider: "a"}, {Failed: true}, {Query: "LOCAL"}, {Agent: "codex", Provider: UnknownProvider}, {Query: "no match"}, {Day: day}, {Day: day, Provider: "a"}, {Day: day, Model: "m", Failed: true}, {Day: "1900-01-01"}, {Through: SourceGateway}, {Through: SourceSession}} {
			for _, offset := range []int{0, 7, 500, int(^uint(0) >> 1)} {
				t.Run(fmt.Sprintf("%s/%+v/%d", period, f, offset), func(t *testing.T) {
					equalPage(t, buildRequestPage(period, f, offset, 7, gateway, []*rowChunk{local}), pageFromLedger(period, f, offset, 7, all))
				})
			}
		}
	}
}

func TestRequestPageModelRankingKeepsAlternatives(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	records := []Record{
		{Time: now, Provider: "a", Agent: "codex", Model: "gpt-5", Input: 10},
		{Time: now, Provider: "a", Agent: "codex", Model: "gpt-5-mini", Input: 20},
		{Time: now, Provider: "b", Agent: "codex", Model: "other-provider", Input: 30},
		{Time: now, Provider: "a", Agent: "claude", Model: "other-agent", Input: 40},
		{Time: now, Provider: "a", Agent: "codex", Model: "rejected-model", Rejected: true},
	}
	gateway := &rowChunk{}
	all := Ledgered{}
	for i, r := range records {
		row := Row{Record: r}
		gateway.add(row, "", int64(i), false)
		all.Rows = append(all.Rows, row)
	}
	f := Filter{Provider: "a", Agent: "codex", Model: "gpt-5"}
	for name, page := range map[string]RequestPage{
		"compact": buildRequestPage(Today, f, 0, 100, gateway, nil),
		"ledger":  pageFromLedger(Today, f, 0, 100, all),
	} {
		t.Run(name, func(t *testing.T) {
			models := map[string]int{}
			for _, share := range page.By["model"] {
				models[share.ID] = share.Calls
			}
			if want := map[string]int{"gpt-5": 1, "gpt-5-mini": 1}; !reflect.DeepEqual(models, want) {
				t.Fatalf("model ranking = %v, want %v", models, want)
			}
			if page.Total != 1 || len(page.Rows) != 1 || page.Rows[0].Model != "gpt-5" || page.Sum.Calls != 1 || page.Sum.Input != 10 {
				t.Fatalf("selected model no longer filters rows and totals: %+v", page)
			}
			var calls, tokens int
			for _, point := range page.Series {
				calls += point.Calls
				tokens += point.Input
			}
			if calls != 1 || tokens != 10 {
				t.Fatalf("selected model no longer filters the chart: calls=%d input=%d", calls, tokens)
			}
			for _, dimension := range []string{"provider", "agent"} {
				if shares := page.By[dimension]; len(shares) != 1 || shares[0].Calls != 1 {
					t.Fatalf("%s ranking ignored the model filter: %+v", dimension, shares)
				}
			}
		})
	}
}

func TestQueryPageSourceAndIdentityInvalidation(t *testing.T) {
	pageHome(t)
	// QueryPage saves a copy of the account Codex is signed in to, at most
	// once every 30s (rememberLogins), and this test expects only auth.json's
	// identity. Look at the accounts before signing in, as an earlier test in
	// a package run does, so no copy is saved while it runs.
	provider.ForgetAccounts()
	provider.Accounts()
	sessionAuth(t, sessions.CodexDir(), "a", "u", "one@example.com")
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	path := filepath.Join(sessions.CodexDir(), "sessions", "rollout-2026-09-30T00-00-00-test.jsonl")
	os.MkdirAll(filepath.Dir(path), 0700)
	meta := `{"type":"session_meta","payload":{"id":"test","model_provider":"custom","creator_account_id":"a","creator_user_id":"u"}}` + "\n" + `{"type":"turn_context","payload":{"model":"m","effort":"high"}}` + "\n"
	line := func(n int) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"output_tokens":%d},"last_token_usage":{"input_tokens":10,"output_tokens":1}}}}`+"\n", now.Format(time.RFC3339Nano), n*10, n)
	}
	os.WriteFile(path, []byte(meta+line(1)), 0600)
	check := func(n int, account string, official bool) {
		t.Helper()
		p := QueryPage(All, Filter{}, 0, 100)
		if p.Total != n {
			t.Fatalf("total %d want %d", p.Total, n)
		}
		for _, r := range p.Rows {
			if r.Source == "log" && (r.SessionAccount != account || r.SessionOfficialLogin != official) {
				t.Fatalf("identity %+v", r)
			}
		}
		equalPage(t, p, pageFromLedger(All, Filter{}, 0, 100, LedgerOf(All, Filter{})))
	}
	check(1, "one@example.com", true)
	before := requestCache.chunks[path]
	check(1, "one@example.com", true)
	if requestCache.chunks[path] != before {
		t.Fatal("unchanged source rebuilt")
	}
	appendFile := func(p, s string) {
		f, e := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0600)
		if e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		f.WriteString(s)
	}
	appendFile(path, line(2))
	check(2, "one@example.com", true)
	sessionAuth(t, sessions.CodexDir(), "a", "u", "two@example.com")
	check(2, "two@example.com", true)
	sessionAuth(t, sessions.CodexDir(), "other", "u", "wrong@example.com")
	check(2, "", false)
	os.MkdirAll(filepath.Dir(Path()), 0700)
	r, _ := json.Marshal(Record{Time: now, Model: "m", Agent: "opencode", Input: 10, Output: 1})
	os.WriteFile(Path(), r, 0600)
	check(2, "", false) // partial gateway line
	appendFile(Path(), "\n")
	check(3, "", false)
	appendFile(Path(), string(r)+"\n")
	check(4, "", false)
	os.WriteFile(Path(), []byte(string(r)+"\n"), 0600)
	check(3, "", false)
	// Reprice cached rows when the shared catalogue changes.
	b, _ := os.ReadFile(catalog.CachePath())
	os.WriteFile(catalog.CachePath(), []byte(strings.Replace(string(b), `"input":2`, `"input":9`, 1)), 0600)
	catalog.Reset()
	check(3, "", false)
	os.Remove(path)
	check(1, "", false)
	os.Remove(Path())
	check(0, "", false)
}

// One model at two providers is ranked once at each, with each one's own
// first token and decode, and a provider picked still ranks the other
// (inaction on Discord).
func TestRequestPageRanksModelAtEachProvider(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	records := []Record{
		{Time: now, Provider: "zhipu", Agent: "claude", Model: "glm-5.3", Output: 1000, Millis: 2200, TTFT: 200},
		{Time: now, Provider: "zhipu", Agent: "claude", Model: "glm-5.3", Output: 1000, Millis: 2200, TTFT: 200},
		{Time: now, Provider: "zcode", Agent: "claude", Model: "glm-5.3", Output: 1000, Millis: 10800, TTFT: 800},
		{Time: now, Provider: "zcode", Agent: "claude", Model: "kimi-k3", Output: 10, Millis: 100, TTFT: 50},
	}
	gateway := &rowChunk{}
	all := Ledgered{}
	for i, r := range records {
		row := Row{Record: r}
		gateway.add(row, "", int64(i), false)
		all.Rows = append(all.Rows, row)
	}
	f := Filter{Provider: "zhipu", Model: "glm-5.3"}
	for name, page := range map[string]RequestPage{
		"compact": buildRequestPage(Today, f, 0, 100, gateway, nil),
		"ledger":  pageFromLedger(Today, f, 0, 100, all),
	} {
		t.Run(name, func(t *testing.T) {
			got := map[string]Share{}
			for _, s := range page.By["modelAt"] {
				got[s.ID] = s
			}
			fast, slow := got[ModelAtKey("zhipu", "glm-5.3")], got[ModelAtKey("zcode", "glm-5.3")]
			if len(got) != 3 || fast.Calls != 2 || slow.Calls != 1 || got[ModelAtKey("zcode", "kimi-k3")].Calls != 1 {
				t.Fatalf("model at each provider = %+v, want glm-5.3 at zhipu (2) and at zcode (1), kimi-k3 at zcode (1)", page.By["modelAt"])
			}
			if fast.Timed != 2 || fast.TTFT != 400 || slow.Timed != 1 || slow.TTFT != 800 {
				t.Fatalf("first tokens mixed between providers: %+v %+v", fast, slow)
			}
			if fast.DecodeMs == 0 || slow.DecodeMs == 0 || fast.DecodeOut*1000/int(fast.DecodeMs) <= slow.DecodeOut*1000/int(slow.DecodeMs) {
				t.Fatalf("decode speeds mixed between providers: %+v %+v", fast, slow)
			}
			if page.Total != 2 {
				t.Fatalf("the provider and model picked no longer filter the requests: %d", page.Total)
			}
		})
	}
}

// A Claude message written again in another file (a resumed or forked
// session's copy) is counted once, at its first file, and only it: a
// message id seen once, or none, is never left out.
func TestVisibleLocalLeavesOutRepeatedMessages(t *testing.T) {
	a, b := &rowChunk{}, &rowChunk{}
	a.add(Row{}, "msg_1", 0, false)
	a.add(Row{}, "msg_2", 1, false)
	b.add(Row{}, "msg_1", 0, false)
	b.add(Row{}, "", 1, false)
	b.add(Row{}, "msg_3", 2, false)
	b.add(Row{}, "msg_2", 3, false)
	a, b = a.pack(), b.pack()
	got := refMap(visibleLocal([]*rowChunk{a, b}))
	if len(got) != 2 || !got[rowRef{b, 0}] || !got[rowRef{b, 3}] {
		t.Fatalf("left out %v, want b's rows 0 and 3 only", got)
	}
}

// A chunk's message ids are read once while its serial stands: a pairing
// decoded every session file of the period for them on every poll
// (#2278), and what is left out is the same either way.
func TestVisibleLocalKeptBySerial(t *testing.T) {
	a, b, c := &rowChunk{}, &rowChunk{}, &rowChunk{}
	a.add(Row{}, "msg_1", 0, false)
	a.add(Row{}, "msg_2", 1, false)
	b.add(Row{}, "msg_2", 0, false)
	b.add(Row{}, "msg_3", 1, false)
	c.add(Row{}, "msg_3", 0, false)
	c.add(Row{}, "msg_1", 1, false)
	a, b, c = a.pack(), b.pack(), c.pack()
	a.serial, b.serial = nextChunkSerial(), nextChunkSerial()
	opened := 0
	msgKeysOpening = func(*rowChunk) { opened++ }
	t.Cleanup(func() { msgKeysOpening = nil })
	want := map[rowRef]bool{{b, 0}: true, {c, 0}: true, {c, 1}: true}
	for i, open := range []int{3, 1, 1} {
		opened = 0
		got := refMap(visibleLocal([]*rowChunk{a, b, c}))
		if !maps.Equal(got, want) {
			t.Fatalf("call %d left out %v, want %v", i, got, want)
		}
		if opened != open {
			t.Fatalf("call %d decoded %d chunks, want %d (c has no serial)", i, opened, open)
		}
	}
	// in another order, the kept keys are read in that order
	opened = 0
	got := refMap(visibleLocal([]*rowChunk{c, b, a}))
	if want := map[rowRef]bool{{b, 1}: true, {a, 0}: true, {a, 1}: true}; !maps.Equal(got, want) || opened != 1 {
		t.Fatalf("reversed: left out %v (decoded %d), want %v", got, opened, want)
	}
}

// A settled chunk's message ids are read for each pairing and not kept:
// a cold start's pairing of the whole history kept every one, ~67 MB of
// its live heap (#2278). What is left out is the same as when they are.
func TestVisibleLocalSettledNotKept(t *testing.T) {
	old := time.Now().AddDate(0, 0, -30)
	a, b := &rowChunk{}, &rowChunk{}
	a.add(Row{Record: Record{Time: old}}, "msg_1", 0, false)
	a.add(Row{Record: Record{Time: old}}, "msg_2", 1, false)
	b.add(Row{}, "msg_2", 0, false)
	b.add(Row{}, "msg_3", 1, false)
	a, b = a.pack(), b.pack()
	a.Latest = old
	a.serial, b.serial = nextChunkSerial(), nextChunkSerial()
	for i := range 3 {
		got := refMap(visibleLocal([]*rowChunk{a, b}))
		if want := map[rowRef]bool{{b, 0}: true}; !maps.Equal(got, want) {
			t.Fatalf("call %d left out %v, want %v", i, got, want)
		}
	}
	msgKeys.Lock()
	_, kept := msgKeys.by[a.serial]
	_, keptB := msgKeys.by[b.serial]
	msgKeys.Unlock()
	if kept || !keptB {
		t.Fatalf("settled chunk's keys kept %v, recent chunk's %v; want false, true", kept, keptB)
	}
}

// The ids one call saw are kept as an empty set for the next (#2278): a
// call after another, on other chunks, leaves out what a first call does,
// and ReleaseScratch lets the set go.
func TestVisibleLocalSeenKeptEmpty(t *testing.T) {
	a, b := &rowChunk{}, &rowChunk{}
	a.add(Row{}, "msg_1", 0, false)
	a.add(Row{}, "msg_2", 1, false)
	b.add(Row{}, "msg_2", 0, false)
	b.add(Row{}, "msg_3", 1, false)
	a, b = a.pack(), b.pack()
	t.Cleanup(ReleaseScratch)
	ReleaseScratch()
	if got := refMap(visibleLocal([]*rowChunk{a, b})); len(got) != 1 || !got[rowRef{b, 0}] {
		t.Fatalf("left out %v, want b's row 0", got)
	}
	kept := keptSeen.Load()
	if kept == nil || len(kept.ids) != 0 {
		t.Fatal("the ids seen weren't kept empty")
	}
	// b alone repeats nothing; the last call's msg_2 doesn't stand
	if got := refMap(visibleLocal([]*rowChunk{b})); len(got) != 0 {
		t.Fatalf("after another call left out %v", got)
	}
	if keptSeen.Load() != kept {
		t.Fatal("a call right after made a set of its own")
	}
	ReleaseScratch()
	if keptSeen.Load() != nil {
		t.Fatal("ReleaseScratch kept the ids seen")
	}
}

// A page read again after a call takes each chunk's newest rows from its
// part, refs and rows: decoding the newest chunks for every page was
// ~390 MB of 90s while an agent worked, a long session's file among them,
// and as much again to read the page's rows (#2278). A page that wants
// more rows than a chunk gave reads it again.
func TestNewestKeptOnPart(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.Local)
	a, b := &rowChunk{}, &rowChunk{}
	for i := range 3 {
		a.add(Row{Record: Record{Time: at.Add(time.Duration(i) * time.Minute), Model: fmt.Sprintf("a%d", i)}}, "", int64(i), false)
		b.add(Row{Record: Record{Time: at.Add(time.Hour + time.Duration(i)*time.Minute), Model: fmt.Sprintf("b%d", i)}}, "", int64(i), false)
	}
	a, b = a.pack(), b.pack()
	ps := &pairedRows{all: []*rowChunk{a, b}}
	parts := []*chunkPart{{kept: 3}, {kept: 3}}
	opened := 0
	newestOpening = func(*rowChunk) { opened++ }
	t.Cleanup(func() { newestOpening = nil })
	refs := func(h newestHeap, read map[rowRef]Row) []rowRef {
		if len(read) != len(h) {
			t.Fatalf("%d rows read for %d refs", len(read), len(h))
		}
		for _, r := range h {
			want := fmt.Sprintf("b%d", r.Index)
			if r.Chunk == a {
				want = fmt.Sprintf("a%d", r.Index)
			}
			if got := read[r.rowRef]; got.Model != want || !got.Time.Equal(r.at) {
				t.Fatalf("%v read as %q at %v, want %q at %v", r.rowRef, got.Model, got.Time, want, r.at)
			}
		}
		h = slices.Clone(h)
		slices.SortFunc(h, func(x, y timedRef) int { return x.at.Compare(y.at) })
		var out []rowRef
		for _, r := range h {
			out = append(out, r.rowRef)
		}
		return out
	}
	for i, c := range []struct {
		take, open int
		want       []rowRef
	}{
		{2, 1, []rowRef{{b, 1}, {b, 2}}},
		{2, 0, []rowRef{{b, 1}, {b, 2}}},
		{1, 0, []rowRef{{b, 2}}},
		{4, 2, []rowRef{{a, 2}, {b, 0}, {b, 1}, {b, 2}}},
		{4, 0, []rowRef{{a, 2}, {b, 0}, {b, 1}, {b, 2}}},
		{6, 0, []rowRef{{a, 0}, {a, 1}, {a, 2}, {b, 0}, {b, 1}, {b, 2}}},
	} {
		opened = 0
		sel, read := ps.newest(Filter{}, parts, c.take)
		open := opened
		// the rows read are their own: decoding the chunks again, over
		// the block they were lent from, leaves them as they were
		ps.newest(Filter{}, []*chunkPart{{kept: 3}, {kept: 3}}, 6)
		opened = open
		got := refs(sel, read)
		if !slices.Equal(got, c.want) || opened != c.open {
			t.Fatalf("page %d (take %d): %v, decoded %d; want %v, %d", i, c.take, got, opened, c.want, c.open)
		}
	}
	// a new part, as after a clear or a rename, reads the chunks again
	opened = 0
	parts = []*chunkPart{{kept: 3}, {kept: 3}}
	if got := refs(ps.newest(Filter{}, parts, 2)); !slices.Equal(got, []rowRef{{b, 1}, {b, 2}}) || opened != 1 {
		t.Fatalf("new parts: %v, decoded %d", got, opened)
	}
}

// A read keeps one chunk a source, the map's and the page's both: a copy
// for each was 13.5 MB after 34k sessions' start (#2278). A chunk an
// earlier read published is taken as it is, its use counted on it: a copy
// on every read was 255 MB in 80s of polls (#2278).
func TestRequestBlocksKeepOneChunkASource(t *testing.T) {
	pageHome(t)
	now := holdClock(t, time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local))
	path := filepath.Join(sessions.CodexDir(), "sessions", "rollout-2026-09-30T00-00-00-one.jsonl")
	os.MkdirAll(filepath.Dir(path), 0700)
	os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"id":"one","model_provider":"custom"}}`+"\n"+`{"type":"turn_context","payload":{"model":"m"}}`+"\n"+
		fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":1},"last_token_usage":{"input_tokens":10,"output_tokens":1}}}}`+"\n", now.Format(time.RFC3339Nano))), 0600)
	st := readRequestState(now)
	shared := &requestIndex{}
	shared.adopt(st)
	read := func() (*rowChunk, *requestIndex) {
		idx := shared.private()
		b := readRequestBlocks(idx, st, All, now, time.Time{}, sessions.ReadCallSource, true)
		defer b.priced.release()
		if len(b.chunks) != 1 {
			t.Fatalf("%d chunks, want 1", len(b.chunks))
		}
		return b.chunks[0], idx
	}
	built, idx := read()
	if idx.chunks[path] != built || built.lastUse() != idx.tick {
		t.Fatalf("a built chunk is copied: map %p, page %p", idx.chunks[path], built)
	}
	shared.keep(idx, map[string]bool{path: true})
	used := built.lastUse()
	again, idx := read()
	if again != built || idx.chunks[path] != again || again.lastUse() != idx.tick || idx.tick <= used {
		t.Fatalf("a published chunk read again: %p (was %p), map %p, used %d (was %d)", again, built, idx.chunks[path], again.lastUse(), used)
	}
}

// visibleLocal's bit sets only narrow what goes in its set of ids: what it
// leaves out is what a set of every id leaves out, for many chunks, ids
// repeated in a chunk and across chunks, settled chunks among them, and so
// few ids that most slots are shared.
func TestVisibleLocalBitsAreExact(t *testing.T) {
	t.Cleanup(ReleaseScratch)
	old := time.Now().AddDate(0, 0, -30)
	rng := rand.New(rand.NewPCG(7, 9))
	for _, ids := range []int{1, 3, 40, 5000} {
		var chunks []*rowChunk
		want := map[rowRef]bool{}
		seen := map[string]bool{}
		var made [][]string
		for c := range 30 {
			ch := &rowChunk{}
			var msgs []string
			for j := range 1 + rng.IntN(300) {
				msg := ""
				if rng.IntN(10) != 0 {
					msg = fmt.Sprintf("msg_%d", rng.IntN(ids*20))
				}
				r := Row{}
				if c%3 == 0 {
					r.Time = old
				}
				ch.add(r, msg, int64(j), false)
				msgs = append(msgs, msg)
			}
			ch = ch.pack()
			if c%3 == 0 {
				ch.Latest = old
			}
			ch.serial = nextChunkSerial()
			chunks = append(chunks, ch)
			made = append(made, msgs)
		}
		for c, msgs := range made {
			for j, msg := range msgs {
				if msg == "" {
					continue
				}
				if seen[msg] {
					want[rowRef{chunks[c], j}] = true
				}
				seen[msg] = true
			}
		}
		for i := range 2 { // the second with the scratch kept
			if got := refMap(visibleLocal(chunks)); !maps.Equal(got, want) {
				t.Fatalf("%d ids, call %d: left out %d rows, want %d", ids, i, len(got), len(want))
			}
		}
	}
}

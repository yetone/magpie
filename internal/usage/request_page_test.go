package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/sessions"
)

func pageHome(t *testing.T) {
	sessionHome(t)
	sessions.Reset()
	catalog.Reset()
	t.Cleanup(func() { sessions.Reset(); catalog.Reset() })
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
	now := time.Now().Truncate(time.Second)
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
	price := pricer()
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
		for _, f := range []Filter{{}, {Agent: "claude"}, {Provider: "a"}, {Failed: true}, {Query: "LOCAL"}, {Agent: "codex", Provider: UnknownProvider}, {Query: "no match"}} {
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
	now := time.Now()
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
	sessionAuth(t, sessions.CodexDir(), "a", "u", "one@example.com")
	path := filepath.Join(sessions.CodexDir(), "sessions", "rollout-2026-09-30T00-00-00-test.jsonl")
	os.MkdirAll(filepath.Dir(path), 0700)
	meta := `{"type":"session_meta","payload":{"id":"test","model_provider":"custom","creator_account_id":"a","creator_user_id":"u"}}` + "\n" + `{"type":"turn_context","payload":{"model":"m","effort":"high"}}` + "\n"
	line := func(n int) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"output_tokens":%d},"last_token_usage":{"input_tokens":10,"output_tokens":1}}}}`+"\n", time.Now().Format(time.RFC3339Nano), n*10, n)
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
	r, _ := json.Marshal(Record{Time: time.Now(), Model: "m", Agent: "opencode", Input: 10, Output: 1})
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

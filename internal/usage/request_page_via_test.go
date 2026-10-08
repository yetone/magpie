package usage

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// viaAll builds a ledger with two gateway rows and three session-file calls,
// as the requests page's two source cells count them, with the packed chunks
// the streaming path reads so both paths can be compared.
func viaAll(t *testing.T) (Ledgered, *rowChunk, []*rowChunk) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	at := time.Now().Add(-time.Hour)
	recs := []Record{
		{Time: at, Agent: "codex", Provider: "relay", Model: "gpt-6-sol", Input: 100, Output: 10, Status: 200},
		{Time: at.Add(time.Minute), Agent: "codex", Provider: "relay", Model: "gpt-6-sol", Input: 200, Output: 20, Status: 200},
		// a call the gateway turned away itself: it is one of the period's
		// rows but neither source's, as the page says under its totals
		// ("local rejections excluded"), so the two cells are the calls that
		// were answered and not every row the period holds
		{Time: at.Add(30 * time.Second), Agent: "codex", Provider: "relay", Model: "gpt-6-sol", Rejected: true, Status: 400},
	}
	logs := []sessions.Call{
		{Time: at.Add(2 * time.Minute), Agent: "dsh", Session: "s", Model: "deepseek-v4", Tokens: sessions.Tokens{Input: 300, Output: 30}},
		{Time: at.Add(3 * time.Minute), Agent: "dsh", Session: "s", Model: "deepseek-v4", Tokens: sessions.Tokens{Input: 400, Output: 40}},
		{Time: at.Add(4 * time.Minute), Agent: "dsh", Session: "s", Model: "deepseek-v4", Tokens: sessions.Tokens{Input: 500, Output: 50}},
	}
	rows, sum, agents, providers := ledgerWith(Period(All).Since(time.Now()), Filter{}, recs, logs)
	gateway := &rowChunk{}
	for i, r := range recs {
		r.Agent = AgentOf(r.Agent)
		gateway.add(Row{Record: r}, "", int64(i), false)
	}
	local := &rowChunk{}
	for i, c := range logs {
		local.add(Row{Record: logRecord(c), Source: "log"}, "", int64(i), false)
	}
	return Ledgered{rows, sum, agents, providers}, gateway, []*rowChunk{local}
}

// The requests page's two source cells and their total: the calls magpie's
// gateway served, the ones read from the agents' own session files, and both
// together. Picking one narrows the page to it, and every cell keeps its own
// number while another is picked — counted with the pick cleared — or the
// cell that was picked would read 0 and there would be no way back to it.
func TestRequestPageViaSplit(t *testing.T) {
	all, _, _ := viaAll(t)

	whole := pageFromLedger(All, Filter{}, 0, 100, all)
	if whole.Through.Calls != 2 || whole.Direct.Calls != 3 || whole.Sum.Calls != 5 {
		t.Fatalf("the split: through %d, direct %d, all %d (want 2, 3, 5)",
			whole.Through.Calls, whole.Direct.Calls, whole.Sum.Calls)
	}
	// the rejected call is one of the period's rows, and the two cells are
	// the answered ones: through + direct is Sum, never Total
	if whole.Total != 6 {
		t.Fatalf("the period holds %d rows, want 6 (5 answered, 1 rejected)", whole.Total)
	}
	if got, want := whole.Through.AllTokens()+whole.Direct.AllTokens(), whole.Sum.AllTokens(); got != want {
		t.Fatalf("through %d + direct %d = %d, want the total %d",
			whole.Through.AllTokens(), whole.Direct.AllTokens(), got, want)
	}

	for _, tc := range []struct {
		via    string
		want   int
		tokens int
	}{
		{SourceGateway, 2, whole.Through.AllTokens()},
		{SourceSession, 3, whole.Direct.AllTokens()},
	} {
		page := pageFromLedger(All, Filter{Through: tc.via}, 0, 100, all)
		if page.Total != tc.want || page.Sum.Calls != tc.want {
			t.Fatalf("%s: %d rows, %d calls (want %d)", tc.via, page.Total, page.Sum.Calls, tc.want)
		}
		if page.Sum.AllTokens() != tc.tokens {
			t.Fatalf("%s: %d tokens, want %d", tc.via, page.Sum.AllTokens(), tc.tokens)
		}
		// every cell goes on saying its own number, so the reader can switch
		// to the other source or back to the total
		if page.Through.Calls != whole.Through.Calls || page.Direct.Calls != whole.Direct.Calls {
			t.Fatalf("%s: the cells read through %d, direct %d (want %d, %d, the pick cleared)",
				tc.via, page.Through.Calls, page.Direct.Calls, whole.Through.Calls, whole.Direct.Calls)
		}
		if got := page.Through.AllTokens() + page.Direct.AllTokens(); got != whole.Sum.AllTokens() {
			t.Fatalf("%s: the total cell reads %d tokens, want %d", tc.via, got, whole.Sum.AllTokens())
		}
		// the total cell is the two cells added up, which is what the page
		// draws it from, not Total (that counts the rejected rows too)
		if got := page.Through.Calls + page.Direct.Calls; got != 5 {
			t.Fatalf("%s: the total cell reads %d calls, want 5", tc.via, got)
		}
	}
}

// The rows themselves are the picked source's, not just the totals: a reader
// who picks "not through magpie" must not be shown a gateway call.
func TestRequestPageViaPicksTheRows(t *testing.T) {
	all, _, _ := viaAll(t)
	for _, tc := range []struct {
		via   string
		log   bool
		count int
	}{
		{SourceGateway, false, 2},
		{SourceSession, true, 3},
	} {
		page := pageFromLedger(All, Filter{Through: tc.via}, 0, 100, all)
		if len(page.Rows) != tc.count {
			t.Fatalf("%s: %d rows, want %d", tc.via, len(page.Rows), tc.count)
		}
		for _, r := range page.Rows {
			if got := r.Source == "log"; got != tc.log {
				t.Fatalf("%s: a row of source %q was listed", tc.via, r.Source)
			}
		}
	}
}

// The same three numbers and the same filtering, on the path a live page is
// built by: queryPage reads the two kinds of chunk, so the streaming builder
// must split and count them the way pageFromLedger does — or a page served
// from the gateway's own tail would disagree with one read from the ledger.
func TestRequestPageViaSplitStreams(t *testing.T) {
	all, gateway, local := viaAll(t)
	page := func(f Filter) RequestPage {
		return buildRequestPage(All, f, 0, 100, gateway, local)
	}
	whole := page(Filter{})
	if whole.Through.Calls != 2 || whole.Direct.Calls != 3 || whole.Sum.Calls != 5 {
		t.Fatalf("the split: through %d, direct %d, all %d (want 2, 3, 5)",
			whole.Through.Calls, whole.Direct.Calls, whole.Sum.Calls)
	}
	if whole.Total != 6 {
		t.Fatalf("the period holds %d rows, want 6 (5 answered, 1 rejected)", whole.Total)
	}
	for _, tc := range []struct {
		via    string
		rows   int
		tokens int
	}{
		{SourceGateway, 2, whole.Through.AllTokens()},
		{SourceSession, 3, whole.Direct.AllTokens()},
	} {
		one := page(Filter{Through: tc.via})
		if one.Total != tc.rows || len(one.Rows) != tc.rows || one.Sum.Calls != tc.rows {
			t.Fatalf("%s: %d rows, %d calls (want %d)", tc.via, one.Total, one.Sum.Calls, tc.rows)
		}
		if one.Sum.AllTokens() != tc.tokens {
			t.Fatalf("%s: %d tokens, want %d", tc.via, one.Sum.AllTokens(), tc.tokens)
		}
		for _, r := range one.Rows {
			if got := r.Source == "log"; got != (tc.via == SourceSession) {
				t.Fatalf("%s: a row of source %q was listed", tc.via, r.Source)
			}
		}
		// the cells go on saying their own numbers with the pick cleared, so
		// the reader can switch to the other source or back to the total
		if one.Through.Calls != whole.Through.Calls || one.Direct.Calls != whole.Direct.Calls {
			t.Fatalf("%s: the cells read through %d, direct %d (want %d, %d, the pick cleared)",
				tc.via, one.Through.Calls, one.Direct.Calls, whole.Through.Calls, whole.Direct.Calls)
		}
		if got := one.Through.AllTokens() + one.Direct.AllTokens(); got != whole.Sum.AllTokens() {
			t.Fatalf("%s: the total cell reads %d tokens, want %d", tc.via, got, whole.Sum.AllTokens())
		}
	}
	// and the two builders agree on the same period, pick and all
	for _, f := range []Filter{{}, {Through: SourceGateway}, {Through: SourceSession}} {
		got, want := page(f), pageFromLedger(All, f, 0, 100, all)
		equalPage(t, got, want)
	}
}

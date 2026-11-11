package usage

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/sessions"
)

// recih on X (#6199): a page's cache totals hide one request that wrote a
// whole conversation again behind many that read it. The page names the
// largest write, and the request to jump to, on both paths that build it.
func TestRequestPageBigWrite(t *testing.T) {
	pageHome(t)
	now := time.Now()
	at := now.Add(-time.Hour)
	recs := []Record{
		{Time: at, Agent: "claude", Provider: "p", Model: "m", RouteID: 11, CacheWrite: 900, CacheRead: 50000, Status: 200},
		{Time: at.Add(time.Minute), Agent: "claude", Provider: "p", Model: "m", RouteID: 12, CacheWrite: 108546, CacheRead: 11541, Output: 197, Status: 200, Kind: KindAgentSummary + InferredSuffix},
		{Time: at.Add(2 * time.Minute), Agent: "claude", Provider: "p", Model: "m", RouteID: 13, CacheWrite: 400, Status: 200},
		// turned away: counted nowhere
		{Time: at.Add(3 * time.Minute), Agent: "claude", Provider: "p", Model: "m", RouteID: 14, CacheWrite: 999999, Rejected: true, Status: 400},
	}
	// a call read from a session file has no request to jump to
	logs := []sessions.Call{{Time: at.Add(4 * time.Minute), Agent: "dsh", Session: "s", Model: "d", Tokens: sessions.Tokens{CacheWrite: 5000000}}}
	gateway := &rowChunk{}
	for i, r := range recs {
		r.Agent = AgentOf(r.Agent)
		gateway.add(Row{Record: r}, "", int64(i), false)
	}
	local := &rowChunk{}
	for i, c := range logs {
		local.add(Row{Record: logRecord(c), Source: "log"}, "", int64(i), false)
	}
	rows, sum, agents, providers := ledgerWith(Period(All).Since(now), Filter{}, recs, logs)
	all := Ledgered{rows, sum, agents, providers}
	want := BigWrite{N: 108546, RouteID: 12, At: recs[1].Time, Model: "m"}
	for name, page := range map[string]RequestPage{
		"streamed": buildRequestPage(All, Filter{}, 0, 100, gateway, []*rowChunk{local}),
		"ledger":   pageFromLedger(All, Filter{}, 0, 100, all),
	} {
		if page.Big.N != want.N || page.Big.RouteID != want.RouteID || page.Big.Model != want.Model {
			t.Errorf("%s: largest write %+v, want %+v", name, page.Big, want)
		}
	}
	// the filter narrows it to the rows it keeps
	page := buildRequestPage(All, Filter{Query: "no such request"}, 0, 100, gateway, []*rowChunk{local})
	if page.Big != (BigWrite{}) {
		t.Errorf("no rows kept, largest write %+v", page.Big)
	}
}

func TestInferredKindsShareThePurposeOfTheirKind(t *testing.T) {
	for _, kind := range []string{KindAgentSummary, KindAutoMode} {
		if PurposeOf(kind+InferredSuffix) != PurposeOf(kind) || PurposeOf(kind) != "kind:"+kind {
			t.Errorf("%s: purposes %q, %q", kind, PurposeOf(kind), PurposeOf(kind+InferredSuffix))
		}
		if purposeKinds[kind+InferredSuffix].Name != purposeKinds[kind].Name+" (inferred)" {
			t.Errorf("%s: inferred name %q", kind, purposeKinds[kind+InferredSuffix].Name)
		}
	}
}

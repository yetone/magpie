package usage

import (
	"testing"
	"time"
)

// maohy915 on X: a magpie on a NAS, used from another computer's magpie,
// showed failed calls on Routing while Usage's Failed filter found none.
// The calls had failed on one provider and been answered by its fallback:
// the failed try wrote no row (nothing was billed) and its request's row
// is an answer on the second try. Failed keeps that request, as Routing
// shows its try failed, and a try handed on, beside the requests the
// agent got an error for; Retried still keeps only the answered ones.
func TestFailedKeepsRequestsAFallbackAnswered(t *testing.T) {
	pageHome(t)
	now := time.Now()
	at := func(s int) time.Time { return now.Add(time.Duration(s-30) * time.Second) }
	Append(Record{Time: at(1), Agent: "claude", Provider: "good", Model: "m2", Requested: "bad/m1", Input: 5, Output: 2, Status: 200, RouteID: 1, Tries: 2, Via: "desk"})
	Append(Record{Time: at(2), Agent: "claude", Provider: "good", Model: "m2", Requested: "bad/m1", Input: 5, Output: 2, Status: 200, RouteID: 2, Via: "desk"})
	Append(Record{Time: at(3), Agent: "claude", Provider: "bad", Model: "m1", Input: 5, Status: 429, Error: "rate limited", RouteID: 3, HandedOn: true, Via: "desk"})
	Append(Record{Time: at(4), Agent: "claude", Provider: "good", Model: "m2", Input: 5, Output: 2, Status: 200, RouteID: 3, Tries: 2, Via: "desk"})
	Append(Record{Time: at(5), Agent: "claude", Provider: "bad", Model: "m1", Status: 429, Error: "rate limited", RouteID: 4, Via: "desk"})
	Append(Record{Time: at(6), Agent: "claude", Provider: "good", Model: "m2", Status: StatusCanceled, Error: AgentCanceled, ErrType: CanceledErrType, RouteID: 5, Tries: 2, Via: "desk"})

	routes := func(rows []Row) map[int64]int {
		m := map[int64]int{}
		for _, r := range rows {
			m[int64(r.RouteID)]++
		}
		return m
	}
	rows, sum, _ := Ledger(All, Filter{Failed: true})
	if got := routes(rows); len(rows) != 4 || got[1] != 1 || got[3] != 2 || got[4] != 1 {
		t.Fatalf("Failed kept routes %v (%d rows), want 1, 3 (both tries) and 4", got, len(rows))
	}
	if sum.Calls != 3 || sum.Errors != 1 || sum.Retried != 2 {
		t.Fatalf("Failed's sum %+v: want 3 requests, 1 failed, 2 answered on a retry", sum)
	}
	if p := QueryPage(All, Filter{Failed: true}, 0, 50); p.Total != 4 || p.Sum.Errors != 1 || p.Sum.Retried != 2 {
		t.Fatalf("Failed page: %d rows, %+v", p.Total, p.Sum)
	}
	if p := QueryPage(All, Filter{Retried: true}, 0, 50); p.Total != 2 || p.Sum.Errors != 0 {
		t.Fatalf("Retried page: %d rows, %+v", p.Total, p.Sum)
	}
	// counted once each, by how it ended, as before
	if p := QueryPage(All, Filter{}, 0, 50); p.Sum.Calls != 5 || p.Sum.Errors != 1 || p.Sum.Retried != 2 || p.Sum.Canceled != 1 {
		t.Fatalf("all: %+v", p.Sum)
	}
}

package usage

import (
	"testing"
	"time"
)

// A try that failed and handed its request on before its vendor billed
// anything (a group member's 429 or 529) is a row of its own on the
// Requests page, with its status and the vendor's words (Dazzle_sys on X),
// but it is no call, no failure, no model the session went through and no
// call whose cache went uncounted: the request is the row that ends it.
func TestTriedOnlyRowListedNotCounted(t *testing.T) {
	resetUsageTest(t)
	at := time.Now().Add(-time.Minute)
	Append(Record{Time: at, Agent: "codex", Session: "s-1", Provider: "kiro", Model: "first", Status: 529,
		Error: "Overloaded right now", RouteID: 7, HandedOn: true})
	Append(Record{Time: at.Add(time.Second), Agent: "codex", Session: "s-1", Provider: "b", Model: "second",
		Input: 10, Output: 2, Status: 200, RouteID: 7, Tries: 2})

	rows, sum, _ := Ledger(All, Filter{})
	if len(rows) != 2 || sum.Calls != 1 || sum.Errors != 0 || sum.Retried != 1 || sum.CacheUnknownCalls != 0 {
		t.Fatalf("rows %d, sum %+v", len(rows), sum)
	}
	var tried *Row
	for i := range rows {
		if rows[i].HandedOn {
			tried = &rows[i]
		}
	}
	if tried == nil || tried.Status != 529 || tried.Error != "Overloaded right now" {
		t.Fatalf("the try's row: %+v", tried)
	}
	vias := Vias(time.Time{}).Of("codex|s-1")
	if len(vias) != 1 || vias[0].Model != "second" {
		t.Fatalf("the session went through %+v", vias)
	}
	got := GatewaySessions(time.Time{}, nil)
	if len(got) != 1 || len(got[0].Models) != 1 || got[0].Models[0].Model != "second" || got[0].Answered != 1 {
		t.Fatalf("gateway sessions %+v", got)
	}
}

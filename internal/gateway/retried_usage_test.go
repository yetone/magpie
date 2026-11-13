package gateway

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A request a group answered on its third member, after the first refused
// (its row kept for what it billed) and the second failed, is one request
// answered on a retry, not a failure (小一 on Discord): the refused try's
// row is marked handed on, and the answer's row says how many tries it
// took. A request that fails outright is the one failure.
func TestRetriedRequestCountsOnce(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", anthropicRefusal}}}
	b := &scripted{replies: []reply{{500, "application/json", `{"error":{"message":"overloaded"}}`}}}
	c := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	scriptedOn(t, "b", provider.Anthropic, b)
	scriptedOn(t, "c", provider.Anthropic, c)
	refusalGroup(t, "a/m", "b/m", "c/m")
	s := New()
	code, body := sendTo(s, "/v1/responses", codexAsk)
	if code != 200 || a.n != 1 || b.n == 0 || c.n != 1 {
		t.Fatalf("%d %s (a %d, b %d, c %d)", code, body, a.n, b.n, c.n)
	}
	route := s.trace.routes[len(s.trace.routes)-1]
	if len(route.Tries) < 3 {
		t.Fatalf("tries: %+v", route.Tries)
	}
	recs := usage.Load(time.Time{})
	if len(recs) != 2 || recs[0].Provider != "a" || !recs[0].HandedOn || recs[0].Status != 400 ||
		recs[1].Provider != "c" || recs[1].HandedOn || recs[1].Status != 200 || recs[1].Tries != len(route.Tries) {
		t.Fatalf("usage: %+v", recs)
	}
	_, sum, _ := usage.Ledger(usage.All, usage.Filter{})
	if sum.Calls != 1 || sum.Errors != 0 || sum.Retried != 1 {
		t.Fatalf("ledger counts %d calls, %d failed, %d retried", sum.Calls, sum.Errors, sum.Retried)
	}
	// Failed lists every request a try failed on, the ones a later try
	// answered too (d370bef98): both rows of this one, as Routing shows.
	if rows, _, _ := usage.Ledger(usage.All, usage.Filter{Failed: true}); len(rows) != 2 {
		t.Fatalf("failed filter leaves out the tries a fallback answered: %+v", rows)
	}
	if rows, _, _ := usage.Ledger(usage.All, usage.Filter{Retried: true}); len(rows) != 1 || rows[0].Provider != "c" {
		t.Fatalf("retried filter: %+v", rows)
	}

	// one provider, failing outright: one request, one failure
	fresh(t)
	x := &scripted{replies: []reply{{400, "application/json", `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`}}}
	scriptedOn(t, "x", provider.Anthropic, x)
	code, body = sendTo(New(), "/v1/messages", `{"model":"x/m","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code < 400 {
		t.Fatalf("%d %s", code, body)
	}
	_, sum, _ = usage.Ledger(usage.All, usage.Filter{})
	if sum.Calls != 1 || sum.Errors != 1 || sum.Retried != 0 {
		t.Fatalf("outright failure counts %d calls, %d failed, %d retried", sum.Calls, sum.Errors, sum.Retried)
	}
	if rows, _, _ := usage.Ledger(usage.All, usage.Filter{Failed: true}); len(rows) != 1 || rows[0].HandedOn || rows[0].Tries > 1 {
		t.Fatalf("failed filter: %+v", rows)
	}
}

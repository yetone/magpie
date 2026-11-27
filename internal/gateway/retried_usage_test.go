package gateway

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A request a group answered on its third member, after the first refused
// (its row kept for what it billed) and the second failed, is one request
// answered on a retry, not a failure (小一 on Discord): the refused try's
// row and each of the second's are marked handed on, and the answer's row
// says how many tries it took. A request that fails outright is the one
// failure.
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
	if n := len(recs); n != 2+b.n || recs[0].Provider != "a" || !recs[0].HandedOn || recs[0].Status != 400 ||
		recs[n-1].Provider != "c" || recs[n-1].HandedOn || recs[n-1].Status != 200 || recs[n-1].Tries != len(route.Tries) {
		t.Fatalf("usage (b asked %d times): %+v", b.n, recs)
	}
	for _, r := range recs[1 : len(recs)-1] {
		if r.Provider != "b" || !r.HandedOn || r.Status != 500 || !strings.Contains(r.Error, "overloaded") {
			t.Fatalf("b's try: %+v", r)
		}
	}
	_, sum, _ := usage.Ledger(usage.All, usage.Filter{})
	if sum.Calls != 1 || sum.Errors != 0 || sum.Retried != 1 {
		t.Fatalf("ledger counts %d calls, %d failed, %d retried", sum.Calls, sum.Errors, sum.Retried)
	}
	// Failed lists every request a try failed on, the ones a later try
	// answered too (d370bef98): every row of this one, as Routing shows.
	if rows, _, _ := usage.Ledger(usage.All, usage.Filter{Failed: true}); len(rows) != 2+b.n {
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

// A group member's 429 or 529 that billed nothing, and the member after
// it answering, leaves a row of its own on the Requests page: the try's
// status and the vendor's words, marked handed on, beside the answer's
// row. Before, only the Routing page showed it for a moment, and the
// Requests page had the answer's "2 tries" alone (Dazzle_sys on X: "弹了
// 几秒的429/529的错误然后我去请求页面翻没有看到相关记录"). The counts are
// the request's: one call, answered on a retry, no failure.
func TestGroupMemberFailedTryHasItsRow(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		said   string
	}{
		{429, `{"type":"error","error":{"type":"rate_limit_error","message":"Number of request tokens has exceeded your per-minute rate limit"}}`, "per-minute rate limit"},
		{529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded right now"}}`, "Overloaded right now"},
	} {
		t.Run(strconv.Itoa(c.status), func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []reply{{c.status, "application/json", c.body}}}
			b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
			scriptedOn(t, "a", provider.Anthropic, a)
			scriptedOn(t, "b", provider.Anthropic, b)
			refusalGroup(t, "a/m", "b/m")
			s := New()
			code, body := sendTo(s, "/v1/responses", codexAsk)
			if code != 200 || a.n == 0 || b.n != 1 {
				t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
			}
			route := s.trace.routes[len(s.trace.routes)-1]
			recs := usage.Load(time.Time{})
			n := len(recs)
			if n != a.n+1 || recs[n-1].Provider != "b" || recs[n-1].HandedOn || recs[n-1].Status != 200 || recs[n-1].Tries != len(route.Tries) {
				t.Fatalf("usage (a asked %d times): %+v", a.n, recs)
			}
			for _, r := range recs[:n-1] {
				if r.Provider != "a" || !r.HandedOn || r.Status != c.status || !strings.Contains(r.Error, c.said) || r.RouteID != route.ID || r.Input+r.Output != 0 {
					t.Fatalf("a's try: %+v", r)
				}
			}
			rows, sum, _ := usage.Ledger(usage.All, usage.Filter{})
			if len(rows) != n || sum.Calls != 1 || sum.Errors != 0 || sum.Retried != 1 || sum.CacheUnknownCalls != 0 {
				t.Fatalf("requests page: %d rows, %d calls, %d failed, %d retried, %d left out of the hit rate", len(rows), sum.Calls, sum.Errors, sum.Retried, sum.CacheUnknownCalls)
			}
			for _, r := range rows {
				if r.Provider == "a" && (!r.HandedOn || r.Status != c.status || !strings.Contains(r.Error, c.said)) {
					t.Fatalf("a's row on the requests page: %+v", r)
				}
			}
		})
	}
}

// answersOf is recs without the rows of tries that failed and handed their
// request on (usage.Record.HandedOn): the rows of the requests themselves.
func answersOf(recs []usage.Record) (answers, handedOn []usage.Record) {
	for _, r := range recs {
		if r.HandedOn {
			handedOn = append(handedOn, r)
		} else {
			answers = append(answers, r)
		}
	}
	return answers, handedOn
}

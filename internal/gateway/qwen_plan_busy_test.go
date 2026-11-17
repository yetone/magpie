package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// The Qwen Token Plan's words for the 429 it answers under many requests
// at once, its quota far from used, as the reporter quoted them
// (pelikanade on X: "HTTP 429: Qwen Token Plan: Allocated quota exceeded,
// please increase your quota limit"; "Qwen Token Plan: " is magpie's
// label), in OpenAI's error shape on /compatible-mode/v1 and DashScope's
// own.
var qwenPlanBusyBodies = []string{
	`{"error":{"message":"Allocated quota exceeded, please increase your quota limit","type":"limit_requests","param":null,"code":"limit_requests"},"request_id":"x"}`,
	`{"code":"Throttling.AllocationQuota","message":"Allocated quota exceeded, please increase your quota limit.","request_id":"x"}`,
}

// scriptedAs is scriptedOn for a provider of the preset given, named
// name.
func scriptedAs(t *testing.T, id, name, preset string, s *scripted) {
	t.Helper()
	up := httptest.NewServer(s)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: name, Preset: preset, Key: "k", Models: []string{"m"}, Chat: up.URL + "/compatible-mode/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// qwenStreamOK is a whole reply streamed: magpie asks DashScope's Token
// Plan for one streamed (TestDashScopeWholeAskedStreamed).
const qwenStreamOK = "data: {\"id\":\"ok\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}]}\n\n" +
	"data: {\"id\":\"ok\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

func qwenBusyTimes(n int, body string) *scripted {
	a := &scripted{}
	for range n {
		a.replies = append(a.replies, reply{429, "", body})
	}
	a.replies = append(a.replies, reply{200, "text/event-stream", qwenStreamOK})
	return a
}

// The Qwen Token Plan's concurrency 429 is asked again on the same
// account after a jittered moment, before anything reaches the agent, and
// the account doesn't rest when it then answers.
func TestQwenPlanConcurrencyIsAskedAgain(t *testing.T) {
	for _, said := range qwenPlanBusyBodies {
		fresh(t)
		a := qwenBusyTimes(2, said)
		scriptedAs(t, "qwen", "Qwen Token Plan", "qwen-token-plan", a)
		s := New()
		code, body := postAs(t, s, "", `{"model":"qwen/m","messages":[{"role":"user","content":"hi"}]}`)
		if code != 200 || !strings.Contains(body, "hello") || a.n != 3 {
			t.Errorf("%s: %d after %d tries: %s", said, code, a.n, body)
			continue
		}
		r := s.trace.routes[len(s.trace.routes)-1]
		// (Again is the pause in whole milliseconds, 0 for a jittered
		// pause under one at the tests' 1ms retryPause)
		if len(r.Tries) != 3 || r.Tries[0].Fail != failRate || r.Tries[1].Fail != failRate || r.Tries[0].Rest != nil || r.Tries[1].Rest != nil {
			t.Errorf("%s: tries %+v", said, r.Tries)
		}
		restingUntil.Lock()
		rests := len(restingUntil.m)
		restingUntil.Unlock()
		if rests != 0 {
			t.Errorf("%s: %d rests after it answered", said, rests)
		}

		// never clears: asked again as a rate limit is, its same-one
		// retries then its patience's rounds, and the agent has the 429
		fresh(t)
		a = qwenBusyTimes(99, said)
		scriptedAs(t, "qwen", "Qwen Token Plan", "qwen-token-plan", a)
		code, body = postAs(t, New(), "", `{"model":"qwen/m","messages":[{"role":"user","content":"hi"}]}`)
		if code != 429 || a.n != 1+rateRetries+replanRounds || !strings.Contains(body, "Allocated quota exceeded") {
			t.Errorf("%s never clearing: %d after %d tries: %s", said, code, a.n, body)
		}
	}
}

// In a group, the Qwen Token Plan member is asked again in place a few
// times; still turned away, the group moves on to its next member, the
// member resting as for a rate limit, not as out of quota.
func TestQwenPlanConcurrencyInAGroup(t *testing.T) {
	said := qwenPlanBusyBodies[0]
	group := func(t *testing.T, a *scripted) (*scripted, *Server) {
		b := &scripted{replies: []reply{{200, "", chatOK}}}
		scriptedAs(t, "qwen", "Qwen Token Plan", "qwen-token-plan", a)
		scriptedOn(t, "b", provider.Chat, b)
		if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"qwen/m", "b/m"}, Routing: provider.Ordered}); err != nil {
			t.Fatal(err)
		}
		return b, New()
	}

	fresh(t)
	a := qwenBusyTimes(2, said)
	b, s := group(t, a)
	if code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`); code != 200 || a.n != 3 || b.n != 0 {
		t.Fatalf("cleared: %d (qwen %d, b %d): %s", code, a.n, b.n, body)
	}

	fresh(t)
	a = qwenBusyTimes(99, said)
	b, s = group(t, a)
	if code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`); code != 200 || a.n != 1+rateRetries || b.n != 1 {
		t.Fatalf("never clearing: %d (qwen %d, b %d): %s", code, a.n, b.n, body)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	var rest *Rest
	for _, try := range r.Tries {
		if try.Rest != nil {
			rest = try.Rest
		}
	}
	if rest == nil || rest.Why != failRate {
		t.Fatalf("the member rests %+v, want a rate limit's; tries %+v", rest, r.Tries)
	}
}

// Another vendor's 429 in the same words is as it was: DashScope's
// pay-as-you-go limit per minute, Bailian's Token Plan, a relay that
// isn't Alibaba's. None is asked again in place; the plain provider's
// agent has the 429 at once, a group's next member is asked at once.
func TestOtherVendorsAllocatedQuotaUnchanged(t *testing.T) {
	said := qwenPlanBusyBodies[0]
	for _, x := range []struct{ name, preset string }{
		{"Qwen (China)", "qwen-cn"},
		{"Bailian Token Plan", "bailian-token-plan"},
		{"Relay", ""},
	} {
		fresh(t)
		a := qwenBusyTimes(2, said)
		scriptedAs(t, "a", x.name, x.preset, a)
		if code, body := postAs(t, New(), "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`); code != 429 || a.n != 1 {
			t.Errorf("%s alone: %d after %d tries: %s", x.name, code, a.n, body)
		}

		fresh(t)
		a = qwenBusyTimes(2, said)
		b := &scripted{replies: []reply{{200, "", chatOK}}}
		scriptedAs(t, "a", x.name, x.preset, a)
		scriptedOn(t, "b", provider.Chat, b)
		if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
			t.Fatal(err)
		}
		if code, body := postAs(t, New(), "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`); code != 200 || a.n != 1 || b.n != 1 {
			t.Errorf("%s in a group: %d (a %d, b %d): %s", x.name, code, a.n, b.n, body)
		}
	}
}

// The rule is the Qwen Token Plan's host's, a custom provider's at it too,
// and its concurrency words only: its free allowance or a plan window
// used up, or a 429 of other words, isn't it.
func TestQwenPlanBusyScope(t *testing.T) {
	plan := provider.Provider{ID: "c", Chat: "https://token-plan.maas.qianwenaiapi.com/compatible-mode/v1", Key: "k"}
	for _, x := range []struct {
		p      provider.Provider
		status int
		body   string
		want   bool
	}{
		{plan, 429, qwenPlanBusyBodies[0], true},
		{plan, 429, qwenPlanBusyBodies[1], true},
		{provider.Provider{Preset: "qwen-token-plan"}, 429, qwenPlanBusyBodies[0], true},
		{plan, 500, qwenPlanBusyBodies[0], false},
		{plan, 429, `{"code":"Throttling.AllocationQuota","message":"Free allocated quota exceeded.","request_id":"x"}`, false},
		{plan, 429, `{"code":"Throttling.AllocationQuota","message":"hour allocated quota exceeded.","request_id":"x"}`, false},
		{plan, 429, `{"error":{"message":"You've used up this month's credits"}}`, false},
		{provider.Provider{Chat: "https://dashscope.aliyuncs.com/compatible-mode/v1"}, 429, qwenPlanBusyBodies[0], false},
		{provider.Provider{Chat: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"}, 429, qwenPlanBusyBodies[0], false},
		{provider.Provider{Chat: "https://api.openai.com/v1"}, 429, qwenPlanBusyBodies[0], false},
	} {
		if got := qwenPlanBusy(x.p, x.status, []byte(x.body)); got != x.want {
			t.Errorf("%s %d %s: %v, want %v", x.p.Host(), x.status, x.body, got, x.want)
		}
	}
	for d := retryPause; d <= 8*retryPause; d *= 2 {
		for range 50 {
			if j := jittered(d); j < d/2 || j >= d/2+d {
				t.Fatalf("jittered(%v) = %v", d, j)
			}
		}
	}
}

// On the plan's Anthropic endpoint (/apps/anthropic) Claude Code's
// request is asked again the same, the 429 in Anthropic's error shape.
func TestQwenPlanConcurrencyOnAnthropic(t *testing.T) {
	fresh(t)
	ok := `{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	a := &scripted{replies: []reply{
		{429, "", `{"type":"error","error":{"type":"rate_limit_error","message":"Allocated quota exceeded, please increase your quota limit"}}`},
		{200, "", ok},
	}}
	up := httptest.NewServer(a)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: "qwen", Name: "Qwen Token Plan", Preset: "qwen-token-plan", Key: "k", Models: []string{"m"}, Anthropic: up.URL + "/apps/anthropic"}); err != nil {
		t.Fatal(err)
	}
	code, body := postTo(t, New(), "/v1/messages", `{"model":"qwen/m","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || a.n != 2 {
		t.Fatalf("%d after %d tries: %s", code, a.n, body)
	}
}

package gateway

import (
	"testing"
)

// WorkBuddy served by its plugin keeps the credits a reply said on its
// row, however the agent asked: chat, Messages or Responses, streamed or
// whole, translated to WorkBuddy's chat stream (jiabubu on Discord: a
// request's credits should be said on its row). TestPlanCreditsReachTheLedger
// holds a provider of its own to it; this is the plugin host's way.
func TestPluginPlanCreditsReachTheLedger(t *testing.T) {
	stream := sse(`data: {"id":"c","model":"fake-1","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		`data: {"id":"c","model":"fake-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],`+wbCreditUsage+`}`, "data: [DONE]")
	for _, c := range []struct{ name, path, body string }{
		{"chat stream", "/v1/chat/completions", `{"model":"workbuddy-ai/fake-1","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"chat whole", "/v1/chat/completions", `{"model":"workbuddy-ai/fake-1","messages":[{"role":"user","content":"hi"}]}`},
		{"messages", "/v1/messages", `{"model":"workbuddy-ai/fake-1","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"messages whole", "/v1/messages", `{"model":"workbuddy-ai/fake-1","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", "/v1/responses", `{"model":"workbuddy-ai/fake-1","stream":true,"input":"hi"}`},
		{"responses whole", "/v1/responses", `{"model":"workbuddy-ai/fake-1","input":"hi"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fake{t: t, reply: stream}
			movedFake(t, "workbuddy-ai", f)
			code, body := postTo(t, New(), c.path, c.body)
			if code != 200 {
				t.Fatalf("%d %s", code, body)
			}
			r := lastUsage(t)
			if r.Credits == nil || *r.Credits != 0.01 {
				t.Fatalf("%s %s: credits %v, want 0.01", r.Provider, r.Endpoint, r.Credits)
			}
		})
	}
}

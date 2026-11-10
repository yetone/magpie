package gateway

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// #4545: a reply that streamed its reasoning while its usage counted none
// of it has Reasoning 0 for "unknown", and the record says so
// (ReasoningHidden), so it isn't read as a reply that didn't reason. The
// Anthropic stream is DeepSeek's Anthropic endpoint's, as it came on
// 2026-10-31 (deepseek-v4-pro, thinking on): 33 thinking deltas, then
// "391", and a usage of output_tokens 35 with no thinking count, as
// Anthropic's own usage never has one.
func TestStreamedReasoningWithoutCount(t *testing.T) {
	b, err := os.ReadFile("testdata/reasoning-uncounted-deepseek-anthropic.sse")
	if err != nil {
		t.Fatal(err)
	}
	var real []string
	for _, ev := range strings.Split(strings.TrimSpace(string(b)), "\n\n") {
		if strings.Contains(ev, `"content_block_delta"`) {
			ev = "~" + ev // paced, as the deltas came
		}
		real = append(real, ev)
	}
	thought := func(s string) string {
		return `~data: {"choices":[{"delta":{"reasoning_content":"` + s + `"}}]}`
	}
	tool := `~data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"check","arguments":"{}"}}]}}]}`
	chatTool := func(details string) []string {
		return []string{thought("We need"), thought(" to check"), thought(" it."), tool,
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":5,"completion_tokens":40` + details + `}}`,
			`data: [DONE]`}
	}
	chatBody := `{"model":"fake/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	for _, c := range []struct {
		name         string
		proto        provider.Protocol
		events       []string
		path, body   string
		out, counted int
		hidden       bool
		timed        bool // its speed told (Decode)
	}{
		{"anthropic", provider.Anthropic, real, "/v1/messages", `{"model":"fake/m1","stream":true,"max_tokens":400,"messages":[{"role":"user","content":"hi"}]}`, 35, 0, true, true},
		{"chat from anthropic", provider.Anthropic, real, "/v1/chat/completions", chatBody, 35, 0, true, true},
		{"responses from anthropic", provider.Anthropic, real, "/v1/responses", `{"model":"fake/m1","stream":true,"input":"hi"}`, 35, 0, true, true},
		// a Chat vendor's reasoning_content with no completion_tokens_details,
		// a tool call its only answer: no text to time it from, and no count
		// to take the reasoning out of its output, so no speed
		{"chat tool, uncounted", provider.Chat, chatTool(""), "/v1/chat/completions", chatBody, 40, 0, true, false},
		{"anthropic from chat tool, uncounted", provider.Chat, chatTool(""), "/v1/messages", `{"model":"fake/m1","stream":true,"max_tokens":400,"messages":[{"role":"user","content":"hi"}]}`, 40, 0, true, false},
		// the same counted is known, and timed from its paced reasoning
		{"chat tool, counted", provider.Chat, chatTool(`,"completion_tokens_details":{"reasoning_tokens":30}`), "/v1/chat/completions", chatBody, 40, 30, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fresh(t)
			w := newHearing(0)
			pc := &paced{events: c.events, gap: 80 * time.Millisecond}
			if c.proto == provider.Chat {
				// each piece a gap after the agent had the one before, so
				// a gateway that reads late can't read them as one (#5948)
				w = newHearing(0, "We need", " to check", " it.")
				pc.heard = w.heard
			}
			up := httptest.NewServer(pc)
			t.Cleanup(up.Close)
			p := provider.Provider{ID: "fake", Name: "Fake", Key: "k", Models: []string{"m1"}}
			if c.proto == provider.Anthropic {
				p.Anthropic = up.URL
			} else {
				p.Chat = up.URL + "/v1"
			}
			if err := provider.Save(p); err != nil {
				t.Fatal(err)
			}
			s := New()
			s.Handler().ServeHTTP(w, httptest.NewRequest("POST", c.path, strings.NewReader(c.body)))
			if w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			u := lastUsage(t)
			if u.Output != c.out || u.Reasoning != c.counted || u.TTFT <= 0 {
				t.Fatalf("usage out %d reasoning %d ttft %d; want %d, %d, streamed", u.Output, u.Reasoning, u.TTFT, c.out, c.counted)
			}
			if u.ReasoningHidden != c.hidden {
				t.Fatalf("reasoning streamed with a count of %d read as hidden=%v; want %v: %+v", c.counted, u.ReasoningHidden, c.hidden, u)
			}
			r := lastRoute(s)
			if r.ReasoningHidden != c.hidden || len(r.Tries) != 1 || r.Tries[0].ReasoningHidden != c.hidden || s.Recent()[0].ReasoningHidden != c.hidden {
				t.Fatalf("trace disagrees with the record (hidden %v): %+v", c.hidden, r)
			}
			if n, ms := u.Decode(); (n > 0 && ms >= usage.MinDecodeMs) != c.timed {
				t.Fatalf("speed %d/%d ms; want told=%v: %+v", n, ms, c.timed, u)
			}
		})
	}
}

package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// Claude refusing after it has only thought, as Opus did Claude Code
// through a group (#248): thinking streamed, then stop_reason "refusal"
// with no word of the reply said. 381–707 tokens out, 200 on the routing
// page, and Claude Code's "…'s safeguards stopped the response above".
var anthropicThoughtThenRefused = sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","role":"assistant","model":"m","content":[],"usage":{"input_tokens":2,"cache_read_input_tokens":32749}}}`,
	`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"The user wants a plan for"}}`,
	`event: ping`+"\n"+`data: {"type":"ping"}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" the next step."}}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`,
	`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
	`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null},"usage":{"output_tokens":381}}`,
	`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)

var anthropicThoughtThenAnswered = sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","role":"assistant","model":"m","content":[]}}`,
	`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hm"}}`,
	`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
	`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"from a"}}`,
	`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":1}`,
	`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
	`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)

const claudeCodeAsk = `{"model":"group/g","max_tokens":10,"stream":true,"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hi"}]}`

// A refusal after nothing but reasoning is one with nothing said: the
// reasoning was shown to nobody, and the next member answers (#248) — for
// Claude Code on Anthropic's API, Codex on Responses, and each protocol's
// own reasoning.
func TestRefusalAfterThinkingFailsOver(t *testing.T) {
	chatThought := sse(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"thinking it over"}}]}`,
		`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`,
		`data: [DONE]`)
	responsesThought := sse(`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}`,
		`event: response.output_item.added`+"\n"+`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		`event: response.reasoning_summary_part.added`+"\n"+`data: {"type":"response.reasoning_summary_part.added","item_id":"rs_1","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
		`event: response.reasoning_summary_text.delta`+"\n"+`data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"**Planning the step**"}`,
		`event: response.reasoning_summary_text.done`+"\n"+`data: {"type":"response.reasoning_summary_text.done","item_id":"rs_1","output_index":0,"summary_index":0,"text":"**Planning the step**"}`,
		`event: response.output_item.done`+"\n"+`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"**Planning the step**"}]}}`,
		`event: response.failed`+"\n"+`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"bio_policy","message":"This content was flagged for possible biological risk."}}}`)
	for _, x := range []struct {
		name  string
		setup func(t *testing.T, s *scripted)
		reply string
		path  string
		ask   string
		model string // a model whose vendor refuses after thinking
	}{
		{"claude to claude code", func(t *testing.T, s *scripted) { scriptedOn(t, "a", provider.Anthropic, s) }, anthropicThoughtThenRefused, "/v1/messages", claudeCodeAsk, "claude-opus-5-5"},
		{"claude to codex", func(t *testing.T, s *scripted) { scriptedOn(t, "a", provider.Anthropic, s) }, anthropicThoughtThenRefused, "/v1/responses", codexAsk, "claude-opus-5-5"},
		{"chat to claude code", func(t *testing.T, s *scripted) { scriptedOn(t, "a", provider.Chat, s) }, chatThought, "/v1/messages", claudeCodeAsk, "gpt-5.2"},
		{"responses to codex", func(t *testing.T, s *scripted) { responsesOn(t, "a", s) }, responsesThought, "/v1/responses", codexAsk, "gpt-5.2"},
		{"responses to claude code", func(t *testing.T, s *scripted) { responsesOn(t, "a", s) }, responsesThought, "/v1/messages", claudeCodeAsk, "gpt-5.2"},
	} {
		t.Run(x.name, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []reply{{200, "text/event-stream", x.reply}}}
			b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
			x.setup(t, a)
			pa, err := provider.Find("a")
			if err != nil {
				t.Fatal(err)
			}
			pa.Models = []string{x.model}
			if err := provider.Save(*pa); err != nil {
				t.Fatal(err)
			}
			scriptedOn(t, "b", provider.Anthropic, b)
			refusalGroup(t, "a/"+x.model, "b/m")
			s := New()
			code, body := sendTo(s, x.path, x.ask)
			if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, `"refusal"`) || strings.Contains(body, "Planning") ||
				strings.Contains(body, "thinking it over") || strings.Contains(body, "user wants") || a.n != 1 || b.n != 1 {
				t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
			}
			r := lastRoute(s)
			if len(r.Tries) != 2 || r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil || r.Status != 200 {
				t.Fatalf("tries: %+v", r.Tries)
			}
		})
	}

	// thinking that goes on to an answer comes through whole, as it came
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", anthropicThoughtThenAnswered}}}
	b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	scriptedOn(t, "b", provider.Anthropic, b)
	refusalGroup(t, "a/m", "b/m")
	if code, body := sendTo(New(), "/v1/messages", claudeCodeAsk); code != 200 || body != anthropicThoughtThenAnswered || b.n != 0 {
		t.Fatalf("answered: %d %s (b %d)", code, body, b.n)
	}
}

// Reasoning keeps the stream held past the 15s a quiet one is: Claude
// thought for 16–25s before refusing (#248). Past holdThinking it goes
// through, as it did.
func TestThinkingHeldLonger(t *testing.T) {
	events := strings.SplitAfter(anthropicThoughtThenRefused, "\n\n")
	start := func(ago time.Duration) *holdWriter {
		h := newHoldWriter(httptest.NewRecorder(), true)
		h.Header().Set("Content-Type", "text/event-stream")
		h.WriteHeader(http.StatusOK)
		h.since = time.Now().Add(-ago)
		return h
	}
	// the reply begins at once; its thinking goes on past holdLongest
	h := start(0)
	h.Write([]byte(events[0] + events[1]))
	h.since = time.Now().Add(-holdLongest - 10*time.Second)
	for _, ev := range events[2:] {
		h.Write([]byte(ev))
	}
	h.settle()
	if h.passing || !h.refused || !h.failed() {
		t.Fatalf("thought 25s then refused: passing %v refused %v", h.passing, h.refused)
	}
	h = start(holdThinking + time.Second)
	h.Write([]byte(events[0] + events[1] + events[2]))
	if !h.passing {
		t.Fatal("reasoning held past holdThinking")
	}
}

package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// emptyStop is #6712's stream, as aistudio2api (magpie-gemini-web) sent
// it when Google's every regeneration came back malformed: keepalive
// comments while it waited, its role chunk with an empty content, then
// finish_reason "stop" and usage that counted the prompt and no output.
var emptyStop = sse(`: keepalive`,
	`data: {"id":"chatcmpl_1794688838638417321_17","object":"chat.completion.chunk","model":"gemini-4-hydrogen","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
	`: keepalive`,
	`data: {"id":"chatcmpl_1794688838638417321_17","object":"chat.completion.chunk","model":"gemini-4-hydrogen","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	`data: {"id":"chatcmpl_1794688838638417321_17","object":"chat.completion.chunk","model":"gemini-4-hydrogen","choices":[],"usage":{"prompt_tokens":2048,"completion_tokens":0,"total_tokens":2048,"prompt_tokens_details":{"cached_tokens":61004}}}`,
	`data: [DONE]`)

var chatFromB = sse(`data: {"id":"c2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
	`data: {"id":"c2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"from b"}}]}`,
	`data: {"id":"c2","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
	`data: [DONE]`)

// threeGroup saves group/g of a, b and c, each a scripted Chat provider
// answering with its own replies.
func threeGroup(t *testing.T, a, b, c *scripted) {
	t.Helper()
	scriptedOn(t, "a", provider.Chat, a)
	scriptedOn(t, "b", provider.Chat, b)
	scriptedOn(t, "c", provider.Chat, c)
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m", "c/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
}

// A group member's 200 stream that ends with finish_reason "stop" having
// said nothing at all — no text, no reasoning, no call — is no answer
// (#6712): the held stream fails before anything of it reaches the agent,
// the group's next member answers, and nobody rests. So in each protocol
// an agent speaks, the reply relayed as it came (Chat) or translated.
func TestEmptyStopFailsOverToTheNextMember(t *testing.T) {
	for _, x := range []struct {
		name, path, body string
	}{
		{"chat", "/v1/chat/completions", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"messages", "/v1/messages", `{"model":"group/g","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"responses", "/v1/responses", `{"model":"group/g","stream":true,"input":"hi"}`},
		{"gemini", "/v1beta/models/group/g:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`},
	} {
		t.Run(x.name, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []reply{{200, "text/event-stream", emptyStop}}}
			b := &scripted{replies: []reply{{200, "text/event-stream", chatFromB}}}
			c := &scripted{replies: []reply{{200, "text/event-stream", chatFromB}}}
			threeGroup(t, a, b, c)
			s := New()
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", x.path, strings.NewReader(x.body)))
			code, body := rec.Code, rec.Body.String()
			if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "chatcmpl_1794688838638417321_17") {
				t.Fatalf("%d %s %+v", code, body, s.trace.routes[len(s.trace.routes)-1])
			}
			if b.n != 1 || c.n != 0 {
				t.Fatalf("a asked %d, b asked %d, c asked %d", a.n, b.n, c.n)
			}
			r := s.trace.routes[len(s.trace.routes)-1]
			if len(r.Tries) != 2 || r.Tries[0].Status != 502 || r.Tries[0].Fail != failOther || r.Tries[0].Rest != nil ||
				!strings.Contains(r.Tries[0].Error, emptyReply) {
				t.Fatalf("tries: %+v", r.Tries)
			}
		})
	}
}

// The same reply given whole, to an agent that didn't ask for a stream,
// goes to the next member too.
func TestEmptyWholeReplyFailsOverToTheNextMember(t *testing.T) {
	fresh(t)
	empty := `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":2048,"completion_tokens":0,"total_tokens":2048}}`
	a := &scripted{replies: []reply{{200, "", empty}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	c := &scripted{replies: []reply{{200, "", chatOK}}}
	threeGroup(t, a, b, c)
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || b.n != 1 || c.n != 0 {
		t.Fatalf("%d %s (a %d, b %d, c %d)", code, body, a.n, b.n, c.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Fail != failOther || r.Tries[0].Rest != nil || !strings.Contains(r.Tries[0].Error, emptyReply) {
		t.Fatalf("tries: %+v", r.Tries)
	}
}

// A reply meant to be empty doesn't go round for ever: every member is
// asked once, and the last one's empty reply is the agent's, a 200 as it
// came. A model asked alone, outside a group, answers empty as before.
func TestEmptyStopEndsAtTheLastMember(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", emptyStop}}}
	b := &scripted{replies: []reply{{200, "text/event-stream", emptyStop}}}
	c := &scripted{replies: []reply{{200, "text/event-stream", emptyStop}}}
	threeGroup(t, a, b, c)
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"finish_reason":"stop"`) || strings.Contains(body, emptyReply) {
		t.Fatalf("%d %s", code, body)
	}
	if a.n != 1 || b.n != 1 || c.n != 1 {
		t.Fatalf("a asked %d, b asked %d, c asked %d", a.n, b.n, c.n)
	}

	fresh(t)
	a = &scripted{replies: []reply{{200, "text/event-stream", emptyStop}}}
	scriptedOn(t, "a", provider.Chat, a)
	code, body = postAs(t, New(), "", `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, `"finish_reason":"stop"`) || a.n != 1 {
		t.Fatalf("alone: %d %s (asked %d)", code, body, a.n)
	}
}

// A reply that only reasoned and then stopped said something: the agent
// has its reasoning, and the next member isn't asked.
func TestReasonedThenStoppedStaysTheAnswer(t *testing.T) {
	fresh(t)
	reasoned := sse(`data: {"id":"r","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
		`data: {"id":"r","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"reasoning_content":"thinking it over"}}]}`,
		`data: {"id":"r","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`data: [DONE]`)
	a := &scripted{replies: []reply{{200, "text/event-stream", reasoned}}}
	b := &scripted{replies: []reply{{200, "text/event-stream", chatFromB}}}
	c := &scripted{replies: []reply{{200, "text/event-stream", chatFromB}}}
	threeGroup(t, a, b, c)
	code, body := postAs(t, New(), "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "thinking it over") || b.n != 0 {
		t.Fatalf("%d %s (b asked %d)", code, body, b.n)
	}
}

// streamEvent tells a reply's end with nothing in its event apart, in
// each protocol, from one that ends a reply with something in it or for
// a reason of its own.
func TestStreamEventEmptyEnd(t *testing.T) {
	for _, x := range []struct {
		ev   string
		kind int
	}{
		{`data: {"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, eventEmptyEnd},
		{`data: {"choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":"stop"}]}`, eventEmptyEnd},
		{`data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`, eventContent},
		{`data: {"choices":[{"index":0,"delta":{"reasoning_content":"hm"},"finish_reason":"stop"}]}`, eventContent},
		{`data: {"choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`, eventContent},
		{`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":0}}`, eventEmptyEnd},
		{`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`, eventContent},
		{`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":0}}}`, eventEmptyEnd},
		{`event: response.completed` + "\n" + `data: {"type":"response.completed","response":{"status":"incomplete","output":[]}}`, eventContent},
		{`data: {"candidates":[{"content":{"role":"model","parts":[{"text":""}]},"finishReason":"STOP"}]}`, eventEmptyEnd},
		{`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`, eventContent},
		{`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MAX_TOKENS"}]}`, eventContent},
	} {
		if k, _, _ := streamEvent([]byte(x.ev)); k != x.kind {
			t.Errorf("streamEvent(%s) = %d, want %d", x.ev, k, x.kind)
		}
	}
}

// readWhole tells a whole reply with nothing in it apart from one with
// something, or one cut at its length, in each protocol.
func TestReadWholeEmpty(t *testing.T) {
	for _, x := range []struct {
		body  string
		empty bool
	}{
		{`{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`, true},
		{`{"choices":[{"message":{"role":"assistant","content":null},"finish_reason":"stop"}]}`, true},
		{`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`, false},
		{`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"t"}]},"finish_reason":"tool_calls"}]}`, false},
		{`{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"hm"},"finish_reason":"stop"}]}`, false},
		{`{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"length"}]}`, false},
		{`{"type":"message","content":[],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":0}}`, true},
		{`{"type":"message","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}`, false},
		{`{"type":"message","content":[],"stop_reason":"max_tokens"}`, false},
		{`{"status":"completed","output":[]}`, true},
		{`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}]}`, false},
		{`{"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}]}`, true},
		{`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f"}}]},"finishReason":"STOP"}]}`, false},
		{`{"error":{"message":"boom"}}`, false},
	} {
		if _, _, empty := readWhole([]byte(x.body)); empty != x.empty {
			t.Errorf("readWhole(%s) empty = %v, want %v", x.body, empty, x.empty)
		}
	}
}

// A Messages or Responses upstream that ends its turn with nothing in it,
// though it counted the prompt's tokens, is no answer either: the
// group's next member gives the agent its reply.
func TestEmptyEndFromEachUpstreamFailsOver(t *testing.T) {
	for _, x := range []struct {
		name  string
		proto provider.Protocol
		body  string
	}{
		{"messages", provider.Anthropic, sse(
			"event: message_start\ndata: "+`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","content":[],"usage":{"input_tokens":2048,"output_tokens":0}}}`,
			"event: ping\ndata: "+`{"type":"ping"}`,
			"event: message_delta\ndata: "+`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":0}}`,
			"event: message_stop\ndata: "+`{"type":"message_stop"}`)},
		{"responses", provider.Responses, sse(
			"event: response.created\ndata: "+`{"type":"response.created","response":{"id":"r","status":"in_progress","output":[]}}`,
			"event: response.completed\ndata: "+`{"type":"response.completed","response":{"id":"r","status":"completed","output":[],"usage":{"input_tokens":2048,"output_tokens":0,"total_tokens":2048}}}`)},
	} {
		t.Run(x.name, func(t *testing.T) {
			fresh(t)
			a := &scripted{replies: []reply{{200, "text/event-stream", x.body}}}
			b := &scripted{replies: []reply{{200, "text/event-stream", chatFromB}}}
			scriptedOn(t, "a", x.proto, a)
			scriptedOn(t, "b", provider.Chat, b)
			if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
				t.Fatal(err)
			}
			s := New()
			code, body := postAs(t, s, "", `{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			if code != 200 || !strings.Contains(body, "from b") || a.n != 1 || b.n != 1 {
				t.Fatalf("%d %s (a %d, b %d) %+v", code, body, a.n, b.n, s.trace.routes[len(s.trace.routes)-1].Tries)
			}
			if r := s.trace.routes[len(s.trace.routes)-1]; r.Tries[0].Rest != nil || !strings.Contains(r.Tries[0].Error, emptyReply) {
				t.Fatalf("tries: %+v", r.Tries)
			}
		})
	}
}

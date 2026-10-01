package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A reply the vendor's safety filter ended before anything was said in it
// (#248): Anthropic's stop_reason "refusal" after an empty text block, as
// Claude answered Codex through a group, 2 tokens out and the whole prompt
// read from the cache.
var anthropicRefusal = sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","role":"assistant","model":"m","content":[],"usage":{"input_tokens":3,"cache_read_input_tokens":237000,"cache_creation_input_tokens":47000}}}`,
	`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`event: ping`+"\n"+`data: {"type":"ping"}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`,
	`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
	`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null},"usage":{"output_tokens":2}}`,
	`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)

var anthropicAnswer = sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m2","role":"assistant","model":"m","content":[],"usage":{"input_tokens":3}}}`,
	`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from b"}}`,
	`event: content_block_stop`+"\n"+`data: {"type":"content_block_stop","index":0}`,
	`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
	`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)

// Chat Completions' content_filter, with nothing said before it.
var chatFiltered = sse(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
	`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`,
	`data: {"id":"c1","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":0}}`,
	`data: [DONE]`)

// Responses' incomplete for content_filter, with an empty message begun.
var responsesFiltered = sse(`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}`,
	`event: response.output_item.added`+"\n"+`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
	`event: response.content_part.added`+"\n"+`data: {"type":"response.content_part.added","item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
	`event: response.incomplete`+"\n"+`data: {"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[],"usage":{"input_tokens":3,"output_tokens":0,"total_tokens":3}}}`)

func refusalGroup(t *testing.T, members ...string) {
	t.Helper()
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: members, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
}

func responsesOn(t *testing.T, id string, s *scripted) {
	t.Helper()
	up := httptest.NewServer(s)
	t.Cleanup(up.Close)
	if err := provider.Save(provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k", Models: []string{"m"}, Responses: up.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
}

func sendTo(s *Server, path, body string) (int, string) {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec.Code, rec.Body.String()
}

const codexAsk = `{"model":"group/g","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`

// Codex asking a group whose first member's safety filter refuses with
// nothing said gets the next member's answer, not an empty reply it asks
// again for five times (#248); the member that refused doesn't rest, and
// what its refusal cost is logged as failed.
func TestRefusalFailsOverToTheNextMember(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", anthropicRefusal}}}
	b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
	for id, script := range map[string]*scripted{"a": a, "b": b} {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Request-Id", "req-"+id)
			script.ServeHTTP(w, r)
		}))
		t.Cleanup(up.Close)
		if err := provider.Save(provider.Provider{ID: id, Name: id, Key: "k", Models: []string{"m"}, Anthropic: up.URL}); err != nil {
			t.Fatal(err)
		}
	}
	refusalGroup(t, "a/m", "b/m")
	s := New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(codexAsk))
	req.Header.Set(SessionHeader, "override-session")
	req.Header.Set("Session_id", "native-session")
	s.Handler().ServeHTTP(rec, req)
	code, body := rec.Code, rec.Body.String()
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "content_filter") || a.n != 1 || b.n != 1 {
		t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
	}
	r := s.trace.routes[len(s.trace.routes)-1]
	if len(r.Tries) != 2 || r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil || r.Tries[0].Status != 400 || r.Status != 200 {
		t.Fatalf("tries: %+v", r.Tries)
	}
	if _, ok := restOf("a"); ok || s.resting("a") {
		t.Fatal("a refusal set the member aside")
	}
	recs := usage.Load(time.Time{})
	if len(recs) != 2 || recs[0].Provider != "a" || recs[0].Status != 400 || recs[0].CacheRead != 237000 || recs[0].Output != 2 ||
		recs[1].Provider != "b" || recs[1].Status != 200 {
		t.Fatalf("usage: %+v", recs)
	}

	if recs[0].Error == "" || recs[0].Error != keepMsg(r.Tries[0].Error) || recs[1].Error != "" {
		t.Fatalf("refusal reason missing or leaked into the successful attempt: %+v", recs)
	}

	for i, r := range recs {
		if r.Session != "override-session" || r.NativeSession != "native-session" || r.RequestID != []string{"req-a", "req-b"}[i] || r.Endpoint != "/v1/responses → /v1/messages" {
			t.Fatalf("attempt %d lost request identity: %+v", i, r)
		}
	}

	for _, rec := range recs {
		if rec.RouteID != r.ID || rec.RouteID == 0 {
			t.Fatalf("usage route %d, want %d", rec.RouteID, r.ID)
		}
	}

	// and on an Anthropic client's own API, relayed as it came
	fresh(t)
	a = &scripted{replies: []reply{{200, "text/event-stream", anthropicRefusal}}}
	b = &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	scriptedOn(t, "b", provider.Anthropic, b)
	refusalGroup(t, "a/m", "b/m")
	code, body = sendTo(New(), "/v1/messages", `{"model":"group/g","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, `"refusal"`) || a.n != 1 || b.n != 1 {
		t.Fatalf("messages: %d %s (a %d, b %d)", code, body, a.n, b.n)
	}

	// not streamed: the whole reply is read for it
	fresh(t)
	a = &scripted{replies: []reply{{200, "", `{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":"refusal","usage":{"input_tokens":3,"output_tokens":2}}`}}}
	b = &scripted{replies: []reply{{200, "", `{"id":"m2","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"from b"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	scriptedOn(t, "b", provider.Anthropic, b)
	refusalGroup(t, "a/m", "b/m")
	code, body = sendTo(New(), "/v1/messages", `{"model":"group/g","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, `"refusal"`) || a.n != 1 || b.n != 1 {
		t.Fatalf("whole: %d %s (a %d, b %d)", code, body, a.n, b.n)
	}
}

// OpenAI's content_filter, by Chat Completions' finish_reason or a
// Responses reply incomplete for it, fails over the same way.
func TestContentFilterFailsOver(t *testing.T) {
	for _, x := range []struct {
		name  string
		setup func(t *testing.T, s *scripted)
		reply string
		path  string
		ask   string
	}{
		{"chat to codex", func(t *testing.T, s *scripted) { scriptedOn(t, "a", provider.Chat, s) }, chatFiltered, "/v1/responses", codexAsk},
		{"chat to chat", func(t *testing.T, s *scripted) { scriptedOn(t, "a", provider.Chat, s) }, chatFiltered, "/v1/chat/completions",
			`{"model":"group/g","stream":true,"messages":[{"role":"user","content":"hi"}]}`},
		{"responses to codex", func(t *testing.T, s *scripted) { responsesOn(t, "a", s) }, responsesFiltered, "/v1/responses", codexAsk},
		{"responses failed", func(t *testing.T, s *scripted) { responsesOn(t, "a", s) },
			sse(`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"resp_1"}}`,
				`event: response.failed`+"\n"+`data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"content_filter","message":"The response was filtered"}}}`),
			"/v1/responses", codexAsk},
		{"chat whole", func(t *testing.T, s *scripted) { scriptedOn(t, "a", provider.Chat, s) },
			`{"id":"c1","choices":[{"index":0,"message":{"role":"assistant","content":null},"finish_reason":"content_filter"}]}`, "/v1/chat/completions",
			`{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`},
	} {
		t.Run(x.name, func(t *testing.T) {
			fresh(t)
			ctype := "text/event-stream"
			if strings.HasPrefix(x.reply, "{") {
				ctype = ""
			}
			a := &scripted{replies: []reply{{200, ctype, x.reply}}}
			b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
			x.setup(t, a)
			scriptedOn(t, "b", provider.Anthropic, b)
			refusalGroup(t, "a/m", "b/m")
			s := New()
			code, body := sendTo(s, x.path, x.ask)
			if code != 200 || !strings.Contains(body, "from b") || strings.Contains(body, "content_filter") || a.n != 1 || b.n != 1 {
				t.Fatalf("%d %s (a %d, b %d)", code, body, a.n, b.n)
			}
			if r := s.trace.routes[len(s.trace.routes)-1]; r.Tries[0].Fail != failRefused {
				t.Fatalf("tries: %+v", r.Tries)
			}
		})
	}
}

// With nobody else to ask, the agent is told it was refused, as a request
// not to send again as it is — not handed an empty reply that looks whole —
// and the usage says it failed.
func TestRefusalWithNobodyLeftIsAnError(t *testing.T) {
	fresh(t)
	a := &scripted{replies: []reply{{200, "text/event-stream", anthropicRefusal}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	refusalGroup(t, "a/m")
	s := New()
	code, body := sendTo(s, "/v1/responses", codexAsk)
	if code != 400 || !strings.Contains(body, "m refused this request (safety filter") || strings.Contains(body, "response.incomplete") || a.n != 1 {
		t.Fatalf("%d %s (a %d)", code, body, a.n)
	}
	if recs := usage.Load(time.Time{}); len(recs) != 1 || recs[0].Status != 400 || recs[0].CacheRead != 237000 {
		t.Fatalf("usage: %+v", recs)
	}
	if r := s.trace.routes[len(s.trace.routes)-1]; r.Status != 400 || r.Tries[0].Fail != failRefused || r.Tries[0].Rest != nil {
		t.Fatalf("route: %+v", r)
	}

	// a model asked by its own name, on Anthropic's API
	fresh(t)
	a = &scripted{replies: []reply{{200, "text/event-stream", anthropicRefusal}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	code, body = sendTo(New(), "/v1/messages", `{"model":"a/m","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || !strings.Contains(body, `"invalid_request_error"`) || !strings.Contains(body, "refused this request") || a.n != 1 {
		t.Fatalf("messages: %d %s (a %d)", code, body, a.n)
	}

	// and on Chat's
	fresh(t)
	a = &scripted{replies: []reply{{200, "text/event-stream", chatFiltered}}}
	scriptedOn(t, "a", provider.Chat, a)
	code, body = sendTo(New(), "/v1/chat/completions", `{"model":"a/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 400 || !strings.Contains(body, "safety filter: content_filter") || a.n != 1 {
		t.Fatalf("chat: %d %s (a %d)", code, body, a.n)
	}
}

// A refusal after some of the reply was said can't be taken back: the
// agent gets it as it came, and nobody else is asked. A reply that ends as
// usual is untouched.
func TestRefusalAfterContentPassesThrough(t *testing.T) {
	fresh(t)
	late := sse(`event: message_start`+"\n"+`data: {"type":"message_start","message":{"id":"m1","role":"assistant","model":"m","content":[]}}`,
		`event: content_block_start`+"\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`+"\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half"}}`,
		`event: message_delta`+"\n"+`data: {"type":"message_delta","delta":{"stop_reason":"refusal"},"usage":{"output_tokens":2}}`,
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`)
	a := &scripted{replies: []reply{{200, "text/event-stream", late}}}
	b := &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	scriptedOn(t, "b", provider.Anthropic, b)
	refusalGroup(t, "a/m", "b/m")
	code, body := sendTo(New(), "/v1/messages", `{"model":"group/g","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || body != late || b.n != 0 {
		t.Fatalf("messages: %d %s (b %d)", code, body, b.n)
	}
	// translated for Codex, it is the incomplete reply it was
	a.n = 0
	code, body = sendTo(New(), "/v1/responses", codexAsk)
	if code != 200 || !strings.Contains(body, "half") || !strings.Contains(body, `"content_filter"`) || b.n != 0 {
		t.Fatalf("responses: %d %s (b %d)", code, body, b.n)
	}

	// an answer as usual, as it came
	fresh(t)
	a = &scripted{replies: []reply{{200, "text/event-stream", anthropicAnswer}}}
	scriptedOn(t, "a", provider.Anthropic, a)
	scriptedOn(t, "b", provider.Anthropic, b)
	refusalGroup(t, "a/m", "b/m")
	code, body = sendTo(New(), "/v1/messages", `{"model":"group/g","max_tokens":10,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || body != anthropicAnswer || b.n != 0 {
		t.Fatalf("answer: %d %s (b %d)", code, body, b.n)
	}
	whole := `{"id":"m2","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"whole"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`
	a.replies = []reply{{200, "", whole}}
	code, body = sendTo(New(), "/v1/messages", `{"model":"group/g","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || strings.TrimSpace(body) != whole || b.n != 0 {
		t.Fatalf("whole: %d %s (b %d)", code, body, b.n)
	}
}

func TestStreamEventRefusals(t *testing.T) {
	for _, x := range []struct {
		ev   string
		kind int
	}{
		{`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, eventLead},
		{`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"x","input":{}}}`, eventContent},
		{`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`, eventThinking},
		{`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"s"}}`, eventLead},
		{`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hm"}}`, eventThinking},
		{`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`, eventContent},
		{`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"refusal"}}`, eventRefusal},
		{`event: message_delta` + "\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}`, eventContent},
		{`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}`, eventRefusal},
		{`data: {"id":"c","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"content_filter"}]}`, eventContent},
		{`data: {"id":"c","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, eventContent},
		{`data: {"type":"response.output_item.added","item":{"type":"reasoning","summary":[]}}`, eventThinking},
		{`data: {"type":"response.reasoning_summary_text.delta","delta":"**Plan**"}`, eventThinking},
		{`data: {"id":"c","choices":[{"index":0,"delta":{"reasoning_content":"hm"}}]}`, eventThinking},
		{`data: {"id":"c","choices":[{"index":0,"delta":{"reasoning_content":"hm"},"finish_reason":"content_filter"}]}`, eventRefusal},
		{`data: {"candidates":[{"content":{"parts":[{"text":"hm","thought":true}]}}]}`, eventThinking},
		{`data: {"type":"response.output_item.added","item":{"type":"function_call","name":"x"}}`, eventContent},
		{`data: {"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"hi"}]}}`, eventContent},
		{`data: {"type":"response.output_text.delta","delta":""}`, eventLead},
		{`data: {"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":""}}`, eventThinking},
		{`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`, eventRefusal},
		{`data: {"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, eventContent},
		{`data: {"type":"response.completed","response":{"status":"completed"}}`, eventContent},
		{`data: {"type":"error","error":{"code":"content_filter","message":"filtered"}}`, eventRefusal},
		{`data: {"candidates":[{"content":{"parts":[]},"finishReason":"SAFETY"}]}`, eventRefusal},
		{`data: {"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"SAFETY"}]}`, eventContent},
		{`data: {"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`, eventContent},
		{`data: {"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"}}`, eventRefusal},
	} {
		if k, _, _ := streamEvent([]byte(x.ev)); k != x.kind {
			t.Errorf("streamEvent(%s) = %d, want %d", x.ev, k, x.kind)
		}
	}
}

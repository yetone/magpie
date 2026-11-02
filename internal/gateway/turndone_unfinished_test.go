package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// emo172 (#4866): a reply that ended unfinished under a 200 is no task
// done. Its error event, a Responses reply failed or incomplete, one cut
// at its length are seen in every wire format, split anywhere; a success
// that only names an error ("error":null, a mention in text) is not. A
// tool search Codex runs itself is a tool call.
func TestReplyScanUnfinished(t *testing.T) {
	for _, c := range []struct {
		name, body  string
		cut, called bool
	}{
		// the ChatGPT backend's, as Codex got them (a tool's item is no
		// reply's end, every other line is)
		{"responses failed", `data: {"type":"response.failed","sequence_number":1,"response":{"id":"r0","status":"failed","error":{"code":"usage_limit_reached","message":"You've hit your usage limit. Upgrade to Pro, or try again in 2 hours."}}}`, true, false},
		{"responses incomplete", `data: {"type":"response.incomplete","response":{"id":"resp_cut","object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"reasoning","id":"rs_1","summary":[]}]}}`, true, false},
		{"responses error event", `data: {"type": "error","code":"server_error","message":"An error occurred while processing your request."}`, true, false},
		{"responses completed", `data: {"type":"response.completed","response":{"id":"r1","status":"completed","error":null,"incomplete_details":null,"output":[{"type":"message","content":[{"type":"output_text","text":"the \"type\":\"error\" case is fixed"}]}]}}`, false, false},
		{"responses tool search", `data: {"type":"response.output_item.done","item":{"type":"tool_search_call","execution":"client","call_id":"ts_1","status":"completed","arguments":{"query":"calendar"}}}`, false, true},
		// Anthropic's
		{"anthropic error", "event: error\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, true, false},
		{"anthropic max_tokens", `data: {"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":32000}}`, true, false},
		{"anthropic end_turn", `data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":12}}`, false, false},
		// Chat's
		{"chat error", `data: {"error":{"message":"upstream overloaded","type":"api_error"}}`, true, false},
		{"chat length", `data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`, true, false},
		{"chat stop", `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"set \"error\":{} and \"finish_reason\":\"length\""},"finish_reason":"stop"}]}`, false, false},
		// Gemini's
		{"gemini max tokens", `data: {"candidates":[{"content":{"parts":[{"text":"so"}],"role":"model"},"finishReason": "MAX_TOKENS"}]}`, true, false},
		{"gemini error", `data: {"error":{"code":502,"message":"no reply","status":"UNAVAILABLE"}}`, true, false},
		{"gemini stop", `data: {"candidates":[{"content":{"parts":[{"text":"so"}],"role":"model"},"finishReason":"STOP"}]}`, false, false},
	} {
		for _, at := range []int{0, 1, 9, 17, len(c.body) / 2, len(c.body) - 3} {
			var s toolScan
			at = min(max(at, 0), len(c.body))
			s.add([]byte(c.body[:at]))
			s.add([]byte(c.body[at:]))
			if s.cut != c.cut || s.called() != c.called || s.ended == c.called {
				t.Errorf("%s split at %d: cut %v called %v ended %v", c.name, at, s.cut, s.called(), s.ended)
			}
		}
	}
	// the agent gone before the reply's end is unfinished; gone after it,
	// as Codex hangs up once it has response.completed, is not
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	mid := &toolScan{ctx: ctx}
	mid.add([]byte(`data: {"type":"response.output_text.delta","delta":"wor"}`))
	if !mid.unfinished() {
		t.Error("gone mid-reply: finished")
	}
	whole := &toolScan{ctx: ctx}
	whole.add([]byte(`data: {"type":"response.completed","response":{"id":"r1","status":"completed"}}`))
	if whole.unfinished() {
		t.Error("gone after the reply's end: unfinished")
	}
	var none *toolScan
	none.add([]byte(`"type":"error"`))
	if none.unfinished() {
		t.Fatal("a nil scan unfinished")
	}
}

// cancelOnWrite is an agent that goes away (Esc, a closed window) once the
// reply has begun reaching it.
type cancelOnWrite struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
	once   sync.Once
}

func (c *cancelOnWrite) Write(p []byte) (int, error) {
	n, err := c.ResponseRecorder.Write(p)
	c.once.Do(c.cancel)
	return n, err
}

// emo172 (#4866): Codex signed in with ChatGPT, its turn on the backend's
// /backend-api/codex. A reply failed or incomplete under the backend's
// 200, and Codex gone mid-reply, are no task done: no end is said, and
// the route says the failure and the cancel as a routed turn's does. A
// whole reply after each still ends its turn.
func TestTurnDoneCodexBackendUnfinished(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	var reply atomic.Value // what the next reply is
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header()["Content-Type"] = nil // as the ChatGPT backend sends it
		created := `data: {"type":"response.created","response":{"id":"r1","status":"in_progress"}}`
		delta := `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_1","delta":"wor"}`
		switch reply.Load().(string) {
		case "failed":
			io.WriteString(w, sse(created, delta,
				`data: {"type":"response.failed","sequence_number":3,"response":{"id":"r1","status":"failed","error":{"code":"server_error","message":"An error occurred while processing your request."}}}`))
		case "incomplete":
			io.WriteString(w, sse(created, delta,
				`data: {"type":"response.incomplete","response":{"id":"r1","object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{"input_tokens":60,"output_tokens":256,"total_tokens":316}}}`))
		case "canceled":
			io.WriteString(w, sse(created, delta))
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-time.After(30 * time.Second):
			}
		default:
			io.WriteString(w, sse(created,
				`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
				`data: {"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":9,"output_tokens":2}}}`))
		}
	})
	ends, _ := turnsFollowed(t, time.Nanosecond)
	s := New()
	post := func(session, kind string) Route {
		reply.Store(kind)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rec := httptest.NewRecorder()
		var w http.ResponseWriter = rec
		if kind == "canceled" {
			w = &cancelOnWrite{ResponseRecorder: rec, cancel: cancel}
		}
		req := httptest.NewRequestWithContext(ctx, "POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":[
		  {"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/home/u/proj</cwd>\n</environment_context>"}]}]}`))
		req.Header.Set("Authorization", "Bearer chatgpt-token")
		req.Header.Set("chatgpt-account-id", "acct-1")
		req.Header.Set("session_id", session)
		s.Handler().ServeHTTP(w, req)
		return lastRoute(s)
	}
	for _, kind := range []string{"failed", "incomplete", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			session := "codex-" + kind
			rt := post(session, kind)
			switch kind {
			case "failed":
				if rt.Error == "" {
					t.Errorf("a failed reply recorded as a success: %d %q", rt.Status, rt.Error)
				}
			case "canceled":
				if rt.Status != 499 {
					t.Errorf("Codex gone mid-reply recorded as %d %q, want 499", rt.Status, rt.Error)
				}
			}
			noEnd(t, ends, "after a reply "+kind)
			post(session, "whole")
			if e := gotEnd(t, ends); e.Session != session {
				t.Fatalf("end %+v", e)
			}
		})
	}
}

// emo172 (#4866): Codex's socket to magpie, its turn on the ChatGPT
// backend. Esc sends response.interrupt; the turn it stops is no task
// done, and the next whole one ends its turn.
func TestTurnDoneCodexSocketInterrupt(t *testing.T) {
	setup(t, provider.Chat, &fake{t: t})
	var long atomic.Bool
	gone := make(chan struct{}, 1)
	chatgpt(t, func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if long.Load() {
			io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"resp_long","object":"response","status":"in_progress","output":[]}}`,
				`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_long","delta":"wor"}`))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			gone <- struct{}{}
			return
		}
		io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"resp_ok"}}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}`,
			`data: {"type":"response.completed","response":{"id":"resp_ok","usage":{"input_tokens":9,"output_tokens":2}}}`))
	})
	ends, _ := turnsFollowed(t, time.Nanosecond)
	_, ts := sockServer(t)
	h := http.Header{"Authorization": {"Bearer chatgpt-token"}, "Chatgpt-Account-Id": {"acct-1"},
		"Openai-Beta": {"responses_websockets=2026-02-06"}, "Session_id": {"codex-socket"}}
	c, _, err := dialSock(t, ts, CodexPath+"/responses", h)
	if err != nil {
		t.Fatal(err)
	}
	frame := `{"type":"response.create","model":"gpt-5.5","stream":true,"store":false,"input":[` + userHi + `]}`
	long.Store(true)
	c.send(frame)
	if got, err := c.until("response.created"); err != nil {
		t.Fatalf("%v %v", got, err)
	}
	c.send(`{"type":"response.interrupt","response_id":"resp_long","mode":"discard_partial_items"}`)
	if got, err := c.until("response.incomplete"); err != nil {
		t.Fatalf("%v %v", got, err)
	}
	select {
	case <-gone:
	case <-time.After(30 * time.Second):
		t.Fatal("the backend's request went on")
	}
	noEnd(t, ends, "after an interrupted turn")
	long.Store(false)
	if got := c.turn(frame); last(got).Get("type").String() != "response.completed" {
		t.Fatalf("%v", got)
	}
	gotEnd(t, ends)
}

// emo172 (#4866), the routed siblings: a reply relayed to the agent in its
// own protocol (Chat, Responses, Anthropic) that ends with an error event,
// failed or incomplete, or cut at its length, all under the upstream's
// 200, is no task done; the next whole reply ends the turn.
func TestTurnDoneRoutedUnfinished(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	noRests()
	t.Cleanup(noRests)
	var reply atomic.Value // the next reply's events, "" for a whole one
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if ev, _ := reply.Load().(string); ev != "" {
			io.WriteString(w, ev)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			io.WriteString(w, sse(`data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"done"}}]}`,
				`data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `data: [DONE]`))
		case strings.HasSuffix(r.URL.Path, "/responses"):
			io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"r1","status":"in_progress"}}`,
				`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
				`data: {"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":9,"output_tokens":2}}}`))
		default:
			io.WriteString(w, sse("event: message_start\n"+`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[],"stop_reason":null,"usage":{"input_tokens":9,"output_tokens":1}}}`,
				"event: content_block_start\n"+`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				"event: content_block_delta\n"+`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`,
				"event: content_block_stop\n"+`data: {"type":"content_block_stop","index":0}`,
				"event: message_delta\n"+`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`,
				"event: message_stop\n"+`data: {"type":"message_stop"}`))
		}
	}))
	defer srv.Close()
	if err := provider.Save(provider.Provider{ID: "plan", Name: "Plan", Chat: srv.URL + "/v1", Responses: srv.URL + "/v1", Anthropic: srv.URL, Models: []string{"m1"}, Key: "k"}); err != nil {
		t.Fatal(err)
	}
	s := New()
	send := func(path, body, session, events string) {
		reply.Store(events)
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set(SessionHeader, session)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
	}
	chat := `{"model":"plan/m1","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	responses := `{"model":"plan/m1","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`
	messages := `{"model":"plan/m1","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	chatText := `data: {"id":"c1","choices":[{"index":0,"delta":{"role":"assistant","content":"wor"}}]}`
	respText := `data: {"type":"response.created","response":{"id":"r1","status":"in_progress"}}` + "\n\n" +
		`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"m1","delta":"wor"}`
	anthText := "event: message_start\n" + `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m1","content":[],"stop_reason":null,"usage":{"input_tokens":9,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"wor"}}`
	for _, c := range []struct{ name, path, body, events string }{
		{"chat error event", "/v1/chat/completions", chat, sse(chatText, `data: {"error":{"message":"upstream overloaded","type":"api_error"}}`)},
		{"chat length", "/v1/chat/completions", chat, sse(chatText, `data: {"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"length"}]}`, `data: [DONE]`)},
		{"responses failed", "/v1/responses", responses, sse(respText, `data: {"type":"response.failed","response":{"id":"r1","status":"failed","error":{"code":"server_error","message":"upstream fell over"}}}`)},
		{"responses incomplete", "/v1/responses", responses, sse(respText, `data: {"type":"response.incomplete","response":{"id":"r1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`)},
		{"anthropic error event", "/v1/messages", messages, sse(anthText, "event: error\n"+`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)},
		{"anthropic max_tokens", "/v1/messages", messages, sse(anthText, "event: content_block_stop\n"+`data: {"type":"content_block_stop","index":0}`,
			"event: message_delta\n"+`data: {"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":100}}`,
			"event: message_stop\n"+`data: {"type":"message_stop"}`)},
	} {
		t.Run(c.name, func(t *testing.T) {
			ends, _ := turnsFollowed(t, time.Nanosecond)
			session := "s-" + strings.ReplaceAll(c.name, " ", "-")
			send(c.path, c.body, session, c.events)
			noEnd(t, ends, c.name)
			send(c.path, c.body, session, "")
			if e := gotEnd(t, ends); e.Session != session {
				t.Fatalf("end %+v", e)
			}
		})
	}
}

package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"github.com/yetone/magpie/internal/provider"
)

// postMessages sends an Anthropic Messages request to the gateway, as dsh's
// DeepSeek provider and Claude Code do.
func postMessages(t *testing.T, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(body))
	New().Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// An upstream that speaks Messages itself is relayed as-is; when it died
// mid-reply the relay just stopped, so the client saw a stream that ended
// cleanly with neither message_stop nor an error: dsh's "DeepSeek Messages
// stream ended before message_stop" (STREAM_CLOSED), which it does not
// retry and which says nothing of what went wrong (#370).
func TestPassthroughStreamSeveredUpstream(t *testing.T) {
	fresh(t)
	severed(t, provider.Anthropic, anthropicStart, anthropicText)
	code, body := postMessages(t, `{"model":"up/m","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "UP:") {
		t.Fatalf("a severed stream ended without an error event naming the provider: %s", body)
	}
	if strings.Contains(body, "message_stop") {
		t.Fatalf("a severed stream was finished as if complete: %s", body)
	}
}

// One the upstream ended cleanly but short of its last event is as broken.
func TestPassthroughStreamEndsShort(t *testing.T) {
	fresh(t)
	f := &fake{reply: anthropicStart + anthropicText}
	setup(t, "anthropic", f)
	code, body := postMessages(t, `{"model":"fake/m1","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "event: error") {
		t.Fatalf("a stream cut short ended without an error event: %d %s", code, body)
	}
}

// The upstream's last write stops mid-line, its last event cut inside the
// data line. The failure event that ends the relay must begin after a
// blank line: appended straight after the partial line, its "event:" name
// lands inside that line, and the client reads one event whose data is the
// truncated half and the error joined — no error of the protocol's, and
// nothing to act on (#370's error event, unread).
func TestPassthroughStreamCutMidLine(t *testing.T) {
	fresh(t)
	f := &fake{reply: anthropicStart + anthropicText + "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_re"}
	setup(t, "anthropic", f)
	code, body := postMessages(t, `{"model":"fake/m1","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}
	atLineEnd(t, body)
}

// One cut exactly at a line's end but short of the blank line joins the
// same way: the error event's fields land inside the event still pending.
func TestPassthroughStreamEndsOnLineEnd(t *testing.T) {
	fresh(t)
	f := &fake{reply: anthropicStart + anthropicText + "event: message_delta\n"}
	setup(t, "anthropic", f)
	code, body := postMessages(t, `{"model":"fake/m1","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}
	atLineEnd(t, body)
}

// atLineEnd fails when the reply's error event doesn't begin after a blank
// line, where an SSE client reads it as an event of its own.
func atLineEnd(t *testing.T, body string) {
	t.Helper()
	i := strings.Index(body, "event: error")
	if i < 0 {
		t.Fatalf("the cut stream ended without an error event: %s", body)
	}
	if !strings.HasSuffix(body[:i], "\n\n") {
		t.Fatalf("the error event began inside the event still pending: %q", body[max(0, i-60):i+len("event: error")])
	}
}

// severedChat is severed for an upstream the client speaks Chat to, as Pi
// does an OpenAI-compatible provider (#3197).
func severedChat(t *testing.T, events ...string) {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		hj, ok := w.(http.Hijacker)
		if !ok {
			panic("no hijacker")
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			panic(err)
		}
		defer conn.Close()
		body := strings.Join(events, "")
		fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n", len(body)+100)
		io.WriteString(buf, body)
		buf.Flush()
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "up", Name: "UP", Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// chatEventsSevered asks a Chat relay of the severed upstream and fails
// when any event it left the client is no JSON a client can read, or the
// stream doesn't end error, then [DONE].
func chatEventsSevered(t *testing.T) {
	t.Helper()
	code, body := post(t, "/v1/chat/completions", `{"model":"up/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}
	readSSE(strings.NewReader(body), func(_, data string) error {
		if data != "[DONE]" && json.Valid([]byte(data)) == false {
			t.Fatalf("the severed stream left an event no client can read: %q", data)
		}
		return nil
	})
	i := strings.Index(body, "connection lost mid-reply")
	if i < 0 {
		t.Fatalf("the severed stream ended without the failure said: %s", body)
	}
	if !strings.Contains(body[i:], "data: [DONE]\n\n") {
		t.Fatalf("the error event was not followed by [DONE]: %s", body[i:])
	}
}

// A Chat stream the upstream severs inside a data line: what the client
// has is half an event, and the blank line the relay's ending writes made
// it one — Pi read its truncated JSON as "malformed server-sent event
// JSON" and hung, the error event after it unread (#3197). The half line
// never goes out, and the stream ends with the error and the [DONE] a
// Chat client reads to.
func TestPassthroughChatSeveredMidLine(t *testing.T) {
	fresh(t)
	severedChat(t,
		`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}`+"\n\n",
		`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"hel"}}]}`+"\n\n",
		`data: {"id":"c1","model":"m","cho`)
	chatEventsSevered(t)
}

// One severed at a line's end keeps the line it finished: the event it
// completes stands, and the error and [DONE] follow as events of their own.
func TestPassthroughChatSeveredAtLineEnd(t *testing.T) {
	fresh(t)
	severedChat(t,
		`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"role":"assistant"}}]}`+"\n\n",
		`data: {"id":"c1","model":"m","choices":[{"index":0,"delta":{"content":"lo"}}]}`+"\n")
	chatEventsSevered(t)
}

// A reply the loop guard cuts (cutLoop) while the client is mid-line ends
// in the same joined event: what is appended after it must begin after a
// blank line too.
func TestLoopCutMidLine(t *testing.T) {
	fresh(t)
	run := strings.Repeat("mcp ", 1024) // 4096 chars, no break in it: one run-on unit
	delta := func(txt string) string {
		b, _ := json.Marshal(map[string]any{"type": "content_block_delta", "index": 0,
			"delta": map[string]any{"type": "text_delta", "text": txt}})
		return "event: content_block_delta\ndata: " + string(b) + "\n\n"
	}
	// the second run-on unit trips the guard (loop_guard.go: two units cut
	// at 4096 are a loop); the line after it stops mid-line, and the write
	// that trips ends there
	f := &fake{reply: anthropicStart + delta(run) + delta(run) + "event: content_block_delta\ndata: {\"type\":\"te"}
	setup(t, "anthropic", f)
	code, body := postMessages(t, `{"model":"fake/m1","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}
	if !strings.Contains(body, "stuck in a loop") {
		t.Fatalf("the run-on reply was not cut by the loop guard: %s", body[:min(600, len(body))])
	}
	// the upstream's own last line is the only event the client may read
	// unparseable: a join of two halves is magpie's doing
	joined := 0
	readSSE(strings.NewReader(body), func(_, data string) error {
		if strings.Contains(data, "\n") && json.Valid([]byte(data)) == false {
			joined++
		}
		return nil
	})
	if joined != 0 {
		t.Fatalf("an appended event was joined onto the line still pending: %s", body[max(0, len(body)-400):])
	}
}

// A whole stream goes through untouched, nothing added after its end,
// but for the model it names: the one the agent asked for (memberWriter).
func TestPassthroughStreamWhole(t *testing.T) {
	fresh(t)
	f := &fake{reply: anthropicStart + anthropicText + anthropicStop}
	setup(t, "anthropic", f)
	code, body := postMessages(t, `{"model":"fake/m1","stream":true,"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || body != namedAs(anthropicStart+anthropicText+anthropicStop, "fake/m1") {
		t.Fatalf("a whole stream was changed: %d %q", code, body)
	}
}

// A Responses stream cut short fails as Codex reads a failed turn.
func TestPassthroughResponsesEndsShort(t *testing.T) {
	fresh(t)
	f := &fake{reply: sse(`event: response.created`+"\n"+`data: {"type":"response.created","sequence_number":0,"response":{"id":"r1","model":"m1","created_at":1700000000}}`,
		`event: response.output_text.delta`+"\n"+`data: {"type":"response.output_text.delta","sequence_number":4,"delta":"hel"}`)}
	setup(t, "responses", f)
	code, body := post(t, "/v1/responses", `{"model":"fake/m1","stream":true,"input":"hi"}`)
	if code != 200 || !strings.Contains(body, `"type":"response.failed"`) || !strings.Contains(body, "Fake:") {
		t.Fatalf("a Responses stream cut short did not fail: %d %s", code, body)
	}
	// The failed turn's response is a whole Response, as a typed client reads
	// it, and the event continues the stream's numbering: the last event the
	// client got was numbered 4, so this is 5.
	failed := ""
	for _, line := range strings.Split(body, "\n") {
		if d, ok := strings.CutPrefix(line, "data: "); ok && gjson.Get(d, "type").String() == "response.failed" {
			failed = d
		}
	}
	if failed == "" {
		t.Fatalf("no response.failed data line: %s", body)
	}
	for _, field := range []string{"sequence_number", "response.id", "response.object", "response.created_at", "response.status", "response.model", "response.output", "response.error"} {
		if !gjson.Get(failed, field).Exists() {
			t.Errorf("response.failed lacks %s: %s", field, failed)
		}
	}
	if got := gjson.Get(failed, "sequence_number").Int(); got != 5 {
		t.Errorf("response.failed sequence_number = %d, want 5: %s", got, failed)
	}
	if got := gjson.Get(failed, "response.created_at").Int(); got != 1700000000 {
		t.Errorf("response.failed created_at = %d, want the stream's 1700000000: %s", got, failed)
	}
}

// The socket route synthesizes the reply headers' events for Codex alone: a
// typed client on the socket reads codex.response.metadata or codex.rate_limits
// as an unknown variant, as on the HTTP route.
func TestSocketHeaderFramesOnlyForCodex(t *testing.T) {
	h := http.Header{}
	h.Set("X-Models-Etag", `W/"9e0"`)
	h.Set("x-codex-primary-used-percent", "3")
	h.Set("x-codex-primary-window-minutes", "300")
	h.Set("openai-model", "gpt-5.5")
	w := &socketTurnWriter{h: h}
	if got := w.headerFrames(); len(got) != 0 {
		t.Fatalf("a client that isn't Codex was sent the backend's frames: %q", got)
	}
	w.codex = true
	got := string(bytes.Join(w.headerFrames(), nil))
	for _, want := range []string{"response.metadata", "codex.response.metadata", "codex.rate_limits"} {
		if !strings.Contains(got, want) {
			t.Errorf("Codex's own request lost %s: %s", want, got)
		}
	}
}

// A whole stream whose last event is too big for the sniffer to read (an
// image's base64 in response.completed) is still whole: nothing is added.
func TestPassthroughResponsesBigLastEvent(t *testing.T) {
	fresh(t)
	big := strings.Repeat("A", 2<<20)
	reply := sse(`event: response.created`+"\n"+`data: {"type":"response.created","response":{"id":"r1","model":"m1"}}`,
		`event: response.completed`+"\n"+`data: {"type":"response.completed","response":{"id":"r1","output":[{"type":"image_generation_call","result":"`+big+`"}]}}`)
	f := &fake{reply: reply}
	setup(t, "responses", f)
	code, body := post(t, "/v1/responses", `{"model":"fake/m1","stream":true,"input":"hi"}`)
	if code != 200 || body != namedAs(reply, "fake/m1") {
		t.Fatalf("a whole stream with a big last event was changed: %d, %d bytes added", code, len(body)-len(reply))
	}
}

// The upstream reset inside an event (#2580: Z.ai cut a glm-5.3 stream
// mid-line under pi): the half event went to the client as data, and the
// client's SSE parser died reading it — "malformed server-sent event
// JSON" — so the error event magpie appended after it was never reached,
// and the turn failed without a word of what went wrong. A line the
// upstream never ended is no event of its own: the client reads the
// events that were whole and then the error, nothing it has to fail on.
func TestChatStreamCutMidLineLeavesNoHalfEvent(t *testing.T) {
	fresh(t)
	severed(t, provider.Chat, sse(chatThink("glm-5.3", "thinking")),
		`data: {"id":"c1","model":"glm-5.3","choices":[{"index":0,"delta":{"reasoning_content":"the VM program itself hit an undefined`)
	code, body := post(t, "/v1/chat/completions", `{"model":"up/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}
	if !strings.Contains(body, "connection lost mid-reply") {
		t.Fatalf("a cut Chat stream ended without the error event: %s", body)
	}
	if strings.Contains(body, "undefined") {
		t.Fatalf("a half event the upstream never ended went to the client as data: %s", body[max(0, len(body)-300):])
	}
	// every event the client reads parses, the error included: the
	// stream is one a strict SSE reader goes through to the end of
	readSSE(strings.NewReader(body), func(_, data string) error {
		if data != "[DONE]" && !json.Valid([]byte(data)) {
			t.Errorf("an event the client reads is no JSON: %q", data)
		}
		return nil
	})
}

// A reset that comes after an event's JSON is whole but before the line
// ends leaves no half event: that event parses, so it goes to the client
// as before, and the error after it is an event of its own. Only a line
// whose data doesn't parse is dropped (#2580).
func TestChatStreamCutAfterWholeEventKeepsIt(t *testing.T) {
	fresh(t)
	severed(t, provider.Chat, sse(chatThink("glm-5.3", "thinking")), `data: `+chatText("glm-5.3", "kept to the end"))
	code, body := post(t, "/v1/chat/completions", `{"model":"up/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status = %d, want 200; body: %s", code, body)
	}
	if !strings.Contains(body, "connection lost mid-reply") {
		t.Fatalf("a cut Chat stream ended without the error event: %s", body)
	}
	var got []string
	readSSE(strings.NewReader(body), func(_, data string) error {
		if data != "[DONE]" && !json.Valid([]byte(data)) {
			t.Errorf("an event the client reads is no JSON: %q", data)
		}
		got = append(got, data)
		return nil
	})
	if !strings.Contains(strings.Join(got, "\n"), "kept to the end") {
		t.Fatalf("the last whole event was dropped: %q", body)
	}
}

package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// cutChat is a Chat upstream that severs the reply mid-stream on the calls
// cut names: the events given go out with a Content-Length promising more,
// then the connection closes. Other calls answer whole. Bodies journals
// what each call was asked.
type cutChat struct {
	mu     sync.Mutex
	bodies []map[string]any
	paths  []string
	cuts   map[int][]string
	whole  []string
}

func (f *cutChat) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	var body map[string]any
	b, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(b, &body); err != nil {
		t.Errorf("upstream request isn't JSON: %s", b)
	}
	f.mu.Lock()
	call := len(f.bodies)
	f.bodies = append(f.bodies, body)
	f.paths = append(f.paths, r.URL.Path)
	events, cut := f.cuts[call]
	whole := f.whole
	f.mu.Unlock()
	if !cut {
		events = whole
	}
	writeStream(w, events, cut, "data: [DONE]\n\n")
}

// writeStream writes events as an SSE stream, ending with tail; when cut,
// it severs the connection after them with a Content-Length promising
// more, the way an upstream dying mid-reply leaves the stream.
func writeStream(w http.ResponseWriter, events []string, cut bool, tail string) {
	w.Header().Set("Content-Type", "text/event-stream")
	if !cut {
		for _, ev := range events {
			io.WriteString(w, "data: "+ev+"\n\n")
		}
		io.WriteString(w, tail)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		panic("no hijacker")
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	var sb strings.Builder
	for _, ev := range events {
		sb.WriteString("data: " + ev + "\n\n")
	}
	fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: %d\r\n\r\n", sb.Len()+100)
	io.WriteString(buf, sb.String())
	buf.Flush()
}

func (f *cutChat) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

// path is the path call was asked on.
func (f *cutChat) path(call int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paths[call]
}

// lastAssistant is the last message a call was asked with, when it's the
// assistant's: the reply so far, sent back to go on from.
func (f *cutChat) lastAssistant(t *testing.T, call int) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs, _ := f.bodies[call]["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "assistant" {
		return nil
	}
	return last
}

// cutAnthropic is an Anthropic upstream that severs the reply mid-stream
// on the calls cut names, as cutChat does for a Chat one; fails calls it
// answers with the status's error body instead. Bodies journals what each
// call was asked.
type cutAnthropic struct {
	mu     sync.Mutex
	bodies []map[string]any
	cuts   map[int][]string
	whole  []string
	fails  map[int]string
}

func (f *cutAnthropic) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	var body map[string]any
	b, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(b, &body); err != nil {
		t.Errorf("upstream request isn't JSON: %s", b)
	}
	f.mu.Lock()
	call := len(f.bodies)
	f.bodies = append(f.bodies, body)
	events, cut := f.cuts[call]
	whole, fails := f.whole, f.fails[call]
	f.mu.Unlock()
	if fails != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		io.WriteString(w, fails)
		return
	}
	if !cut {
		events = whole
	}
	writeStream(w, events, cut, "")
}

func (f *cutAnthropic) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

// lastAssistantText is the text of the last message a call was asked
// with, when it's the assistant's: the reply so far, sent back to go on
// from. want is false when the last message is the user's.
func (f *cutAnthropic) lastAssistantText(t *testing.T, call int) (string, bool) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs, _ := f.bodies[call]["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "assistant" {
		return "", false
	}
	var sb strings.Builder
	for _, b := range last["content"].([]any) {
		block, _ := b.(map[string]any)
		if s, _ := block["text"].(string); s != "" {
			sb.WriteString(s)
		}
	}
	return sb.String(), true
}

// anthStart, anthThink and anthText are Anthropic stream events: the
// reply's start (input tokens billed), a thinking and a text delta;
// anthStop its end (output tokens billed).
func anthStart(model string, input int) string {
	return fmt.Sprintf(`{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":%q,"content":[],"usage":{"input_tokens":%d,"output_tokens":0}}}`, model, input)
}

func anthThink(s string) string {
	return `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":` + jsonStr(s) + `}}`
}

func anthText(s string) string {
	return `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + jsonStr(s) + `}}`
}

func anthStop(output int) string {
	return fmt.Sprintf(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":%d}}`, output)
}

// continuationChatServer points a provider whose only API is Anthropic's
// at f, and answers a Chat request for its model: the reply is translated.
func continuationChatServer(t *testing.T, model string, f *cutAnthropic) string {
	t.Helper()
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.serve(t, w, r) }))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "up", Name: "UP", Key: "k", Models: []string{model}, Anthropic: up.URL}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/chat/completions", `{"model":"up/`+model+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	return body
}

// chatTextOf concatenates a Chat stream's content deltas, chatThinkOf its
// reasoning deltas; chatUsageOf is the usage of its last chunk that
// carries one.
func chatTextOf(body string) string {
	return chatDeltaOf(body, "content")
}

func chatThinkOf(body string) string {
	return chatDeltaOf(body, "reasoning_content")
}

func chatDeltaOf(body, field string) string {
	var sb strings.Builder
	for _, ev := range events(body) {
		for _, c := range choices(ev) {
			if d, ok := c["delta"].(map[string]any); ok {
				if s, _ := d[field].(string); s != "" {
					sb.WriteString(s)
				}
			}
		}
	}
	return sb.String()
}

func chatUsageOf(body string) map[string]any {
	var out map[string]any
	for _, ev := range events(body) {
		if u, ok := ev["usage"].(map[string]any); ok {
			out = u
		}
	}
	return out
}

func choices(ev map[string]any) []map[string]any {
	var out []map[string]any
	cs, _ := ev["choices"].([]any)
	for _, c := range cs {
		if m, ok := c.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// chatThink and chatText are one Chat chunk of reasoning and of text.
func chatThink(model, s string) string {
	return fmt.Sprintf(`{"id":"c1","model":%q,"choices":[{"index":0,"delta":{"reasoning_content":%s}}]}`, model, jsonStr(s))
}

func chatText(model, s string) string {
	return fmt.Sprintf(`{"id":"c1","model":%q,"choices":[{"index":0,"delta":{"content":%s}}]}`, model, jsonStr(s))
}

func chatStop(model string) string {
	return fmt.Sprintf(`{"id":"c1","model":%q,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":4}}`, model)
}

// continuationServer points a provider whose only API is Chat at f, and
// answers a Messages request for its model: the reply is translated.
func continuationServer(t *testing.T, model string, f *cutChat) string {
	t.Helper()
	fresh(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.serve(t, w, r) }))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "up", Name: "UP", Key: "k", Models: []string{model}, Chat: up.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	code, body := post(t, "/v1/messages", `{"model":"up/`+model+`","max_tokens":32000,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	return body
}

// textOf concatenates a Messages stream's text deltas, thinkOf its
// thinking deltas.
func textOf(body string) string {
	var sb strings.Builder
	for _, ev := range events(body) {
		if ev["type"] == "content_block_delta" {
			if d, ok := ev["delta"].(map[string]any); ok && d["type"] == "text_delta" {
				sb.WriteString(d["text"].(string))
			}
		}
	}
	return sb.String()
}

func thinkOf(body string) string {
	var sb strings.Builder
	for _, ev := range events(body) {
		if ev["type"] == "content_block_delta" {
			if d, ok := ev["delta"].(map[string]any); ok && d["type"] == "thinking_delta" {
				sb.WriteString(d["thinking"].(string))
			}
		}
	}
	return sb.String()
}

func messageStops(body string) (starts, stops, errs int) {
	for _, ev := range events(body) {
		switch ev["type"] {
		case "message_start":
			starts++
		case "message_stop":
			stops++
		case "error":
			errs++
		}
	}
	return
}

// A reply the upstream cut after some text goes on: the same conversation
// is asked again with what the client has sent back, and the client reads
// one whole reply, its thinking with it.
func TestStreamCutMidReplyContinues(t *testing.T) {
	f := &cutChat{
		cuts:  map[int][]string{0: {chatThink("kimi-k3", "think-1"), chatText("kimi-k3", "hel")}},
		whole: []string{chatThink("kimi-k3", "think-2"), chatText("kimi-k3", "lo"), chatStop("kimi-k3")},
	}
	body := continuationServer(t, "kimi-k3", f)
	if f.calls() != 2 {
		t.Fatalf("the cut reply wasn't asked of the same conversation again: %d calls", f.calls())
	}
	m := f.lastAssistant(t, 1)
	if m == nil || m["content"] != "hel" || m["reasoning_content"] != "think-1" || m["partial"] != true {
		t.Fatalf("the second ask didn't carry what the client has to go on from, in Kimi's partial mode: %v", m)
	}
	if got := textOf(body); got != "hello" {
		t.Fatalf("client's text = %q, want %q", got, "hello")
	}
	if got := thinkOf(body); got != "think-1" {
		// a continuation's fresh thinking isn't shown a second time
		t.Fatalf("client's thinking = %q, want %q", got, "think-1")
	}
	if starts, stops, errs := messageStops(body); starts != 1 || stops != 1 || errs != 0 {
		t.Fatalf("starts %d, stops %d, errors %d, want 1/1/0: %s", starts, stops, errs, body)
	}
}

// A model that says what it was prefilled with again before going on has
// the echo dropped: the client reads the text once.
func TestStreamCutContinuationEchoDropped(t *testing.T) {
	f := &cutChat{
		cuts:  map[int][]string{0: {chatText("kimi-k3", "hel")}},
		whole: []string{chatText("kimi-k3", "hel"), chatText("kimi-k3", "lo"), chatStop("kimi-k3")},
	}
	body := continuationServer(t, "kimi-k3", f)
	if f.calls() != 2 {
		t.Fatalf("calls = %d, want 2", f.calls())
	}
	if got := textOf(body); got != "hello" {
		t.Fatalf("client's text = %q, want %q (echo not dropped)", got, "hello")
	}
	if _, _, errs := messageStops(body); errs != 0 {
		t.Fatalf("error events in the stream: %s", body)
	}
}

// A reply a tool call of has begun can't be prefilled and go on: it ends
// with the error in the stream, as it used to.
func TestStreamCutAfterToolCallEndsAsBefore(t *testing.T) {
	f := &cutChat{
		cuts: map[int][]string{0: {
			chatText("kimi-k3", "hel"),
			`{"id":"c1","model":"kimi-k3","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"ci"}}]}}]}`,
		}},
	}
	body := continuationServer(t, "kimi-k3", f)
	if f.calls() != 1 {
		t.Fatalf("a reply with a tool call begun was asked again: %d calls", f.calls())
	}
	if _, _, errs := messageStops(body); errs != 1 {
		t.Fatalf("no error event in the stream: %s", body)
	}
	if !strings.Contains(body, "connection lost mid-reply") {
		t.Fatalf("the error doesn't say the connection was lost: %s", body)
	}
}

// A reply whose every ask is cut ends with the error after the tries are
// up, as it used to after one.
func TestStreamCutContinuesOnlySoLong(t *testing.T) {
	f := &cutChat{
		cuts: map[int][]string{0: {chatText("kimi-k3", "hel")}, 1: {chatText("kimi-k3", "hel")}, 2: {chatText("kimi-k3", "hel")}},
	}
	body := continuationServer(t, "kimi-k3", f)
	if f.calls() != 1+streamRetries {
		t.Fatalf("calls = %d, want %d", f.calls(), 1+streamRetries)
	}
	if _, _, errs := messageStops(body); errs != 1 {
		t.Fatalf("no error event in the stream: %s", body)
	}
	if got := textOf(body); got != "hel" {
		t.Fatalf("client's text = %q, want %q", got, "hel")
	}
}

// A reply cut while it was only thinking goes on from the thinking the
// client has, a model that reads it back asked with it; the client reads
// its thinking once, then the answer.
func TestStreamCutMidThinkingContinues(t *testing.T) {
	f := &cutChat{
		cuts:  map[int][]string{0: {chatThink("kimi-k3", "think-1")}},
		whole: []string{chatThink("kimi-k3", "rethink"), chatText("kimi-k3", "done"), chatStop("kimi-k3")},
	}
	body := continuationServer(t, "kimi-k3", f)
	if f.calls() != 2 {
		t.Fatalf("calls = %d, want 2", f.calls())
	}
	m := f.lastAssistant(t, 1)
	if m == nil || m["reasoning_content"] != "think-1" || m["partial"] != true {
		t.Fatalf("the second ask didn't carry the thinking to go on from, in Kimi's partial mode: %v", m)
	}
	if got := thinkOf(body); got != "think-1" {
		t.Fatalf("client's thinking = %q, want %q", got, "think-1")
	}
	if got := textOf(body); got != "done" {
		t.Fatalf("client's text = %q, want %q", got, "done")
	}
	if _, _, errs := messageStops(body); errs != 0 {
		t.Fatalf("error events in the stream: %s", body)
	}
}

// A reply cut while it was only thinking goes on where the upstream
// prefills natively (an Anthropic one): its thinking can't be prefilled —
// Anthropic takes a reply's thinking back only signed — so the same
// conversation is asked again without it and answers anew; the client
// reads the thinking the cut reply had, then the fresh answer.
func TestStreamCutMidThinkingContinuesOnAnthropic(t *testing.T) {
	f := &cutAnthropic{
		cuts:  map[int][]string{0: {anthStart("m", 7), anthThink("think-1")}},
		whole: []string{anthStart("m", 11), anthThink("rethink"), anthText("done"), anthStop(4)},
	}
	body := continuationChatServer(t, "m", f)
	if f.calls() != 2 {
		t.Fatalf("calls = %d, want 2", f.calls())
	}
	if prefill, ok := f.lastAssistantText(t, 1); ok {
		t.Fatalf("a reply with nothing prefillable was sent back as a prefill: %q", prefill)
	}
	if got := chatThinkOf(body); got != "think-1" {
		t.Fatalf("client's thinking = %q, want %q", got, "think-1")
	}
	if got := chatTextOf(body); got != "done" {
		t.Fatalf("client's text = %q, want %q", got, "done")
	}
}

// A stream's own error event mid-reply, the connection kept open after
// it, ends the read at once and the reply goes on: the client isn't made
// to wait on the vendor hanging up.
func TestStreamErrorMidReplyContinuesAtOnce(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var calls int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if !first {
			io.WriteString(w, sse("data: "+chatText("kimi-k3", "lo"), "data: "+chatStop("kimi-k3"), "data: [DONE]"))
			return
		}
		io.WriteString(w, sse("data: "+chatText("kimi-k3", "hel"), `data: {"error":{"message":"boom"}}`))
		w.(http.Flusher).Flush()
		select { // said, and not hung up
		case <-r.Context().Done():
		case <-time.After(20 * time.Second):
			t.Error("the error event's read wasn't ended at once")
		}
	}))
	t.Cleanup(up.Close)
	p := provider.Provider{ID: "up", Name: "UP", Key: "k", Models: []string{"kimi-k3"}, Chat: up.URL + "/v1"}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	body := continuationServerBody(t, "kimi-k3")
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
	if got := textOf(body); got != "hello" {
		t.Fatalf("client's text = %q, want %q", got, "hello")
	}
}

func continuationServerBody(t *testing.T, model string) string {
	t.Helper()
	code, body := post(t, "/v1/messages", `{"model":"up/`+model+`","max_tokens":32000,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	return body
}

// unecho drops a full echo of what a continuation was prefilled with,
// however it comes in pieces: the client already has the prefill, so a
// stream that starts with it is one, and what follows goes on; anything
// else goes whole.
func TestContinuationUnecho(t *testing.T) {
	for _, c := range []struct {
		echo   string
		chunks []string
		want   string
	}{
		{"hel", []string{"lo"}, "lo"},            // a plain continuation
		{"hel", []string{"hel", "lo"}, "lo"},     // the echo, then the continuation
		{"hel", []string{"h", "e", "llo"}, "lo"}, // the echo in pieces
		{"hel", []string{"he", "lp"}, "p"},       // starts with the prefill: an echo
		{"hel", []string{"x", "yz"}, "xyz"},      // nothing alike goes whole
		{"hel", []string{"hel"}, ""},             // the echo alone
		{"hel", []string{"he"}, ""},              // what could still be one is held
		{"", []string{"hel"}, "hel"},             // nothing prefilled: nothing dropped
	} {
		con := &continuation{echo: c.echo}
		var sb strings.Builder
		for _, ch := range c.chunks {
			sb.WriteString(con.unecho(ch))
		}
		if sb.String() != c.want {
			t.Errorf("unecho(%q, %v) = %q, want %q", c.echo, c.chunks, sb.String(), c.want)
		}
	}
}

// A Chat upstream without a prefill mode answers the reply's part sent
// back again from the start, reworded — spliced onto the part the client
// has, with no error, the client would keep the garble as the reply.
// There the cut reply ends with the error, as it used to: the agent can
// try the turn again.
func TestStreamCutOnPlainChatEndsAsBefore(t *testing.T) {
	f := &cutChat{
		cuts:  map[int][]string{0: {chatText("m", "Go's garbage collector is a concurrent, tri-color")}},
		whole: []string{chatText("m", "Go uses a concurrent, tri-color mark-and-sweep garbage collector"), chatStop("m")},
	}
	body := continuationServer(t, "m", f)
	if f.calls() != 1 {
		t.Fatalf("an upstream that re-answers was asked to go on: %d calls", f.calls())
	}
	if got := textOf(body); got != "Go's garbage collector is a concurrent, tri-color" {
		t.Fatalf("client's text = %q, want only the part of the cut reply", got)
	}
	if _, _, errs := messageStops(body); errs != 1 {
		t.Fatalf("no error event in the stream: %s", body)
	}
}

// DeepSeek goes on from the reply's part in its prefix mode: the
// continuation is asked of the /beta endpoint with "prefix": true on the
// message.
func TestStreamCutContinuesWithDeepSeekPrefix(t *testing.T) {
	f := &cutChat{
		cuts:  map[int][]string{0: {chatText("deepseek-chat", "hel")}},
		whole: []string{chatText("deepseek-chat", "lo"), chatStop("deepseek-chat")},
	}
	body := continuationServer(t, "deepseek-chat", f)
	if f.calls() != 2 {
		t.Fatalf("calls = %d, want 2", f.calls())
	}
	if p := f.path(1); p != "/beta/chat/completions" {
		t.Fatalf("the continuation went to %q, want DeepSeek's prefix-mode /beta path", p)
	}
	m := f.lastAssistant(t, 1)
	if m == nil || m["content"] != "hel" || m["prefix"] != true {
		t.Fatalf("the continuation wasn't DeepSeek's prefix mode: %v", m)
	}
	if got := textOf(body); got != "hello" {
		t.Fatalf("client's text = %q, want %q", got, "hello")
	}
}

// An Anthropic upstream goes on from the reply's part natively: the
// continuation is the same conversation with it as the last, assistant,
// message — no mode field — and the client reads one whole reply.
func TestStreamCutContinuesOnAnthropic(t *testing.T) {
	f := &cutAnthropic{
		cuts:  map[int][]string{0: {anthStart("m", 7), anthText("hel")}},
		whole: []string{anthStart("m", 11), anthText("lo"), anthStop(4)},
	}
	body := continuationChatServer(t, "m", f)
	if f.calls() != 2 {
		t.Fatalf("calls = %d, want 2", f.calls())
	}
	prefill, ok := f.lastAssistantText(t, 1)
	if !ok || prefill != "hel" {
		t.Fatalf("the second ask didn't carry what the client has to go on from: %q, %v", prefill, ok)
	}
	if got := chatTextOf(body); got != "hello" {
		t.Fatalf("client's text = %q, want %q", got, "hello")
	}
}

// A prefill's trailing whitespace, which Anthropic turns the request away
// for, is trimmed: the client has it already, and the model goes on from
// the reply's last word.
func TestStreamCutTrimsPrefillWhitespace(t *testing.T) {
	f := &cutAnthropic{
		cuts:  map[int][]string{0: {anthStart("m", 7), anthText("hel\n")}},
		whole: []string{anthStart("m", 11), anthText("lo"), anthStop(4)},
	}
	body := continuationChatServer(t, "m", f)
	if f.calls() != 2 {
		t.Fatalf("calls = %d, want 2", f.calls())
	}
	if prefill, _ := f.lastAssistantText(t, 1); prefill != "hel" {
		t.Fatalf("the prefill kept its trailing whitespace: %q", prefill)
	}
	if got := chatTextOf(body); got != "hel\nlo" {
		t.Fatalf("client's text = %q, want %q", got, "hel\nlo")
	}
}

// An upstream that turns the prefill itself away (the newest Claude
// models answer one with a 400) ends the reply with the error, as it used
// to, rather than ask again.
func TestStreamCutPrefillRefusedEndsAsBefore(t *testing.T) {
	f := &cutAnthropic{
		cuts:  map[int][]string{0: {anthStart("m", 7), anthText("hel")}},
		fails: map[int]string{1: `{"type":"error","error":{"type":"invalid_request_error","message":"prefill is not supported for this model"}}`},
	}
	body := continuationChatServer(t, "m", f)
	if f.calls() != 2 {
		t.Fatalf("calls = %d, want 2", f.calls())
	}
	if got := chatTextOf(body); got != "hel" {
		t.Fatalf("client's text = %q, want %q", got, "hel")
	}
	if !strings.Contains(body, "prefill is not supported") {
		t.Fatalf("the prefill's refusal didn't reach the client: %s", body)
	}
}

// The tries are each billed in the ledger, and the client is told the
// last try's prompt: the continuation re-sent the cut try's.
func TestStreamCutUsageTellsLastTry(t *testing.T) {
	f := &cutAnthropic{
		cuts:  map[int][]string{0: {anthStart("m", 7), anthText("hel")}},
		whole: []string{anthStart("m", 11), anthText("lo"), anthStop(4)},
	}
	body := continuationChatServer(t, "m", f)
	u := chatUsageOf(body)
	if u == nil {
		t.Fatalf("no usage in the stream: %s", body)
	}
	// the client is told the continuation's prompt, which holds the cut
	// try's; both are billed (sleepy_shibe, #5115)
	if u["prompt_tokens"] != 11.0 || u["completion_tokens"] != 4.0 {
		t.Fatalf("usage = %v, want the last try's prompt 11, completion 4", u)
	}
	if r := usage.Load(time.Time{}); len(r) != 1 || r[0].Input != 18 {
		t.Fatalf("ledger %+v, want both tries billed: 7+11=18", r)
	}
}

// chatPrefill names DeepSeek's and Kimi's own prefill modes on their own
// APIs, and takes a relay on this machine or the LAN serving one of their
// models to front the vendor; every other Chat upstream re-answers.
func TestChatPrefill(t *testing.T) {
	for _, c := range []struct{ host, model, want string }{
		{"api.deepseek.com", "deepseek-chat", "prefix"},
		{"api.moonshot.cn", "kimi-k3", "partial"},
		{"api.moonshot.ai", "kimi-k3", "partial"},
		{"api.kimi.ai", "kimi-for-coding", "partial"},
		{"api.openai.com", "gpt-5", ""},
		{"openrouter.ai", "moonshotai/kimi-k3", ""}, // a relay elsewhere isn't taken to pass it on
		{"relay.example.com", "kimi-k3", ""},
		{"127.0.0.1:8080", "kimi-k3", "partial"},
		{"127.0.0.1:8080", "deepseek-chat", "prefix"},
		{"[::1]:8080", "kimi-k3", "partial"},
		{"192.168.1.5:8080", "kimi-k3", "partial"},
		{"localhost:8317", "kimi-for-coding", "partial"},
		{"127.0.0.1:8080", "gpt-5", ""},
	} {
		if got := chatPrefill(c.host, c.model); got != c.want {
			t.Errorf("chatPrefill(%q, %q) = %q, want %q", c.host, c.model, got, c.want)
		}
	}
}

// prefillHow: an Anthropic Messages upstream goes on from a trailing
// assistant message natively, a Chat one in its vendor's own mode, and
// every other API not at all.
func TestPrefillHow(t *testing.T) {
	chat := provider.Provider{Chat: "https://api.moonshot.cn/v1"}
	if got := prefillHow(chat, provider.Chat, "kimi-k3"); got != "partial" {
		t.Errorf("prefillHow(moonshot, Chat) = %q, want partial", got)
	}
	if got := prefillHow(chat, provider.Anthropic, "kimi-k3"); got != "anthropic" {
		t.Errorf("prefillHow(moonshot, Anthropic) = %q, want anthropic", got)
	}
	if got := prefillHow(chat, provider.Responses, "kimi-k3"); got != "" {
		t.Errorf("prefillHow(moonshot, Responses) = %q, want none", got)
	}
	plain := provider.Provider{Chat: "https://api.openai.com/v1"}
	if got := prefillHow(plain, provider.Chat, "gpt-5"); got != "" {
		t.Errorf("prefillHow(openai, Chat) = %q, want none", got)
	}
}

// A Resume request's last assistant message carries the upstream's own
// prefill mark — DeepSeek's prefix, Kimi's partial — and its reasoning
// where the upstream reads reasoning_content back; an upstream without a
// mode gets neither (and is never asked to go on).
func TestBuildChatResumePrefillMarks(t *testing.T) {
	r := &Request{Resume: true, Messages: []Message{
		{Role: "user", Parts: []Part{{Kind: Text, Text: "hi"}}},
		{Role: "assistant", Parts: []Part{{Kind: Thinking, Text: "think-1"}, {Kind: Text, Text: "hel"}}},
	}}
	lastOf := func(body []byte) map[string]any {
		var v struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatalf("built request isn't JSON: %v", err)
		}
		return v.Messages[len(v.Messages)-1]
	}
	m := lastOf(buildChat(r, "deepseek-chat", "api.deepseek.com", false))
	if m["prefix"] != true || m["reasoning_content"] != "think-1" {
		t.Errorf("DeepSeek's prefill = %v, want prefix and the reasoning", m)
	}
	m = lastOf(buildChat(r, "kimi-k3", "api.moonshot.cn", false))
	if m["partial"] != true || m["reasoning_content"] != "think-1" {
		t.Errorf("Kimi's prefill = %v, want partial and the reasoning", m)
	}
	m = lastOf(buildChat(r, "gpt-5", "api.openai.com", false))
	if m["prefix"] != nil || m["partial"] != nil || m["reasoning_content"] != nil {
		t.Errorf("an upstream without a prefill mode got one marked: %v", m)
	}
}

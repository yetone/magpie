package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// nativeAnthropicToolStream is a Messages reply whose text block is followed
// by a tool_use the model made itself. Its events, data lines and blank
// delimiters are the shape a Messages upstream streams.
const nativeAnthropicToolStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_relay_native","type":"message","role":"assistant","model":"relay-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":21,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Checking: "}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_native_01","name":"Bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"echo hi\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":12}}

event: message_stop
data: {"type":"message_stop"}

`

// writtenAnthropicToolStream is a Messages reply whose text holds a call
// written the way Claude's own calls look. The relay turns it into a
// tool_use block after the words before it.
const writtenAnthropicToolStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_relay_written","type":"message","role":"assistant","model":"relay-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":18,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I'll check: <invoke name=\"Bash\"><parameter name=\"command\">echo hi</parameter></invoke>"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":9}}

event: message_stop
data: {"type":"message_stop"}

`

// writtenAtStartAnthropicToolStream is writtenAnthropicToolStream with no
// words before the written call, so the text block's held start is replaced
// by the call's block.
const writtenAtStartAnthropicToolStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_relay_start","type":"message","role":"assistant","model":"relay-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":17,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"<invoke name=\"Bash\"><parameter name=\"command\">echo hi</parameter></invoke>"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":8}}

event: message_stop
data: {"type":"message_stop"}

`

// heldTextAnthropicStream is a Messages reply whose text starts with a call
// of a tool the request didn't offer. The relay holds the whole text block,
// then has to release its held start and stop as text when the reply ends.
const heldTextAnthropicStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_relay_held","type":"message","role":"assistant","model":"relay-model","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":16,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"<invoke name=\"NotOffered\"><parameter name=\"x\">y</parameter></invoke>"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":7}}

event: message_stop
data: {"type":"message_stop"}

`

type anthropicSSEChunk struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
}

type anthropicSSEFrame struct {
	event string
	chunk anthropicSSEChunk
}

// readAnthropicSSE reads a Messages stream strictly: every frame must name
// its event and carry exactly one data line whose JSON parses. A frame that
// lost its event header, or whose data lines were joined, fails here rather
// than being skipped by a lenient parser.
func readAnthropicSSE(t *testing.T, body string) []anthropicSSEFrame {
	t.Helper()
	// CRLF and LF both delimit SSE lines; the relay may pass an upstream's
	// CRLF frames through while making its own with LF.
	body = strings.ReplaceAll(body, "\r\n", "\n")
	var out []anthropicSSEFrame
	for _, frame := range strings.Split(body, "\n\n") {
		if strings.TrimSpace(frame) == "" {
			continue
		}
		var event string
		var data []string
		for _, line := range strings.Split(frame, "\n") {
			switch {
			case line == "":
			case strings.HasPrefix(line, "event:"):
				if event != "" {
					t.Fatalf("SSE frame names two events:\n%s", frame)
				}
				event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
				if event == "" {
					t.Fatalf("SSE frame has an empty event name:\n%s", frame)
				}
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			default:
				t.Fatalf("SSE frame has a line that is neither event nor data: %q\n%s", line, frame)
			}
		}
		if event == "" {
			t.Fatalf("SSE frame reached the client without an event line:\n%s", frame)
		}
		if len(data) != 1 {
			t.Fatalf("SSE frame %q has %d data lines, want one:\n%s", event, len(data), frame)
		}
		var chunk anthropicSSEChunk
		if err := json.Unmarshal([]byte(data[0]), &chunk); err != nil {
			t.Fatalf("SSE frame %q has unreadable data %q: %v", event, data[0], err)
		}
		out = append(out, anthropicSSEFrame{event: event, chunk: chunk})
	}
	if len(out) == 0 {
		t.Fatal("the reply carried no SSE events")
	}
	return out
}

type wantAnthropicCall struct {
	name string
	id   string // "" when the relay made the call and its id is its own
	args string
}

// checkAnthropicStream asserts what a Messages client sees: events whose
// names match their data, blocks opened and closed one at a time in index
// order, the text, calls and stop reason expected.
func checkAnthropicStream(t *testing.T, frames []anthropicSSEFrame, wantBlocks []string, wantText, wantStop string, wantCalls []wantAnthropicCall) {
	t.Helper()
	if frames[0].event != "message_start" {
		t.Fatalf("the reply begins with %q, not message_start", frames[0].event)
	}
	open, next := -1, 0
	var blocks []string
	var text strings.Builder
	type toolBlock struct{ id, name, input string }
	var tools []*toolBlock
	ended := false
	for i, f := range frames {
		if f.event != f.chunk.Type {
			t.Fatalf("frame %d says event %q but data type %q", i, f.event, f.chunk.Type)
		}
		switch f.event {
		case "message_start":
			if i != 0 {
				t.Fatalf("message_start is frame %d", i)
			}
		case "content_block_start":
			if open >= 0 {
				t.Fatalf("block %d opened while block %d was open", f.chunk.Index, open)
			}
			if f.chunk.Index != next {
				t.Fatalf("block %d opened, want %d", f.chunk.Index, next)
			}
			open, next = f.chunk.Index, next+1
			blocks = append(blocks, f.chunk.ContentBlock.Type)
			if f.chunk.ContentBlock.Type == "tool_use" {
				tools = append(tools, &toolBlock{id: f.chunk.ContentBlock.ID, name: f.chunk.ContentBlock.Name})
			}
		case "content_block_delta":
			if f.chunk.Index != open {
				t.Fatalf("block %d got a delta while block %d was open", f.chunk.Index, open)
			}
			switch f.chunk.Delta.Type {
			case "text_delta":
				text.WriteString(f.chunk.Delta.Text)
			case "input_json_delta":
				if len(tools) == 0 || f.chunk.Index != next-1 {
					t.Fatalf("input for block %d reached the client without its start", f.chunk.Index)
				}
				tools[len(tools)-1].input += f.chunk.Delta.PartialJSON
			default:
				t.Fatalf("block %d has an unexpected delta type %q", f.chunk.Index, f.chunk.Delta.Type)
			}
		case "content_block_stop":
			if f.chunk.Index != open {
				t.Fatalf("block %d stopped while block %d was open", f.chunk.Index, open)
			}
			open = -1
		case "message_delta":
			if open >= 0 {
				t.Fatalf("the reply ended while block %d was open", open)
			}
			if f.chunk.Delta.StopReason != wantStop {
				t.Fatalf("the reply stopped for %q, want %q", f.chunk.Delta.StopReason, wantStop)
			}
		case "message_stop":
			if open >= 0 {
				t.Fatalf("the reply stopped while block %d was open", open)
			}
			ended = true
		default:
			t.Fatalf("unexpected event %q", f.event)
		}
	}
	if open >= 0 {
		t.Fatalf("the reply ended with block %d open", open)
	}
	if !ended {
		t.Fatal("the reply carried no message_stop")
	}
	if got := strings.Join(blocks, ","); got != strings.Join(wantBlocks, ",") {
		t.Fatalf("blocks %v, want %v", blocks, wantBlocks)
	}
	if text.String() != wantText {
		t.Fatalf("text %q, want %q", text.String(), wantText)
	}
	if len(tools) != len(wantCalls) {
		t.Fatalf("%d tool calls reached the client, want %d", len(tools), len(wantCalls))
	}
	for i, want := range wantCalls {
		got := tools[i]
		if got.name != want.name || got.id == "" || want.id != "" && got.id != want.id {
			t.Fatalf("call %d is id %q name %q, want id %q name %q", i, got.id, got.name, want.id, want.name)
		}
		var a, b any
		if json.Unmarshal([]byte(got.input), &a) != nil || json.Unmarshal([]byte(want.args), &b) != nil || !jsonSame(a, b) {
			t.Fatalf("call %d args %q, want %q", i, got.input, want.args)
		}
	}
}

func jsonSame(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// chunkAnthropicSSE cuts a stream at the boundaries a provider's writes may
// land on: whole, one event, one line, one byte, and one CRLF line.
func chunkAnthropicSSE(stream, how string) []string {
	if how == "crlf lines" {
		stream = strings.ReplaceAll(stream, "\n", "\r\n")
	}
	sep := "\n"
	switch how {
	case "whole":
		return []string{stream}
	case "events":
		sep = "\n\n"
	case "bytes":
		out := make([]string, 0, len(stream))
		for i := range len(stream) {
			out = append(out, stream[i:i+1])
		}
		return out
	}
	out := strings.SplitAfter(stream, sep)
	if n := len(out); n > 0 && out[n-1] == "" {
		out = out[:n-1]
	}
	return out
}

// The relayed Messages reply is read from whole events, whatever write
// boundaries the upstream used: an event line, its data line and the blank
// line may each arrive alone. Every event reaches the client with its name
// and one parseable data value, blocks open and close in order, and the
// native tool_use stays the call it was.
func TestAnthropicTextCallsSSEChunksKeepFraming(t *testing.T) {
	cases := []struct {
		name   string
		stream string
		blocks []string
		text   string
		stop   string
		calls  []wantAnthropicCall
	}{
		{
			name: "native text then tool use", stream: nativeAnthropicToolStream,
			blocks: []string{"text", "tool_use"}, text: "Checking: ", stop: "tool_use",
			calls: []wantAnthropicCall{{name: "Bash", id: "toolu_native_01", args: `{"command":"echo hi"}`}},
		},
		{
			name: "written call after text", stream: writtenAnthropicToolStream,
			blocks: []string{"text", "tool_use"}, text: "I'll check: ", stop: "tool_use",
			calls: []wantAnthropicCall{{name: "Bash", args: `{"command":"echo hi"}`}},
		},
		{
			name: "written call at the start", stream: writtenAtStartAnthropicToolStream,
			blocks: []string{"tool_use"}, text: "", stop: "tool_use",
			calls: []wantAnthropicCall{{name: "Bash", args: `{"command":"echo hi"}`}},
		},
		{
			name: "held text released whole", stream: heldTextAnthropicStream,
			blocks: []string{"text"}, text: `<invoke name="NotOffered"><parameter name="x">y</parameter></invoke>`, stop: "end_turn",
		},
	}
	chunkings := []string{"whole", "events", "lines", "bytes", "crlf lines"}
	for _, c := range cases {
		for _, how := range chunkings {
			t.Run(c.name+"/"+how, func(t *testing.T) {
				tc := &anthropicTextCalls{names: map[string]bool{"Bash": true}, sse: true}
				var out strings.Builder
				for _, part := range chunkAnthropicSSE(c.stream, how) {
					out.Write(tc.write([]byte(part)))
				}
				out.Write(tc.flush())
				checkAnthropicStream(t, readAnthropicSSE(t, out.String()), c.blocks, c.text, c.stop, c.calls)
			})
		}
		// two reads, cut at every byte: every place one line ends and
		// the next begins among them, LF and CRLF
		for _, crlf := range []bool{false, true} {
			stream := c.stream
			if crlf {
				stream = strings.ReplaceAll(stream, "\n", "\r\n")
			}
			t.Run(fmt.Sprintf("%s/every cut crlf=%v", c.name, crlf), func(t *testing.T) {
				for at := 1; at < len(stream); at++ {
					tc := &anthropicTextCalls{names: map[string]bool{"Bash": true}, sse: true}
					out := append(tc.write([]byte(stream[:at])), tc.write([]byte(stream[at:]))...)
					out = append(out, tc.flush()...)
					ok := t.Run(fmt.Sprintf("at %d", at), func(t *testing.T) {
						checkAnthropicStream(t, readAnthropicSSE(t, string(out)), c.blocks, c.text, c.stop, c.calls)
					})
					if !ok {
						return // one cut's failure says it; the rest would say it again
					}
				}
			})
		}
	}
}

// lineCuts is the stream in two reads, cut after each of its lines.
func lineCuts(stream string) [][]string {
	var out [][]string
	for at := 0; at < len(stream)-1; at++ {
		if stream[at] == '\n' {
			out = append(out, []string{stream[:at+1], stream[at+1:]})
		}
	}
	return out
}

// sseChunkBody hands the relay the upstream's writes one at a time, so a
// test controls the read boundaries without a socket or a sleep.
type sseChunkBody struct {
	chunks []string
}

func (b *sseChunkBody) Read(p []byte) (int, error) {
	if len(b.chunks) == 0 {
		return 0, io.EOF
	}
	part := b.chunks[0]
	b.chunks = b.chunks[1:]
	if len(part) > len(p) {
		b.chunks = append([]string{part[len(p):]}, b.chunks...)
		part = part[:len(p)]
	}
	return copy(p, part), nil
}

func (b *sseChunkBody) Close() error { return nil }

// The same chunk boundaries through magpie's Messages relay: the reply the
// agent reads is the stream the upstream sent, with each event complete and
// one data value each, not an event whose header went in an earlier write or
// whose data values were joined.
func TestAnthropicMessagesSSEChunksKeepFraming(t *testing.T) {
	setup(t, provider.Anthropic, &fake{t: t})
	var chunks []string
	s := New()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body:       &sseChunkBody{chunks: append([]string(nil), chunks...)},
			Request:    r,
		}, nil
	})}
	const ask = `{"model":"fake/m1","max_tokens":256,"stream":true,` +
		`"tools":[{"name":"Bash","description":"Run a shell command","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}],` +
		`"messages":[{"role":"user","content":"run echo hi"}]}`
	cases := []struct {
		name   string
		stream string
		blocks []string
		text   string
		stop   string
		calls  []wantAnthropicCall
	}{
		{
			name: "native text then tool use", stream: nativeAnthropicToolStream,
			blocks: []string{"text", "tool_use"}, text: "Checking: ", stop: "tool_use",
			calls: []wantAnthropicCall{{name: "Bash", id: "toolu_native_01", args: `{"command":"echo hi"}`}},
		},
		{
			name: "written call after text", stream: writtenAnthropicToolStream,
			blocks: []string{"text", "tool_use"}, text: "I'll check: ", stop: "tool_use",
			calls: []wantAnthropicCall{{name: "Bash", args: `{"command":"echo hi"}`}},
		},
		{
			name: "written call at the start", stream: writtenAtStartAnthropicToolStream,
			blocks: []string{"tool_use"}, text: "", stop: "tool_use",
			calls: []wantAnthropicCall{{name: "Bash", args: `{"command":"echo hi"}`}},
		},
		{
			name: "held text released whole", stream: heldTextAnthropicStream,
			blocks: []string{"text"}, text: `<invoke name="NotOffered"><parameter name="x">y</parameter></invoke>`, stop: "end_turn",
		},
	}
	for _, c := range cases {
		for _, how := range []string{"whole", "events", "lines", "bytes", "crlf lines"} {
			t.Run(c.name+"/"+how, func(t *testing.T) {
				chunks = chunkAnthropicSSE(c.stream, how)
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(ask))
				req.Header.Set("anthropic-version", "2023-06-01")
				req.Header.Set("User-Agent", "claude-cli/2.1.316 (external, cli)")
				s.Handler().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("%d %s", rec.Code, rec.Body)
				}
				checkAnthropicStream(t, readAnthropicSSE(t, rec.Body.String()), c.blocks, c.text, c.stop, c.calls)
			})
		}
		t.Run(c.name+"/each line cut", func(t *testing.T) {
			for i, cut := range lineCuts(c.stream) {
				chunks = cut
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(ask))
				req.Header.Set("anthropic-version", "2023-06-01")
				req.Header.Set("User-Agent", "claude-cli/2.1.316 (external, cli)")
				s.Handler().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("cut %d: %d %s", i, rec.Code, rec.Body)
				}
				if !t.Run(fmt.Sprintf("cut %d", i), func(t *testing.T) {
					checkAnthropicStream(t, readAnthropicSSE(t, rec.Body.String()), c.blocks, c.text, c.stop, c.calls)
				}) {
					return
				}
			}
		})
	}
	// a request that offers no tools: nothing reads the reply for calls,
	// and it reaches the client as the upstream cut it, whole events each
	const plain = `{"model":"fake/m1","max_tokens":256,"stream":true,"messages":[{"role":"user","content":"run echo hi"}]}`
	for _, how := range []string{"lines", "bytes", "crlf lines"} {
		t.Run("no tools/"+how, func(t *testing.T) {
			chunks = chunkAnthropicSSE(nativeAnthropicToolStream, how)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/v1/messages?beta=true", strings.NewReader(plain))
			req.Header.Set("anthropic-version", "2023-06-01")
			req.Header.Set("User-Agent", "claude-cli/2.1.316 (external, cli)")
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			checkAnthropicStream(t, readAnthropicSSE(t, rec.Body.String()), []string{"text", "tool_use"}, "Checking: ", "tool_use",
				[]wantAnthropicCall{{name: "Bash", id: "toolu_native_01", args: `{"command":"echo hi"}`}})
		})
	}
}

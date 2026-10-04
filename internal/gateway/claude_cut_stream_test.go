package gateway

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// cutStart is the start of the reporter's first attempt (#726): the whole
// conversation written to the cache, 5 tokens out so far.
const cutStart = `{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_cut","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":467708,"cache_read_input_tokens":0,"output_tokens":5}}}}`

// cutAttempt is an attempt Anthropic's stream cut short in its thinking,
// as Claude Code 2.1.28x tells it: its own content_block_stop and
// message_stop, no message_delta, then an api_retry.
var cutAttempt = []string{
	cutStart,
	`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}}`,
	`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"thinking so far. "}}}`,
	`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
	`{"type":"stream_event","event":{"type":"message_stop"}}`,
	`{"type":"assistant","message":{"id":"msg_cut","model":"claude-opus-5-5","role":"assistant","content":[{"type":"thinking","thinking":"thinking so far. ","signature":""}],"stop_reason":null,"usage":{"input_tokens":2,"cache_creation_input_tokens":467708,"cache_read_input_tokens":0,"output_tokens":5}}}`,
	`{"type":"system","subtype":"api_retry","attempt":1,"max_retries":10,"retry_delay_ms":500,"error_status":null,"error":"unknown"}`,
}

// claudeReply runs lines through readOutput and relay as an Anthropic
// Messages stream, and says what the client was told and how the run was
// left.
func claudeReply(t *testing.T, run *subscriptionRun, lines []string, hold bool) (text, stop string, out, write int, raw string, ended string, ok bool) {
	t.Helper()
	segment := run.attach()
	pr, pw := io.Pipe()
	go run.readOutput(pr)
	go func() {
		for _, l := range lines {
			pw.Write([]byte(l + "\n"))
		}
		if !hold { // Claude Code waits on its next input, its output open
			pw.Close()
		}
	}()
	t.Cleanup(func() { pw.Close() })
	rec := httptest.NewRecorder()
	req := &Request{Model: "claude-opus-5-5", Stream: true}
	var usage Usage
	relay(rec, httptest.NewRequest("POST", "/v1/messages", nil), provider.Anthropic, "Claude Code", req, segment, &usage,
		func() {}, func(said, s string, k bool) { ended, ok = s, k })
	raw = rec.Body.String()
	var tools []string
	for _, l := range strings.Split(raw, "\n") {
		data, found := strings.CutPrefix(l, "data: ")
		if !found {
			continue
		}
		var ev struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content_block"`
			Delta struct {
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				Output     int `json:"output_tokens"`
				CacheWrite int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				tools = append(tools, ev.ContentBlock.Name)
			}
		case "content_block_delta":
			text += ev.Delta.Text
		case "message_delta":
			stop, out, write = ev.Delta.StopReason, ev.Usage.Output, ev.Usage.CacheWrite
		}
	}
	if len(tools) > 0 {
		text += "[" + strings.Join(tools, ",") + "]"
	}
	return
}

// Anthropic's stream is cut short and Claude Code asks again, streamed: the
// client hears the answer of the attempt that gave one, with its usage and
// stop, and the run is kept for the next turn (#726). The old code took
// Claude Code's own message_stop for the reply's end: end_turn, 5 tokens
// out, no answer, and the run ended.
func TestClaudeCutStreamGoesOnInRetry(t *testing.T) {
	lines := append(append([]string{}, cutAttempt...),
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_retry","model":"claude-opus-5-5","usage":{"input_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":467708,"output_tokens":5}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"the answer"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12000}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"the answer"}`,
	)
	text, stop, out, write, raw, ended, ok := claudeReply(t, &subscriptionRun{}, lines, true)
	if text != "the answer" || stop != "end_turn" || ended != "stop" || !ok {
		t.Fatalf("reply %q stop %q, run ended %q %v:\n%s", text, stop, ended, ok, raw)
	}
	// both attempts were made, and are counted
	if out != 12005 || write != 467708 {
		t.Fatalf("usage: %d out, %d written:\n%s", out, write, raw)
	}
}

// Cut short twice, Claude Code asks without a stream and tells the answer
// in whole messages only: the client hears it.
func TestClaudeCutStreamAnsweredUnstreamed(t *testing.T) {
	lines := append(append(append([]string{}, cutAttempt...), cutAttempt...),
		`{"type":"assistant","message":{"id":"msg_whole","model":"claude-opus-5-5","role":"assistant","content":[{"type":"thinking","thinking":"NS thinking","signature":"sig"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"cache_read_input_tokens":467708,"output_tokens":777}}}`,
		`{"type":"assistant","message":{"id":"msg_whole","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"NONSTREAM ANSWER"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"cache_read_input_tokens":467708,"output_tokens":777}}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"NONSTREAM ANSWER"}`,
	)
	text, stop, out, _, raw, ended, ok := claudeReply(t, &subscriptionRun{}, lines, true)
	if text != "NONSTREAM ANSWER" || stop != "end_turn" || ended != "stop" || !ok {
		t.Fatalf("reply %q stop %q, run ended %q %v:\n%s", text, stop, ended, ok, raw)
	}
	if out != 787 {
		t.Fatalf("usage: %d out:\n%s", out, raw)
	}
	if !strings.Contains(raw, "NS thinking") {
		t.Fatalf("thinking lost:\n%s", raw)
	}
}

// An unstreamed answer calling the client's tools: Claude Code then calls
// them and waits, writing nothing more, so the reply ends once its blocks
// stop coming.
func TestClaudeCutStreamUnstreamedToolCalls(t *testing.T) {
	was := unstreamedQuiet
	unstreamedQuiet = 50 * time.Millisecond
	t.Cleanup(func() { unstreamedQuiet = was })
	lines := append(append(append([]string{}, cutAttempt...), cutAttempt...),
		`{"type":"assistant","message":{"id":"msg_whole","model":"claude-opus-5-5","content":[{"type":"text","text":"calling"}],"stop_reason":"tool_use","usage":{"output_tokens":777}}}`,
		`{"type":"assistant","message":{"id":"msg_whole","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_a","name":"mcp__magpie__Read","input":{"p":1}}],"stop_reason":"tool_use","usage":{"output_tokens":777}}}`,
		`{"type":"assistant","message":{"id":"msg_whole","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_b","name":"mcp__magpie__Read","input":{"p":2}}],"stop_reason":"tool_use","usage":{"output_tokens":777}}}`,
	)
	run := &subscriptionRun{}
	text, stop, _, _, raw, ended, _ := claudeReply(t, run, lines, true)
	if text != "calling[Read,Read]" || stop != "tool_use" || ended != "tool" {
		t.Fatalf("reply %q stop %q, run ended %q:\n%s", text, stop, ended, raw)
	}
	if !strings.Contains(raw, `"partial_json":"{\"p\":2}"`) {
		t.Fatalf("arguments lost:\n%s", raw)
	}
	if len(run.asked) != 2 {
		t.Fatalf("asked: %v", run.asked)
	}
}

// Cut short with nothing to go on in — Claude Code's output ends, or its
// turn ends without an answer — the client is told of an error, not an
// empty end_turn.
func TestClaudeCutStreamWithNoAnswer(t *testing.T) {
	for name, lines := range map[string][]string{
		"output ends": cutAttempt,
		"turn ends":   append(append([]string{}, cutAttempt...), `{"type":"result","subtype":"success","is_error":false,"result":""}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, stop, _, _, raw, _, ok := claudeReply(t, &subscriptionRun{}, lines, name == "turn ends")
			if stop == "end_turn" || ok || !strings.Contains(raw, "cut short") {
				t.Fatalf("stop %q, ok %v:\n%s", stop, ok, raw)
			}
		})
	}
}

// A stream cut short after words were shown can't go on in the next
// attempt, which writes its answer again: the reply fails instead, for the
// client to ask again.
func TestClaudeCutStreamAfterWords(t *testing.T) {
	lines := []string{
		cutStart,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half an ans"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
	}
	_, stop, _, _, raw, _, ok := claudeReply(t, &subscriptionRun{}, lines, true)
	if stop == "end_turn" || ok || !strings.Contains(raw, "cut short") {
		t.Fatalf("stop %q, ok %v:\n%s", stop, ok, raw)
	}
}

// A whole stream's output count is its message_delta's, which the client
// is told: no usage is lost on the way (#726's first question).
func TestClaudeStreamOutputCounted(t *testing.T) {
	lines := []string{
		cutStart,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hello"}}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12000}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"assistant","message":{"id":"msg_cut","model":"claude-opus-5-5","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"output_tokens":12000}}}`,
	}
	text, stop, out, write, raw, ended, ok := claudeReply(t, &subscriptionRun{}, lines, true)
	if text != "hello" || stop != "end_turn" || out != 12000 || write != 467708 || ended != "stop" || !ok {
		t.Fatalf("reply %q stop %q out %d write %d ended %q %v:\n%s", text, stop, out, write, ended, ok, raw)
	}
}

package gateway

import (
	"strings"
	"testing"
)

// A cited line, as a search's answer streams it (search_cite.go, and
// Anthropic's own): a text block whose citations_delta come before its
// text. Held while it may turn out to be a written call, the block's
// start still goes before its citations: a client that is told of a
// block's citations before the block itself (DeepSeek's chat page, the
// Anthropic SDKs) drops them or fails.
func TestTextCallsCitationAfterItsBlock(t *testing.T) {
	stream := `event: message_start
data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"x","content":[],"usage":{"input_tokens":1,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://go.dev/dl/","title":"Downloads","cited_text":"","encrypted_index":""}}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Go 1.27 is out ([go.dev](https://go.dev/dl/)).\n"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":9}}

event: message_stop
data: {"type":"message_stop"}

`
	tc := &anthropicTextCalls{names: map[string]bool{"read_url": true}, sse: true}
	var out []byte
	for chunk := range strings.SplitSeq(stream, "\n\n") {
		if chunk != "" {
			out = append(out, tc.write([]byte(chunk+"\n\n"))...)
		}
	}
	out = append(out, tc.flush()...)
	var order []string
	for _, ev := range events(string(out)) {
		typ, _ := ev["type"].(string)
		if d, ok := ev["delta"].(map[string]any); ok && typ == "content_block_delta" {
			typ, _ = d["type"].(string)
		}
		order = append(order, typ)
	}
	want := "message_start content_block_start citations_delta text_delta content_block_stop message_delta message_stop"
	if got := strings.Join(order, " "); got != want {
		t.Errorf("events %s, want %s\n%s", got, want, out)
	}
}

package gateway

import (
	"strconv"
	"strings"
	"testing"
)

// progressStart opens a thinking block the way a Claude subscription run
// does, and progressDelta is the vendor's live beat during a long think:
// a thinking_delta that says nothing but carries estimated_tokens (#5390).
var progressStart = []string{
	`{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_prog","model":"claude-opus-5-5","usage":{"input_tokens":2,"output_tokens":1}}}}`,
	`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}}`,
}

func progressDelta(estimated int) string {
	return `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"","estimated_tokens":` + strconv.Itoa(estimated) + `}}}`
}

// TestThinkingProgressTokensReachTheClient: an empty thinking_delta that
// carries estimated_tokens is the vendor's live progress beat; the client
// gets it as it came instead of hearing nothing during a long think.
func TestThinkingProgressTokensReachTheClient(t *testing.T) {
	run := &subscriptionRun{}
	lines := append([]string{}, progressStart...)
	lines = append(lines,
		progressDelta(50),
		progressDelta(120),
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"so far, so good. "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":777}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":""}`,
	)
	_, _, _, _, raw, _, ok := claudeReply(t, run, lines, true)
	if !ok {
		t.Fatalf("the run did not end cleanly")
	}
	for _, beat := range []string{`"estimated_tokens":50`, `"estimated_tokens":120`} {
		if !strings.Contains(raw, beat) {
			t.Errorf("the client never heard the progress beat %s; got:\n%s", beat, raw)
		}
	}
	for _, line := range strings.Split(raw, "\n") {
		if strings.Contains(line, "thinking_delta") && strings.Contains(line, `"thinking":""`) && !strings.Contains(line, "estimated_tokens") {
			t.Errorf("a progress beat went out without its count: %s", line)
		}
	}
	if !strings.Contains(raw, `"thinking":"so far, so good. "`) {
		t.Errorf("the real thinking text is missing; got:\n%s", raw)
	}
}

// TestBareEmptyThinkingDeltaStaysQuiet: an empty thinking_delta that
// carries no progress count stays skipped (#1445) — no empty deltas.
func TestBareEmptyThinkingDeltaStaysQuiet(t *testing.T) {
	run := &subscriptionRun{}
	lines := append([]string{}, progressStart...)
	lines = append(lines,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"words. "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":777}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"result","subtype":"success","is_error":false,"result":""}`,
	)
	_, _, _, _, raw, _, ok := claudeReply(t, run, lines, true)
	if !ok {
		t.Fatalf("the run did not end cleanly")
	}
	for _, line := range strings.Split(raw, "\n") {
		if !strings.Contains(line, "thinking_delta") {
			continue
		}
		if strings.Contains(line, `"thinking":""`) {
			t.Errorf("a bare empty thinking delta reached the client: %s", line)
		}
	}
}

// TestAnthropicDecoderKeepsProgressCount: the API leg's decoder carries
// the same beat's count, so a stream from Anthropic's own API keeps it on
// the way to the client too (#5390).
func TestAnthropicDecoderKeepsProgressCount(t *testing.T) {
	var got []Event
	data := `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"","estimated_tokens":50}}`
	if err := decodeAnthropic(data, func(ev Event) { got = append(got, ev) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != KThink || got[0].Estimated == nil || *got[0].Estimated != 50 {
		t.Fatalf("decoded %+v, want one KThink carrying estimated_tokens 50", got)
	}
}

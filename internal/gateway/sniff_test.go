package gateway

import (
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestSniffAnthropicStream(t *testing.T) {
	s := newSniffer(provider.Anthropic, "text/event-stream")
	s.write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"usage\":{\"input_tokens\":12,\"cache_read_input_tokens\":300,\"cache_creation_input_tokens\":40,\"output_tokens\":1}}}\n\n"))
	s.write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"text\":\"hi\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":57}}\n\n"))
	got := s.usage()
	want := Usage{Input: 12, Output: 57, CacheRead: 300, CacheWrite: 40}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestSniffChatStreamSplitAcrossWrites(t *testing.T) {
	s := newSniffer(provider.Chat, "text/event-stream; charset=utf-8")
	line := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"prompt_tokens_details\":{\"cached_tokens\":60},\"completion_tokens_details\":{\"reasoning_tokens\":5}}}\n\ndata: [DONE]\n\n"
	s.write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}],\"usage\":null}\n\n" + line[:30]))
	s.write([]byte(line[30:]))
	got := s.usage()
	want := Usage{Input: 40, Output: 20, CacheRead: 60, Reasoning: 5}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestSniffResponsesJSON(t *testing.T) {
	s := newSniffer(provider.Responses, "application/json")
	s.write([]byte(`{"id":"r","output":[],`))
	s.write([]byte(`"usage":{"input_tokens":50,"output_tokens":9,"input_tokens_details":{"cached_tokens":10}}}`))
	got := s.usage()
	want := Usage{Input: 40, Output: 9, CacheRead: 10}
	if got != want {
		t.Fatalf("got %+v want %+v", got, want)
	}
	s = newSniffer(provider.Responses, "text/event-stream")
	s.write([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n"))
	if got := s.usage(); got != (Usage{Input: 7, Output: 3}) {
		t.Fatalf("got %+v", got)
	}
}

// SSE permits data to be split over multiple data lines, and the final event
// need not end with a newline. Both shapes occur in proxy streams.
func TestSniffResponsesMultilineAndUnterminatedSSE(t *testing.T) {
	for _, payload := range []string{
		"event: response.completed\ndata: {\"response\":\ndata: {\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}\n\n",
		"event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":7,\"output_tokens\":3}}}",
	} {
		s := newSniffer(provider.Responses, "text/event-stream")
		for i := 0; i < len(payload); i += 9 {
			end := i + 9
			if end > len(payload) {
				end = len(payload)
			}
			s.write([]byte(payload[i:end]))
		}
		if got := s.usage(); got != (Usage{Input: 7, Output: 3}) {
			t.Errorf("usage %+v from %q", got, payload)
		}
	}
}

// Some relays separate complete events with a single newline, not a blank line.
// This case worked on main and must remain counted after multiline support.
func TestSniffSingleNewlineEvents(t *testing.T) {
	payload := "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":7}}}\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":3}}\n"
	s := newSniffer(provider.Anthropic, "text/event-stream")
	s.write([]byte(payload))
	if got := s.usage(); got.Input != 7 || got.Output != 3 {
		t.Errorf("usage %+v", got)
	}
}

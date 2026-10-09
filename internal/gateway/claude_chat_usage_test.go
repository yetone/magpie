package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestClaudeChatCacheWrites(t *testing.T) {
	// Claude reports the TTL split at message_start, but omits it in the
	// final delta. Both Chat response modes must retain the write count.
	lines := []string{
		`{"type":"stream_event","event":{"type":"message_start","message":{"id":"m1","model":"claude-haiku-4-5","usage":{"input_tokens":10,"cache_creation_input_tokens":6416,"cache_read_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":6416},"output_tokens":4}}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}}`,
		`{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"cache_creation_input_tokens":6416,"cache_read_input_tokens":0,"output_tokens":40,"output_tokens_details":{"thinking_tokens":33}}}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
	}
	for _, c := range []struct {
		name   string
		stream bool
	}{{"stream", true}, {"non-stream", false}} {
		t.Run(c.name, func(t *testing.T) {
			run := &subscriptionRun{}
			segment := run.attach()
			go run.readOutput(strings.NewReader(strings.Join(lines, "\n") + "\n"))
			rec := httptest.NewRecorder()
			req := &Request{Model: "claude-haiku-4-5", Stream: c.stream}
			var usage Usage
			code, msg := relay(rec, httptest.NewRequest("POST", "/v1/chat/completions", nil), provider.Chat, "Claude Code", req, segment, &usage,
				func() {}, nil, func(string, string, bool) {})
			if code != 200 || msg != "" {
				t.Fatalf("reply: %d %s", code, msg)
			}
			var reply struct {
				Usage *struct {
					Prompt     int `json:"prompt_tokens"`
					Completion int `json:"completion_tokens"`
					Total      int `json:"total_tokens"`
					Details    struct {
						Read  int  `json:"cached_tokens"`
						Write *int `json:"cache_write_tokens"`
					} `json:"prompt_tokens_details"`
				} `json:"usage"`
			}
			if c.stream {
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					data, ok := strings.CutPrefix(line, "data: ")
					if !ok || data == "[DONE]" {
						continue
					}
					if err := json.Unmarshal([]byte(data), &reply); err != nil {
						t.Fatal(err)
					}
				}
			} else if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			u := reply.Usage
			if u == nil || u.Details.Write == nil || *u.Details.Write != 6416 {
				t.Fatalf("cache writes missing or changed: %s", rec.Body.String())
			}
			if u.Prompt != 6426 || u.Completion != 40 || u.Total != 6466 || u.Details.Read != 0 {
				t.Fatalf("token counts: %+v", u)
			}
		})
	}
}

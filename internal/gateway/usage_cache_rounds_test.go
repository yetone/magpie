package gateway

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// anthCacheStart reports the writes billed for one Anthropic round.
func anthCacheStart(writes int, split bool) string {
	usage := fmt.Sprintf(`"cache_creation_input_tokens":%d`, writes)
	if split {
		usage += fmt.Sprintf(`,"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":0}`, writes)
	}
	return `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"m","content":[],"usage":{` + usage + `}}}`
}

func TestStreamCutZeroCacheSplitKeepsWrites(t *testing.T) {
	f := &cutAnthropic{
		cuts: map[int][]string{0: {anthCacheStart(30, false), anthText("hel")}},
		whole: []string{
			anthCacheStart(20, true), anthText("lo"),
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}}}`,
			`{"type":"message_stop"}`,
		},
	}
	body := continuationChatServer(t, "m", f)
	if f.calls() != 2 {
		t.Fatalf("calls = %d, want 2", f.calls())
	}
	if prefill, ok := f.lastAssistantText(t, 1); !ok || prefill != "hel" {
		t.Fatalf("second try did not continue the cut reply: %q, %v", prefill, ok)
	}
	if got := chatTextOf(body); got != "hello" {
		t.Fatalf("client's text = %q, want hello", got)
	}
	u := chatUsageOf(body)
	details, _ := u["prompt_tokens_details"].(map[string]any)
	// told the continuation's writes, billed both tries'
	if got := details["cache_write_tokens"]; got != 20.0 {
		t.Fatalf("zero delta after cut try reduced client cache writes: got %v, want the last try's 20; usage %v", got, u)
	}
	if r := usage.Load(time.Time{}); len(r) != 1 || r[0].CacheWrite != 50 {
		t.Fatalf("ledger %+v, want both tries' writes billed: 50", r)
	}
}

func TestClaudeInternalMessagesKeepCacheWrites(t *testing.T) {
	for _, tc := range []struct {
		name, before, start, delta string
		hour, told                 int
	}{
		{"zeroDelta", `"cache_creation_input_tokens":30`,
			`"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":0}`,
			`"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}`, 0, 20},
		{"pure5mAfterHour", `"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":20}`,
			`"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":0}`,
			`"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":30,"ephemeral_1h_input_tokens":0}`, 20, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := func(e string) string { return `{"type":"stream_event","event":` + e + `}` }
			lines := []string{
				ev(`{"type":"message_start","message":{"id":"m1","model":"x","usage":{` + tc.before + `}}}`),
				ev(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"WebSearch"}}`),
				ev(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`),
				ev(`{"type":"message_stop"}`),
				`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"results"}]}}`,
				ev(`{"type":"message_start","message":{"id":"m2","model":"x","usage":{` + tc.start + `}}}`),
				ev(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`),
				ev(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7,` + tc.delta + `}}`),
				ev(`{"type":"message_stop"}`),
			}
			run := &subscriptionRun{}
			seg := run.attach()
			done := make(chan struct{})
			go func() {
				defer close(done)
				run.readOutput(strings.NewReader(strings.Join(lines, "\n") + "\n"))
			}()
			var client Usage
			var starts, stops int
			var text string
			for e := range seg {
				switch e.Kind {
				case KStart:
					starts++
					client.add(e.Usage)
				case KUsage:
					client.add(e.Usage)
				case KStop:
					stops++
				case KText:
					text += e.Text
				}
			}
			<-done
			if starts != 1 || stops != 1 || text != "answer" {
				t.Fatalf("internal messages did not produce one reply: starts=%d stops=%d text=%q", starts, stops, text)
			}
			// told the answering message's writes, billed both messages'
			if client.CacheWrite != tc.told || client.CacheWrite1h != 0 || client.Output != 12 {
				t.Fatalf("internal-message usage = writes %d/%d, output %d; want the last message's %d/0, 12", client.CacheWrite, client.CacheWrite1h, client.Output, tc.told)
			}
			if b := client.billed(); b.CacheWrite != 50 || b.CacheWrite1h != tc.hour {
				t.Fatalf("billed writes %d/%d, want 50/%d", b.CacheWrite, b.CacheWrite1h, tc.hour)
			}
			wantUsageSplit(t, client, &aCacheCreation{Ephemeral5m: tc.told})
		})
	}
}

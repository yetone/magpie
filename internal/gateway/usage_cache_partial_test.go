package gateway

import (
	"strings"
	"testing"
)

func TestCachePartialUpdateMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		hour         int
	}{
		{"A_zeroFilled", `"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}`, 15},
		{"B_partial5m", `"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":0}`, 15},
		{"C_partialBoth", `"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":2}`, 12},
		{"D_partial1h", `"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":2}`, 12},
		{"E_pure5m", `"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":0}`, 15},
		{"F_pure1h", `"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":20}`, 30},
		{"G_unreported", `"cache_creation_input_tokens":20`, 15},
		{"H_zeroTotal", `"cache_creation_input_tokens":0`, 15},
		{"I_zeroTotalSplit", `"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0}`, 15},
		{"J_noCache", `"output_tokens":7`, 15},
		{"K_splitOnly", `"cache_creation":{"ephemeral_5m_input_tokens":15,"ephemeral_1h_input_tokens":5}`, 15},
		{"L_consistent", `"cache_creation_input_tokens":20,"cache_creation":{"ephemeral_5m_input_tokens":15,"ephemeral_1h_input_tokens":5}`, 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			delta := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{` + tc.fields + `}}`
			t.Run("bridge", func(t *testing.T) { checkCacheBridge(t, delta, tc.hour) })
			t.Run("continuation", func(t *testing.T) { checkCacheContinuation(t, delta, tc.hour) })
			t.Run("search", func(t *testing.T) { checkCacheSearch(t, delta, tc.hour) })
		})
	}
}

func checkCacheBridge(t *testing.T, delta string, hour int) {
	t.Helper()
	ev := func(e string) string { return `{"type":"stream_event","event":` + e + `}` }
	lines := []string{
		ev(cacheSplitRoundStart(zeroSplitBefore)),
		ev(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"WebSearch"}}`),
		ev(`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`),
		ev(`{"type":"message_stop"}`),
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"results"}]}}`,
		ev(cacheSplitRoundStart(zeroSplitStart)),
		ev(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}`),
		ev(delta),
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
	for e := range seg {
		if e.Kind == KStart || e.Kind == KUsage {
			client.add(e.Usage)
		}
	}
	<-done
	if client.CacheWrite != 20 || client.CacheWrite1h != hour-zeroSplitBefore1h {
		t.Errorf("client writes: %d/hour%d, want the last message's 20/hour%d", client.CacheWrite, client.CacheWrite1h, hour-zeroSplitBefore1h)
	}
	if b := client.billed(); b.CacheWrite != 50 || b.CacheWrite1h != hour {
		t.Errorf("billed writes: %d/hour%d, want50/hour%d", b.CacheWrite, b.CacheWrite1h, hour)
	}
}

func TestCacheZeroFilledPartialUpdateOmitsSplit(t *testing.T) {
	before := Usage{CacheWrite: 30, CacheWrite1h: 10}
	for _, tc := range []struct {
		name  string
		total int
	}{
		{"positiveTotal", 20},
		{"zeroTotal", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := aUsage{CacheCreationInputTokens: tc.total, CacheCreation: &aCacheCreation{}}.usage().plus(before, true)
			got := u.anthropic().CacheCreation
			if tc.total > 0 {
				if got != nil {
					t.Fatalf("zero-filled partial update rendered split %+v, want none", got)
				}
			} else if got == nil || *got != (aCacheCreation{}) {
				t.Fatalf("zero-total split = %+v, want reported zero split", got)
			}
		})
	}
}

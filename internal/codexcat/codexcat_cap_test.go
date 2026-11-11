package codexcat

import "testing"

// Cap holds one of Codex's own entries to a window, with the most it may be
// raised to and the threshold it compacts at, and touches nothing else.
func TestCapHoldsAnEntry(t *testing.T) {
	e := map[string]any{"slug": "gpt-5.5", "context_window": float64(272000), "max_context_window": float64(872000), "auto_compact_token_limit": float64(244800)}
	Cap(e, 200000)
	for _, k := range []string{"context_window", "max_context_window", "auto_compact_token_limit"} {
		if got, _ := e[k].(int); got != 200000 {
			t.Errorf("%s: %v, want 200000", k, e[k])
		}
	}
	short := map[string]any{"context_window": float64(128000)}
	Cap(short, 200000)
	if short["context_window"] != float64(128000) {
		t.Errorf("a shorter window changed: %v", short)
	}
	Cap(short, 0)
	if short["context_window"] != float64(128000) {
		t.Errorf("no cap changed it: %v", short)
	}
}

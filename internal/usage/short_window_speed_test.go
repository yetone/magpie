package usage

import "testing"

// pluviophile714 on Discord: a gpt-6.1-sol row on the Routing page read
// 1,539 tok/s, where other monitors read ~70: 392 output tokens, its first
// token at 8.2 s and its end at 8.3 s. The reply came whole in a moment after
// a long wait, spread over ~250 ms, so neither its flow nor the 100 ms floor
// told it from a stream: a window under HeldWindowMs after a wait HeldShare
// times longer tells no speed. A fast vendor's short reply after a short
// wait, and a long window after a long wait, keep theirs.
func TestShortWindowAfterLongWaitTellsNoSpeed(t *testing.T) {
	for _, c := range []struct {
		name string
		r    Record
		n    int
		w    int64
	}{
		// the report: 392 tokens 255 ms after an 8.2 s first token, the
		// content spread over most of the window (flow 230 ms)
		{"the report", Record{Output: 392, Millis: 8455, TTFT: 8200, FirstText: 8200, Flow: 230, Status: 200}, 0, 0},
		// a row of the owner's own ledger: gpt-6-sol, 42 reasoning tokens,
		// 376 answer tokens 315 ms after its first text at 10 s (1,194 tok/s)
		{"reasoned, answer after a long think", Record{Output: 418, Reasoning: 42, Millis: 10325, TTFT: 9949, FirstText: 10010, Flow: 172, ReasoningHidden: true, Status: 200}, 0, 0},
		// the flow unknown (a record from before it was kept)
		{"no flow kept", Record{Output: 392, Millis: 8455, TTFT: 8200, Status: 200}, 0, 0},
		// a fast vendor: 500 tokens in 250 ms after a 300 ms first token
		{"fast vendor, short wait", Record{Output: 500, Millis: 550, TTFT: 300, Flow: 250, Status: 200}, 500, 250},
		// at the boundary: a wait of four windows still tells, one more ms doesn't
		{"wait four windows", Record{Output: 200, Millis: 4995, TTFT: 3996, Flow: 999, Status: 200}, 200, 999},
		{"wait past four windows", Record{Output: 200, Millis: 4996, TTFT: 3997, Flow: 999, Status: 200}, 0, 0},
		// a second or more is timed however long the wait
		{"a second after a long wait", Record{Output: 100, Millis: 9200, TTFT: 8200, Flow: 1000, Status: 200}, 100, 1000},
	} {
		t.Run(c.name, func(t *testing.T) {
			if n, w := c.r.Decode(); n != c.n || w != c.w {
				t.Fatalf("speed counted %d tokens/%d ms; want %d/%d", n, w, c.n, c.w)
			}
		})
	}
	// the report's row adds its first token to a period's totals, not a speed
	var tot Totals
	tot.addRow(Row{Record: Record{Output: 392, Millis: 8455, TTFT: 8200, FirstText: 8200, Flow: 230, Status: 200}})
	if tot.Timed != 1 || tot.DecodeN != 0 || tot.Speed() != 0 {
		t.Fatalf("totals: timed %d, decode_n %d, speed %v", tot.Timed, tot.DecodeN, tot.Speed())
	}
}

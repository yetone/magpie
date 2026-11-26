package settings

import "testing"

// AtPercent rounds down, so an agent never compacts later than the percent
// picked, and leaves n as it is for none (0) or 100; CompactPercent reads
// only an agent's own, and a value out of 1–100 as none.
func TestCompactPercent(t *testing.T) {
	for _, c := range []struct{ n, p, want int }{
		{272000, 65, 176800}, {131072, 65, 85196}, {1000000, 0, 1000000},
		{1000000, 100, 1000000}, {0, 65, 0}, {1, 1, 1},
	} {
		if got := AtPercent(c.n, c.p); got != c.want {
			t.Errorf("AtPercent(%d, %d) = %d; want %d", c.n, c.p, got, c.want)
		}
	}
	s := Settings{AgentCompactPcts: map[string]int{"claude": 65, "codex": 150, "grok": -1}}
	for kind, want := range map[string]int{"claude": 65, "codex": 0, "grok": 0, "qwen": 0} {
		if got := s.CompactPercent(kind); got != want {
			t.Errorf("CompactPercent(%q) = %d; want %d", kind, got, want)
		}
	}
	if got := s.ForAgent("claude").CompactPercent("claude"); got != 65 {
		t.Errorf("ForAgent lost the percent: %d", got)
	}
}

package usage

import "testing"

// A hit rate never reads 100% unless every prompt token was cached, nor 0%
// when any was: rounding that would hide the difference shows a decimal or
// two instead (ZhangXiaof48473 on X: Codex requests read "缓存 100%").
func TestHitPctNeverRoundsToAllOrNone(t *testing.T) {
	for _, c := range []struct {
		h    float64
		want string
	}{
		{1, "100%"}, {0, "0%"}, {0.6, "60%"}, {0.994, "99%"},
		{77200.0 / 77500, "99.6%"}, {0.9995, "99.95%"}, {0.99999, "99.99%"},
		{0.003, "0.3%"}, {0.0004, "0.04%"}, {0.0000001, "0.01%"},
	} {
		if got := HitPct(c.h); got != c.want {
			t.Errorf("HitPct(%v) = %q, want %q", c.h, got, c.want)
		}
	}
}

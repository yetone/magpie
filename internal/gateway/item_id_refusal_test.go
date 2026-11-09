package gateway

import (
	"regexp"
	"strings"
	"testing"
)

// TestItemIDRefusalAsTheRegexp: the hand-made match finds what the
// regexp it replaced found, at the 1000-character edge too.
func TestItemIDRefusalAsTheRegexp(t *testing.T) {
	was := regexp.MustCompile(`(?s)Invalid 'input\[\d+\]\.id'.{0,1000}?Expected an ID that contains letters, numbers, underscores, or dashes`)
	const head, tail = "Invalid 'input[12].id'", "Expected an ID that contains letters, numbers, underscores, or dashes"
	in := []string{
		`{"error":{"message":"Invalid 'input[1].id': 'rs_6aca2fc7533c979d5f0e4f7a:rs_01a1'.\nExpected an ID that contains letters, numbers, underscores, or dashes, but this value contained additional characters."}}`,
		"", head, tail, tail + head, head + tail, "Invalid 'input[].id'" + tail,
		head + strings.Repeat("x", 2000) + head + "." + tail,
		head + "Expected an ID" + strings.Repeat(" ", 900) + tail,
	}
	for _, unit := range []string{"x", "é", "日", "\xff", "\n"} {
		for _, n := range []int{998, 999, 1000, 1001, 1002} {
			in = append(in, head+strings.Repeat(unit, n)+tail, head+strings.Repeat(unit, n)+tail[:20])
		}
	}
	for _, s := range in {
		if w, g := was.MatchString(s), itemIDRefusal([]byte(s)); w != g {
			t.Errorf("%d bytes: %v, want %v", len(s), g, w)
		}
	}
}

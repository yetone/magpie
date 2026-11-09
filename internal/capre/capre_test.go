package capre

import (
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// The bounded patterns magpie had, each beside what replaced it.
var pairs = []struct {
	bounded, open string
	max           int
}{
	{`^[A-Za-z0-9][A-Za-z0-9.-]{0,254}$`, `^[A-Za-z0-9][A-Za-z0-9.-]*$`, 255},
	{`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`, `^[A-Za-z0-9][A-Za-z0-9_-]*$`, 64},
	{`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`, `^[A-Za-z0-9][A-Za-z0-9._-]*$`, 64},
	{`^[a-zA-Z0-9_-]{1,128}$`, `^[a-zA-Z0-9_-]+$`, 128},
	{`^[A-Za-z0-9._@:/ -]{1,80}$`, `^[A-Za-z0-9._@:/ -]+$`, 80},
	{`^[^\x00-\x1f\x7f]{0,100}$`, `^[^\x00-\x1f\x7f]*$`, 100},
	{`(?i)^[a-z0-9][a-z0-9_-]{0,63}$`, `(?i)^[a-z0-9][a-z0-9_-]*$`, 64},
	{`^[0-9A-Za-z_.-]{1,64}$`, `^[0-9A-Za-z_.-]{1,}$`, 64},
}

func TestCappedMatchesBounded(t *testing.T) {
	var in []string
	for _, unit := range []string{"a", "Z", "9", "-", ".", "_", " ", "é", "K", "ſ", "\x00", "\xff", "@", "日"} {
		for _, n := range []int{0, 1, 2, 63, 64, 65, 79, 80, 81, 99, 100, 101, 127, 128, 129, 254, 255, 256, 300} {
			in = append(in, strings.Repeat(unit, n), "a"+strings.Repeat(unit, n), strings.Repeat(unit, n)+"a")
		}
	}
	for _, p := range pairs {
		want, got := regexp.MustCompile(p.bounded), MustCompile(p.open, p.max)
		for _, s := range in {
			if w, g := want.MatchString(s), got.MatchString(s); w != g {
				t.Errorf("%s on %d bytes of %q: %v, want %v", p.open, len(s), s[:min(len(s), 4)], g, w)
			}
		}
	}
}

// TestCappedIsSmall: the reason for the package. A program of the
// bounded class is tens of kB; held to a length it is a fraction of one.
func TestCappedIsSmall(t *testing.T) {
	heap := func(f func() any) int64 {
		var a, b runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&a)
		v := f()
		runtime.GC()
		runtime.ReadMemStats(&b)
		runtime.KeepAlive(v)
		return max(0, int64(b.HeapAlloc)-int64(a.HeapAlloc))
	}
	p := pairs[0]
	bounded := heap(func() any { return regexp.MustCompile(p.bounded) })
	capped := heap(func() any { return MustCompile(p.open, p.max) })
	if capped*10 > bounded {
		t.Fatalf("held to a length: %d bytes, bounded %d", capped, bounded)
	}
	t.Logf("held to a length: %d bytes, bounded %d", capped, bounded)
}

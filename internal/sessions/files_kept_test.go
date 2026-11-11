package sessions

import (
	"slices"
	"testing"
	"unsafe"
)

func sameArray(a, b []file) bool {
	return len(a) > 0 && len(b) > 0 && unsafe.SliceData(a) == unsafe.SliceData(b)
}

// A pass that lists the files the last one did gives its list back and
// allocates nothing for it; any change, a file gone included, is a new
// list, and each path is still listed once.
func TestFilesKeptWhileTheSame(t *testing.T) {
	var k filesKept
	a := []file{{agent: "claude", key: "claude:1", path: "/a"}, {agent: "claude", key: "claude:2", path: "/b"}}
	b := []file{{agent: "codex", key: "codex:3", path: "/c"}, {agent: "claude", key: "claude:1", path: "/a"}}
	first := k.distinct(a, b)
	if want := []file{a[0], a[1], b[0]}; !slices.Equal(first, want) {
		t.Fatalf("first = %v, want %v", first, want)
	}
	again := k.distinct(a, b)
	if !sameArray(first, again) {
		t.Fatal("the same files were listed in a new slice")
	}
	if n := testing.AllocsPerRun(20, func() { k.distinct(a, b) }); n != 0 {
		t.Fatalf("an unchanged pass allocated %v times", n)
	}

	grown := slices.Clone(b)
	grown[0].size = 10
	changed := k.distinct(a, grown)
	if sameArray(changed, first) || changed[2].size != 10 || !slices.Equal(first, []file{a[0], a[1], b[0]}) {
		t.Fatalf("a grown file: %v (last list now %v)", changed, first)
	}
	gone := k.distinct(a)
	if sameArray(gone, changed) || !slices.Equal(gone, a) {
		t.Fatalf("a file gone from the end: %v", gone)
	}
	// nil reads as "not listed": the parses of files gone would be kept
	if l := k.distinct(); l == nil || len(l) != 0 {
		t.Fatalf("an empty pass: %#v", l)
	}
	if l := k.distinct(); l == nil {
		t.Fatal("a second empty pass gave nil")
	}
	if len(k.distinct(a)) != 2 {
		t.Fatal("a pass after an empty one")
	}
	if l := k.distinct(a); cap(l) != len(l) {
		t.Fatalf("cap %d, len %d: an append would write into the kept list", cap(l), len(l))
	}
}

package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"unsafe"
)

// A listing the same as the last one is the last one, and allocates no
// list of its own: every 15 s Sessions poll made every rollout's and
// Claude Code file's list again, ~2.5 MB a poll of garbage on a real
// history while nothing had changed (#2278). A file grown, new or gone is
// in the next listing as a fresh one has it.
func TestUnchangedListingsAreTheLast(t *testing.T) {
	t.Cleanup(forgetListsKept)
	t.Cleanup(func() { ccListed.Clear(); rolloutsSeen.Clear() })
	sameArray := func(a, b []file) bool { return len(a) > 0 && unsafe.SliceData(a) == unsafe.SliceData(b) }

	t.Run("claude code", func(t *testing.T) {
		dir := t.TempDir()
		path := func(i int) string {
			return filepath.Join(dir, "projects", fmt.Sprintf("-Users-someone-p%d", i%3), fmt.Sprintf("%08d-1111-2222-3333-444444444444.jsonl", i))
		}
		const n = 600
		for i := range n {
			os.MkdirAll(filepath.Dir(path(i)), 0o755)
			if err := os.WriteFile(path(i), []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		first := listCCFiles("claude", dir)
		if len(first) != n {
			t.Fatalf("listed %d, want %d", len(first), n)
		}
		again := listCCFiles("claude", dir)
		if !sameArray(again, first) {
			t.Fatal("an unchanged listing made a list of its own")
		}
		if err := os.WriteFile(path(7), []byte("{}\n{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		grown := listCCFiles("claude", dir)
		if sameArray(grown, first) {
			t.Fatal("a grown file's listing is the last one")
		}
		if i := slices.IndexFunc(grown, func(f file) bool { return f.path == path(7) }); i < 0 || grown[i].size != 6 {
			t.Fatal("a grown file isn't listed at its size")
		}
		if first[slices.IndexFunc(first, func(f file) bool { return f.path == path(7) })].size != 3 {
			t.Fatal("the last listing was written over")
		}
		os.Remove(path(8))
		if gone := listCCFiles("claude", dir); len(gone) != n-1 || slices.ContainsFunc(gone, func(f file) bool { return f.path == path(8) }) {
			t.Fatalf("a gone file: %d listed", len(gone))
		}
	})

	t.Run("codex", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("listed in one call a folder on macOS only")
		}
		root := filepath.Join(t.TempDir(), "sessions")
		day := filepath.Join(root, "2026", "11", "12")
		os.MkdirAll(day, 0o700)
		name := func(i int) string {
			return filepath.Join(day, fmt.Sprintf("rollout-2026-11-12T10-00-%02d-0190aaaa-1111-7222-8333-4444555566%02d.jsonl", i, i))
		}
		for i := range 40 {
			if err := os.WriteFile(name(i), []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		first, ok := listedRollouts(root)
		if !ok || len(first) != 40 {
			t.Fatalf("listed %d, ok %t", len(first), ok)
		}
		again, _ := listedRollouts(root)
		if !sameArray(again, first) {
			t.Fatal("an unchanged listing made a list of its own")
		}
		// a day folder's rollouts are the list's own, not a copy
		seen, _ := rolloutsSeen.Load(day)
		if l, _ := seen.([]file); !sameArray(l, first) || cap(l) != len(l) {
			t.Fatal("a day folder's rollouts are a copy, or open to an append")
		}
		os.WriteFile(name(3), []byte("{}\n{}\n"), 0o600)
		os.WriteFile(name(50), []byte("{}\n"), 0o600)
		changed, _ := listedRollouts(root)
		if sameArray(changed, first) || len(changed) != 41 || changed[3].size != 6 || changed[40].path != name(50) {
			t.Fatalf("a grown and a new rollout: %d listed", len(changed))
		}
		if first[3].size != 3 {
			t.Fatal("the last listing was written over")
		}
	})
}

// The lists a lister keeps are read by every pass after, so what is
// made from them never writes into them.
func TestListsKeptAreNotWritten(t *testing.T) {
	t.Cleanup(forgetListsKept)
	a := []file{{path: "/a"}, {path: "/b"}}
	b := []file{{path: "/b"}, {path: "/c"}}
	one := concatKept("test", a, nil)
	if unsafe.SliceData(one) != unsafe.SliceData(a) {
		t.Error("one list among empty ones was copied")
	}
	both := concatKept("test", a, b)
	if again := concatKept("test", a, b); unsafe.SliceData(again) != unsafe.SliceData(both) || len(again) != 4 {
		t.Error("the same lists one after another were made again")
	}
	was := slices.Clone(both)
	d := distinctPaths(both)
	if len(d) != 3 || !slices.Equal(both, was) {
		t.Fatalf("distinctPaths gave %d files, and its list is %v, was %v", len(d), both, was)
	}
	if d := distinctPaths(a); unsafe.SliceData(d) != unsafe.SliceData(a) {
		t.Error("a list with every path once was copied")
	}
	if l := concatKept("test", a, b[:1]); len(l) != 3 || l[2].path != "/b" {
		t.Errorf("a list gone short: %v", l)
	}
}

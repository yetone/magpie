package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// packFixture writes n Claude Code sessions, the first `working` of them
// changed now (their revision windows kept) and the rest two days ago.
func packFixture(t *testing.T, claude string, n, working int) []string {
	t.Helper()
	var paths []string
	for s := 0; s < n; s++ {
		path := filepath.Join(claude, "projects", "-work-pack", fmt.Sprintf("44444444-2222-3333-4444-%012d.jsonl", s))
		var lines []string
		for i := 0; i < 6; i++ {
			lines = append(lines, strings.TrimSuffix(ccMsg(fmt.Sprintf("msg_%d_%d", s, i), fmt.Sprintf("2026-09-21T10:%02d:00Z", i), 100+i, 20+i), "\n"))
		}
		writeLines(t, path, lines...)
		if s >= working {
			old := time.Now().Add(-48 * time.Hour)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
		}
		paths = append(paths, path)
	}
	return paths
}

// packDirFiles counts the files in the calls cache, by kind.
func packDirFiles(t *testing.T) map[string]int {
	t.Helper()
	out := map[string]int{}
	entries, _ := os.ReadDir(callCacheDir())
	for _, e := range entries {
		out[filepath.Ext(e.Name())]++
	}
	return out
}

// TestColdSessionsReadWritesOnePack: a cold Sessions read of a long
// Claude Code history made a shard and a revisions file for each session,
// each made, closed and renamed: ~21,000 files for 12,000 sessions, and on
// Windows Defender scanned each one as it closed, 15 s of the page's 19 s.
// The idle sessions' calls read along go to one pack a pass; one being
// written keeps its shard, which its new lines are appended to.
func TestColdSessionsReadWritesOnePack(t *testing.T) {
	claude, codex := setup(t)
	os.RemoveAll(codex) // Codex's calls are kept as before
	paths := packFixture(t, claude, 60, 5)
	Reset()
	List(0)
	Saved()
	got := packDirFiles(t)
	if got[".pack"] != 1 || got[".gob"] > 10 {
		t.Fatalf("a cold read of 55 idle sessions and 5 being written wrote %v to the calls cache, want one .pack and shards for the 5 (and testdata's 3)", got)
	}
	for i, p := range paths {
		_, err := os.Stat(callCachePath(p))
		if working := i < 5; working != (err == nil) {
			t.Fatalf("session %d (being written %v) has a shard of its own: %v", i, working, err == nil)
		}
		if _, err := os.Stat(callRevisionsPath(p)); err == nil && i >= 5 {
			t.Fatalf("idle session %d has a revisions file", i)
		}
	}
}

// TestPackedCallsServeRequests: the calls a Sessions read packed are what
// Requests reads, after a restart too, without reading a session again;
// a pack cut off is as if it were not there, and its sessions are read
// again from their files.
func TestPackedCallsServeRequests(t *testing.T) {
	claude, _ := setup(t)
	paths := packFixture(t, claude, 20, 3)
	readAll := func() map[string][]Call {
		out := map[string][]Call{}
		for _, s := range CallSources() {
			if s.Agent == "claude" && strings.Contains(s.Path, "-work-pack") {
				out[s.Path] = ReadCallSource(s)
			}
		}
		return out
	}
	var mu sync.Mutex
	scanned := map[string]int{}
	scanHook = func(p string, _ int64) {
		if strings.Contains(p, "-work-pack") {
			mu.Lock()
			scanned[p]++
			mu.Unlock()
		}
	}
	t.Cleanup(func() { scanHook = nil })
	scans := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, c := range scanned {
			n += c
		}
		scanned = map[string]int{}
		return n
	}

	// the reference: calls read on their own, no cache
	Reset()
	want := readAll()
	if len(want) != len(paths) {
		t.Fatalf("%d sources read, want %d", len(want), len(paths))
	}
	for p, cs := range want {
		if len(cs) != 6 {
			t.Fatalf("%s: %d calls, want 6", p, len(cs))
		}
	}
	Reset()
	os.Remove(CachePath())
	os.RemoveAll(callCacheDir())

	List(0) // packs the calls it reads along
	Saved()
	scans()
	if got := readAll(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls from the live pack differ from a read of their own")
	}
	if n := scans(); n != 0 {
		t.Fatalf("the packed calls were read again: %d scans", n)
	}
	Reset() // a restart: the pack is read from disk
	if got := readAll(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls from the pack on disk differ from a read of their own")
	}
	if n := scans(); n != 0 {
		t.Fatalf("the packed calls were read again after a restart: %d scans", n)
	}

	// cut off: read again, and right
	Reset()
	os.Remove(CachePath())
	for p := range want {
		os.Remove(callCachePath(p))
		os.Remove(callRevisionsPath(p))
	}
	packs, _ := filepath.Glob(filepath.Join(callCacheDir(), "*.pack"))
	if len(packs) != 1 {
		t.Fatalf("%d packs, want 1", len(packs))
	}
	b, err := os.ReadFile(packs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packs[0], b[:len(b)/2], 0600); err != nil {
		t.Fatal(err)
	}
	Reset()
	if got := readAll(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls with their pack cut off differ from a read of their own")
	}
	if n := scans(); n < len(paths) {
		t.Fatalf("with the pack cut off only %d of %d sessions were read again", n, len(paths))
	}
}

// TestPackedSessionGrows: a packed session that grows is read on, and its
// calls written whole to a shard of their own, which is read before the
// pack from then on.
func TestPackedSessionGrows(t *testing.T) {
	claude, _ := setup(t)
	paths := packFixture(t, claude, 4, 0)
	Reset()
	List(0)
	Saved()
	path := paths[0]
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(ccMsg("msg_grown", "2026-09-21T11:00:00Z", 500, 50))
	f.Close()
	var calls []Call
	for _, s := range CallSources() {
		if s.Path == path {
			calls = ReadCallSource(s)
		}
	}
	if len(calls) != 7 {
		t.Fatalf("%d calls after the session grew, want 7", len(calls))
	}
	if _, err := os.Stat(callCachePath(path)); err != nil {
		t.Fatalf("the grown session's calls have no shard of their own: %v", err)
	}
	Reset()
	for _, s := range CallSources() {
		if s.Path == path {
			calls = ReadCallSource(s)
		}
	}
	if len(calls) != 7 || calls[6].Msg != "msg_grown" {
		t.Fatalf("after a restart: %d calls, want 7 ending in msg_grown", len(calls))
	}
}

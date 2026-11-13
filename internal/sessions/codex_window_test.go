package sessions

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestCodexSessionReadOnAfterRestart: a live Codex rollout that grew after
// magpie started again is read on from where the last read ended, by the
// Sessions list and the Requests reader alike, and counts what a read from
// its first line counts. Its window lived in memory alone, so the first
// line after a restart read the whole rollout again for each: a 100 MB
// one took over ten seconds of the Sessions page on Windows
// (shangyuan0818 on X).
func TestCodexSessionReadOnAfterRestart(t *testing.T) {
	d := setupCalls(t)
	path := filepath.Join(d.codex, "sessions", "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl")
	const turns = 60
	lines := longRollout(0, turns)
	writeLines(t, path, lines...)
	var mu sync.Mutex
	var starts []int64
	scanHook = func(p string, off int64) {
		if p == path {
			mu.Lock()
			starts = append(starts, off)
			mu.Unlock()
		}
	}
	t.Cleanup(func() { scanHook = nil })
	read := func() ([]Call, []Session) {
		t.Helper()
		var calls []Call
		for _, s := range CallSources() {
			if s.Path == path {
				calls = ReadCallSource(s)
			}
		}
		ss := List(0)
		if len(ss) != 1 {
			t.Fatalf("%d sessions", len(ss))
		}
		return calls, ss
	}
	if cs, _ := read(); len(cs) != turns {
		t.Fatalf("%d calls", len(cs))
	}
	Reset() // a restart: what was read is on disk alone
	// the last turn's usage written again, as a resumed rollout replays
	// it, and a new turn: a window that lost what it had seen counts the
	// replay a second time
	var replay string
	for _, l := range lines {
		if strings.Contains(l, `"token_count"`) {
			replay = l
		}
	}
	appendText(t, path, replay+"\n"+strings.Join(longRollout(turns, turns+1), "\n")+"\n")
	mu.Lock()
	starts = nil
	mu.Unlock()
	got, gotList := read()
	mu.Lock()
	if len(starts) < 2 {
		t.Fatalf("the summary and the calls weren't both read: %v", starts)
	}
	for _, off := range starts {
		if off == 0 {
			t.Fatalf("the rollout was read from its first line after a restart: %v", starts)
		}
	}
	mu.Unlock()
	Reset()
	for _, p := range []string{CachePath(), callCachePath(path), callRevisionsPath(path), codexWindowPath(path)} {
		os.Remove(p)
	}
	want, wantList := read()
	if len(got) != turns+1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("read on: %d calls, from the start: %d", len(got), len(want))
	}
	if g, w := gotList[0], wantList[0]; !reflect.DeepEqual(g.Tokens, w.Tokens) || !reflect.DeepEqual(g.Models, w.Models) || g.Cost != w.Cost || g.Tokens == (Tokens{}) {
		t.Fatalf("read on: %+v\nfrom the start: %+v", g, w)
	}
}

// A rollout no longer being written keeps no window on disk: what is kept
// is the windows the cache keeps (trimSummaryRevisions).
func TestCodexWindowLeavesWithItsSession(t *testing.T) {
	setupCalls(t)
	s := &state{Agent: "codex", Size: 10, Off: 10, ContentHash: digest{1}, Codex: &codexUsageState{Session: "s"}}
	writeCodexWindows(map[string]*state{"/a.jsonl": s})
	if _, err := os.Stat(codexWindowPath("/a.jsonl")); err != nil {
		t.Fatal(err)
	}
	if got := codexWindowed(file{agent: "codex", path: "/a.jsonl"}, s.withoutRevisions()); got.Codex == nil || got.Codex.Session != "s" {
		t.Fatalf("the window kept wasn't read back: %+v", got.Codex)
	}
	other := *s
	other.Off = 9
	if got := codexWindowed(file{agent: "codex", path: "/a.jsonl"}, other.withoutRevisions()); got.Codex != nil {
		t.Fatal("a window of another read was taken")
	}
	writeCodexWindows(map[string]*state{"/a.jsonl": s.withoutRevisions()})
	if _, err := os.Stat(codexWindowPath("/a.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("a window without its session stayed: %v", err)
	}
}

package sessions

import (
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// TestCallParseVersionRaiseReadsThatAgentAlone: raising callCacheVersion
// for a change to one agent's calls read every agent's history again after
// the update. Raising that agent's callParseVersions reads its files alone,
// once, and keeps every other agent's shards.
func TestCallParseVersionRaiseReadsThatAgentAlone(t *testing.T) {
	d := setupCalls(t)
	// from a magpie whose agents' calls are all at 0, whatever they are now
	before := callParseVersions
	callParseVersions = map[string]int{}
	t.Cleanup(func() { callParseVersions = before })
	cc := filepath.Join(d.claude, "projects", "-work-app", "sess1.jsonl")
	writeLines(t, cc, claudeMsg("m1", "claude-opus-5-5", 100, 20, 5000, 1000, 1), claudeMsg("m2", "claude-opus-5-5", 7, 8, 0, 0, 2))
	cx := filepath.Join(d.codex, "sessions", "2026", "09", "20", "rollout-2026-09-20T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl")
	writeLines(t, cx, append(cxTurnLines(0, "t", "gpt-6-astra", "high"), tokenCountLine(1, cxUse(100, 0, 0, 1, 0), cxUse(100, 0, 0, 1, 0)))...)

	var mu sync.Mutex
	scans := map[string]int{}
	scanHook = func(p string, off int64) {
		mu.Lock()
		scans[p]++
		mu.Unlock()
	}
	t.Cleanup(func() { scanHook = nil })
	counted := func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		out := scans
		scans = map[string]int{}
		return out
	}
	read := func() map[string][]Call {
		out := map[string][]Call{}
		for _, s := range CallSources() {
			out[s.Path] = ReadCallSource(s)
		}
		return out
	}

	want := read()
	if len(want[cc]) != 2 || len(want[cx]) != 1 {
		t.Fatalf("calls read: %d Claude, %d Codex", len(want[cc]), len(want[cx]))
	}
	if n := counted(); n[cc] != 1 || n[cx] != 1 {
		t.Fatalf("first read scanned %v", n)
	}
	Reset()
	read()
	if n := counted(); len(n) != 0 {
		t.Fatalf("a start with every shard current scanned %v", n)
	}

	unraised := CallCacheVersion()
	kept := callParseVersions
	callParseVersions = map[string]int{"claude": 1}
	t.Cleanup(func() { callParseVersions = kept })
	if CallCacheVersion() == unraised || !strings.Contains(CallCacheVersion(), "claude=1") {
		t.Fatalf("raised Claude's calls, and what is made of them is still %q", CallCacheVersion())
	}
	Reset()
	got := read()
	if n := counted(); n[cc] != 1 || n[cx] != 0 {
		t.Fatalf("Claude's calls raised: scanned %v, want Claude's file alone", n)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("calls after the raise differ")
	}
	Reset()
	read()
	if n := counted(); len(n) != 0 {
		t.Fatalf("the start after the raise scanned %v", n)
	}
}

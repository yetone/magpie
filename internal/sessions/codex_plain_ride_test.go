package sessions

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestPlainRolloutReadOnceForSessionsAndCalls: a plain rollout read from
// its first line, at a start with nothing kept, has its calls read on the
// summary's own pass, as a packed one has. Read apart, a cold start
// read every rollout twice: 3.5 GB of reads for 1.7 GB of real rollouts
// (#4369). A rollout read on from where it was, in the same run or after a
// restart (its windows are kept on disk, codex_window.go), is read on by
// each as before.
func TestPlainRolloutReadOnceForSessionsAndCalls(t *testing.T) {
	_, codex := setup(t)
	long := filepath.Join(codex, "sessions", "2026", "09", "21", "rollout-2026-09-21T10-00-00-0190aaaa-1111-7222-8333-444455556666.jsonl")
	writeLines(t, long, longRollout(0, 40)...)
	forget := func() {
		// Reset first: it writes a save still pending, which would put
		// back a cache file removed before it
		Reset()
		os.Remove(CachePath())
		os.RemoveAll(callCacheDir())
	}

	var mu sync.Mutex
	scans := map[string][]int64{}
	scanHook = func(p string, off int64) {
		if strings.HasSuffix(p, ".jsonl") && strings.Contains(p, "rollout-") {
			mu.Lock()
			scans[p] = append(scans[p], off)
			mu.Unlock()
		}
	}
	t.Cleanup(func() { scanHook = nil })
	counted := func() map[string][]int64 {
		mu.Lock()
		defer mu.Unlock()
		out := scans
		scans = map[string][]int64{}
		return out
	}
	readCallsAll := func() map[string][]Call {
		out := map[string][]Call{}
		for _, s := range CallSources() {
			if s.Agent == "codex" {
				out[s.Path] = ReadCallSource(s)
			}
		}
		return out
	}
	day := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	// reference: calls read first, then the summaries, each on its own scan
	reference := func() (map[string][]Call, []Session, Stats) {
		forget()
		calls := readCallsAll()
		list, stats := List(0), StatsAt(0, day)
		counted()
		return calls, list, stats
	}
	type read struct {
		calls map[string][]Call
		list  []Session
		stats Stats
		scans map[string][]int64
	}
	// the warm-up's order: sessions, then calls
	run := func() read {
		list, stats := List(0), StatsAt(0, day)
		calls := readCallsAll()
		return read{calls, list, stats, counted()}
	}
	same := func(name string, got read) {
		t.Helper()
		calls, list, stats := reference()
		if !reflect.DeepEqual(got.calls, calls) {
			t.Errorf("%s: calls differ from their own read's", name)
		}
		if !reflect.DeepEqual(got.list, list) || !reflect.DeepEqual(got.stats, stats) {
			t.Errorf("%s: sessions differ from a read without the calls", name)
		}
	}
	grow := func(from, to int) {
		f, err := os.OpenFile(long, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range longRollout(from, to) {
			f.WriteString(l + "\n")
		}
		f.Close()
	}

	calls, _, _ := reference()
	n := 0
	for _, cs := range calls {
		n += len(cs)
	}
	if len(calls) != 3 || n < 40 {
		t.Fatalf("%d rollouts, %d calls: the fixture tests nothing", len(calls), n)
	}

	forget()
	counted()
	got := run()
	if len(got.scans) != 3 {
		t.Errorf("first read: %d rollouts scanned, want 3: %v", len(got.scans), got.scans)
	}
	for p, offs := range got.scans {
		if len(offs) != 1 || offs[0] != 0 {
			t.Errorf("first read: %s scanned from %v, want once from 0", filepath.Base(p), offs)
		}
	}
	same("first read", got)

	// the next line, in the same run as a first read: each reader reads on
	// from where it was (the calls' window was dropped as a moved root's)
	forget()
	run()
	grow(40, 42)
	got = run()
	if len(got.scans) != 1 || len(got.scans[long]) == 0 {
		t.Errorf("an append: %v scanned, want the long rollout only", got.scans)
	}
	for _, off := range got.scans[long] {
		if off == 0 {
			t.Errorf("an append: the long rollout read again from its first line: %v", got.scans[long])
		}
	}
	same("an append", got)

	// grown while magpie was closed: both windows are kept on disk, so
	// each read goes on from where it was
	forget()
	run()
	grow(42, 43)
	Reset()
	counted()
	got = run()
	if len(got.scans) != 1 || len(got.scans[long]) == 0 {
		t.Errorf("grown after a restart: %v scanned, want the long rollout only", got.scans)
	}
	for _, off := range got.scans[long] {
		if off == 0 {
			t.Errorf("grown after a restart: the long rollout read again from its first line: %v", got.scans[long])
		}
	}
	same("grown after a restart", got)
}

package sessions

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"
	"time"
)

// A write of a file's calls makes no list of patches of its own for each
// frame: each frame grew one to framePatches, ~1 GB of a first start's
// read of a 21 GB history (#2278). What it writes is still read back
// whole after a write of another file.
func TestCallsWriteKeepsNoPatchListPerFrame(t *testing.T) {
	d := setupCalls(t)
	mk := func(name string, n int) *callFile {
		st := &callFile{Path: filepath.Join(d.claude, "projects", "p", name), Agent: "claude", Began: map[string]time.Time{}}
		for i := range n {
			msg := fmt.Sprint(name, i)
			st.Calls = append(st.Calls, Call{Time: time.Unix(int64(i), 0), Agent: "claude", Session: "s", Model: "m", Msg: msg})
		}
		return st
	}
	a, b := mk("a.jsonl", 3*framePatches), mk("b.jsonl", 10)
	// no collection while it counts: one empties the pool, as it does
	// for a moment on a running magpie
	runtime.GC()
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	writeCalls(a, 0, false)
	var ms runtime.MemStats
	least := uint64(1 << 62)
	for range 3 {
		runtime.ReadMemStats(&ms)
		before := ms.TotalAlloc
		writeCalls(a, 0, false)
		runtime.ReadMemStats(&ms)
		least = min(least, ms.TotalAlloc-before)
	}
	writeCalls(b, 0, false)
	for _, st := range []*callFile{a, b} {
		got := loadKept(file{path: st.Path, agent: "claude"})
		if got == nil || len(got.Calls) != len(st.Calls) || got.Calls[len(st.Calls)-1].Msg != st.Calls[len(st.Calls)-1].Msg {
			t.Fatalf("%s not read back", st.Path)
		}
	}
	// ~2.7 MB: the gob and the file's bytes; a list per frame made ~13 MB
	if least > 6<<20 {
		t.Fatalf("a write of %d calls made %d KB, want at most 6 MB", len(a.Calls), least>>10)
	}
}

// A write just after a collection, the pools emptied as a first start's
// writes find them, makes its patches' list once and holds one frame's
// bytes, not the whole write's: append's doublings of both were ~270 MB
// of a first start's ~550 MB of writes. What it writes reads back whole.
func TestCallsWriteAfterACollection(t *testing.T) {
	d := setupCalls(t)
	st := &callFile{Path: filepath.Join(d.claude, "projects", "p", "a.jsonl"), Agent: "claude", Began: map[string]time.Time{}}
	for i := range 5 * framePatches {
		st.Calls = append(st.Calls, Call{Time: time.Unix(int64(i), 0), Agent: "claude", Session: "s", Model: "m", Msg: fmt.Sprint("msg_", i), RequestID: fmt.Sprint("req_", i), Tokens: Tokens{Output: i}})
	}
	writeCalls(st, 0, false)
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	var ms runtime.MemStats
	least := uint64(1 << 62)
	for range 3 {
		runtime.GC()
		runtime.GC() // the pools' victims too
		runtime.ReadMemStats(&ms)
		before := ms.TotalAlloc
		writeCalls(st, 0, false)
		runtime.ReadMemStats(&ms)
		least = min(least, ms.TotalAlloc-before)
	}
	info, err := os.Stat(callCachePath(st.Path))
	if err != nil {
		t.Fatal(err)
	}
	got := loadKept(file{path: st.Path, agent: "claude"})
	if got == nil || len(got.Calls) != len(st.Calls) {
		t.Fatal("not read back")
	}
	for i, c := range got.Calls {
		if c.Msg != st.Calls[i].Msg || c.RequestID != st.Calls[i].RequestID || c.Output != i {
			t.Fatalf("call %d read back as %+v", i, c)
		}
	}
	// ~4.9 MB: the list once (~0.9 MB) and each frame's gob encoder's own
	// buffer; ~7.4 MB with the list grown, ~9.3 MB with the write held too
	if least > 6<<20 {
		t.Fatalf("a write of %d calls (%d KB on disk) made %d KB, want at most 6 MB", len(st.Calls), info.Size()>>10, least>>10)
	}
}

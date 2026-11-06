package sessions

import (
	"fmt"
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

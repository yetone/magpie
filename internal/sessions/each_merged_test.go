package sessions

import (
	"fmt"
	"reflect"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

// TestEachMergedListsWhatMergeExternalDid: the Sessions page makes its rows
// as the list and the gateway's sessions merge, in MergeExternal's order:
// newest first, a native before an external of the same moment, and a list
// not newest first sorted before. Its external are the gateway's sessions
// native doesn't hold (GatewaySessions), so a native's id isn't among them.
func TestEachMergedListsWhatMergeExternalDid(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	native := []Session{
		{Agent: "claude", ID: "c1", Last: at.Add(5 * time.Minute), Copies: []string{"c1-copy"}},
		{Agent: "codex", ID: "x1", Last: at.Add(3 * time.Minute)},
		{Agent: "claude", ID: "c2", Last: at.Add(3 * time.Minute)},
		{Agent: "pi", ID: "p1", Last: at},
	}
	external := []ExternalSession{
		{Agent: "opencode", ID: "g-old", Last: at.Add(-time.Hour)},
		{Agent: "opencode", ID: "g-tie", Last: at.Add(3 * time.Minute)},
		{Agent: "opencode", ID: "g-new", Last: at.Add(time.Hour)},
		{Agent: "", ID: "nobody", Last: at},
		{Agent: "opencode", ID: "g-tie2", Last: at.Add(3 * time.Minute)},
	}
	unsorted := []Session{native[2], native[0], native[3], native[1]}
	for _, in := range [][]Session{native, unsorted, nil} {
		for _, ext := range [][]ExternalSession{external, nil} {
			want := MergeExternal(in, ext)
			var got []Session
			EachMerged(in, ext, func(s *Session) bool { got = append(got, *s); return true })
			if !reflect.DeepEqual(mergedIDs(got), mergedIDs(want)) || len(got) != len(want) {
				t.Fatalf("merged\n got %v\nwant %v", mergedIDs(got), mergedIDs(want))
			}
			for i := range want {
				if !reflect.DeepEqual(got[i], want[i]) {
					t.Fatalf("row %d\n got %+v\nwant %+v", i, got[i], want[i])
				}
			}
			if len(want) > 2 {
				n := 0
				EachMerged(in, ext, func(*Session) bool { n++; return n < 2 })
				if n != 2 {
					t.Fatalf("went on after false: %d calls", n)
				}
			}
		}
	}
	if fmt.Sprint(mergedIDs(unsorted)) != "[claude:c2 claude:c1 pi:p1 codex:x1]" {
		t.Fatalf("the caller's list was sorted in place: %v", mergedIDs(unsorted))
	}
}

// TestEachMergedCopiesNoList: a poll's merge was a copy of every session.
func TestEachMergedCopiesNoList(t *testing.T) {
	native := make([]Session, 5000)
	at := time.Now()
	for i := range native {
		native[i] = Session{Agent: "claude", ID: fmt.Sprint("s", i), Last: at.Add(-time.Duration(i) * time.Second)}
	}
	ext := []ExternalSession{{Agent: "opencode", ID: "g", Last: at.Add(-time.Hour)}}
	list := uint64(len(native)) * uint64(unsafe.Sizeof(Session{}))
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	n := 0
	EachMerged(native, ext, func(*Session) bool { n++; return true })
	runtime.ReadMemStats(&after)
	if n != len(native)+1 {
		t.Fatalf("%d sessions, want %d", n, len(native)+1)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > list/20 {
		t.Fatalf("merging a %d-byte list allocated %d bytes", list, got)
	}
}

func mergedIDs(list []Session) []string {
	var out []string
	for _, s := range list {
		out = append(out, s.Agent+":"+s.ID)
	}
	return out
}

// TestEachMergedMakesOnlyTheRowsItReaches: every gateway session was made
// into a row before the merge, ~22 MB of each poll over ~15k sessions
// while one of them grew, though the page stops at sessions.All rows.
func TestEachMergedMakesOnlyTheRowsItReaches(t *testing.T) {
	at := time.Now()
	ext := make([]ExternalSession, 15000)
	for i := range ext {
		// oldest first, so the order is the merge's to make
		ext[i] = ExternalSession{Agent: "opencode", ID: fmt.Sprint("g", i), Last: at.Add(time.Duration(i-len(ext)) * time.Second),
			Models: []ExternalModel{{Model: "m"}}}
	}
	native := []Session{{Agent: "claude", ID: "c", Last: at.Add(-10 * time.Second)}}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var got []Session
	EachMerged(native, ext, func(s *Session) bool { got = append(got, *s); return len(got) < 20 })
	runtime.ReadMemStats(&after)
	if want := MergeExternal(native, ext)[:20]; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows\n got %v\nwant %v", mergedIDs(got), mergedIDs(want))
	}
	rows := uint64(len(ext)) * uint64(unsafe.Sizeof(Session{}))
	if a := after.TotalAlloc - before.TotalAlloc; a > rows/10 {
		t.Fatalf("20 rows of %d gateway sessions allocated %d bytes; every row is %d", len(ext), a, rows)
	}
}

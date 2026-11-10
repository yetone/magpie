package dirent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// Stats says of each entry what os.Lstat says: a file's size and time to
// the nanosecond, a folder, a link (to a file, a folder, or nothing) as a
// link, in a folder of a few entries and in one too long for one call.
func TestStatsAsLstat(t *testing.T) {
	dir, elsewhere := t.TempDir(), t.TempDir()
	write := func(name string, n int, at time.Time) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Repeat("x", n)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	write("a.jsonl", 0, time.Unix(1_700_000_000, 123_456_789))
	write("日本語 session.jsonl", 70_000, time.Unix(1_800_000_000, 1))
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(filepath.Join(elsewhere, "f"), []byte("abc"), 0o644); err != nil {
			t.Fatal(err)
		}
		for name, to := range map[string]string{"tofile": filepath.Join(elsewhere, "f"), "tofolder": elsewhere, "broken": filepath.Join(elsewhere, "gone")} {
			if err := os.Symlink(to, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	check := func(dir string, want int) {
		got, ok := Stats(dir)
		if !ok {
			if runtime.GOOS == "darwin" {
				t.Fatalf("%s: Stats read nothing on macOS", dir)
			}
			return
		}
		if !slices.IsSortedFunc(got, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) }) {
			t.Errorf("%s: Stats isn't in name order", dir)
		}
		if len(got) != want {
			t.Errorf("%s: Stats has %d entries, want %d", dir, len(got), want)
		}
		es, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			fi, err := os.Lstat(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			s, ok := got.Get(e.Name())
			if !ok {
				t.Errorf("%q: not in Stats", e.Name())
				continue
			}
			if s.Mode != fi.Mode().Type() || !s.ModTime().Equal(fi.ModTime()) || fi.Mode().IsRegular() && s.Size != fi.Size() {
				t.Errorf("%q: Stats says %v %d %v, Lstat %v %d %v", e.Name(), s.Mode, s.Size, s.Mod, fi.Mode().Type(), fi.Size(), fi.ModTime())
			}
		}
	}
	want := 3
	if runtime.GOOS != "windows" {
		want += 3
	}
	check(dir, want)

	// ~3000 long names are more than one call's 64 KB answer
	many := t.TempDir()
	for i := range 3000 {
		if err := os.WriteFile(filepath.Join(many, fmt.Sprintf("rollout-2026-10-27T06-%04d-%s.jsonl", i, strings.Repeat("n", 60))), []byte(strings.Repeat("y", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	check(many, 3000)

	if _, ok := Stats(filepath.Join(dir, "a.jsonl")); ok {
		t.Error("Stats read a file as a folder")
	}
	if _, ok := Stats(filepath.Join(dir, "gone")); ok {
		t.Error("Stats read a folder that isn't there")
	}
}

// A listing given back (Done) is the next one's room, and holds nothing of
// its folder: a smaller folder read into it has its own entries only.
func TestStatsAfterDone(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("listed in one call a folder on macOS only")
	}
	big, small := t.TempDir(), t.TempDir()
	for i := range 40 {
		if err := os.WriteFile(filepath.Join(big, fmt.Sprintf("b%02d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(small, "s"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		l, ok := Stats(big)
		if !ok || len(l) != 40 {
			t.Fatalf("big folder: %d entries, ok %t", len(l), ok)
		}
		l.Done(big)
		s, ok := Stats(small)
		if !ok || len(s) != 1 || s[0].Name != "s" || s[0].Size != 3 {
			t.Fatalf("small folder after a big one given back: %+v, ok %t", s, ok)
		}
		if rest := s[1:cap(s)]; slices.ContainsFunc(rest, func(e Entry) bool { return e.Name != "" }) {
			t.Fatalf("a listing given back kept its folder's entries: %+v", rest)
		}
		s.Done(small)
	}
}

// A listing given back is kept across garbage collections: a poll
// collects about twice a second, and a sync.Pool emptied by each had none
// for the next folder (#2278). A folder's next read takes the names it had
// where they are the same, and the room of the read before.
func TestListingKeptAcrossCollections(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("listed in one call a folder on macOS only")
	}
	listings.Lock()
	listings.last, listings.spare, listings.room, listings.entries = nil, nil, nil, 0 // what tests before kept
	listings.Unlock()
	dir := t.TempDir()
	for _, name := range []string{"rollout-a.jsonl", "rollout-b.jsonl", "rollout-c.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	first, ok := Stats(dir)
	if !ok {
		t.Fatal("Stats failed")
	}
	room, b := unsafe.SliceData(first[:cap(first)]), unsafe.StringData(first[1].Name)
	first.Done(dir)
	runtime.GC()
	runtime.GC()
	if err := os.WriteFile(filepath.Join(dir, "rollout-b.jsonl"), []byte("grown"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "rollout-c.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-d.jsonl"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	second, _ := Stats(dir)
	var names []string
	for _, e := range second {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"rollout-a.jsonl", "rollout-b.jsonl", "rollout-d.jsonl"}) || second[1].Size != 5 {
		t.Fatalf("after a file grew, one went and one came: %+v", second)
	}
	if unsafe.StringData(second[1].Name) != b {
		t.Error("a name listed before was copied again, or let go by a garbage collection")
	}
	second.Done(dir)
	runtime.GC()
	third, _ := Stats(dir)
	if unsafe.SliceData(third[:cap(third)]) != room {
		t.Error("a listing given back was let go by a garbage collection")
	}
	third.Done(dir)
}

// Folders of different sizes read one after another, as a poll reads
// Codex's day folders and Claude Code's projects, each take their own
// room: a room shared between them went from a small folder to a large
// one and was grown again on every poll (#2278).
func TestListingsOfDifferentSizesMadeOnce(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("listed in one call a folder on macOS only")
	}
	listings.Lock()
	listings.last, listings.spare, listings.room, listings.entries = nil, nil, nil, 0
	listings.Unlock()
	var dirs []string
	for _, n := range []int{2, 600, 5, 300, 1} {
		dir := t.TempDir()
		for i := range n {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("rollout-%04d.jsonl", i)), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		dirs = append(dirs, dir)
	}
	poll := func() {
		for _, dir := range dirs {
			if l, ok := Stats(dir); ok {
				l.Done(dir)
			}
		}
	}
	poll()
	poll()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 10 {
		poll()
	}
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > 64<<10 {
		t.Errorf("ten polls of unchanged folders allocated %d bytes, want next to none", got)
	}
}

// Forget lets go of every listing given back, and Stats still answers
// what the folder has after it.
func TestForgetLetsListingsGo(t *testing.T) {
	dir := t.TempDir()
	for i := range 5 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l, ok := Stats(dir)
	if !ok {
		t.Skip("Stats reads nothing here")
	}
	l.Done(dir)
	kept := func() int {
		listings.Lock()
		defer listings.Unlock()
		return len(listings.last) + len(listings.spare) + len(listings.room) + listings.entries
	}
	if kept() == 0 {
		t.Fatal("nothing kept of a listing given back")
	}
	Forget()
	if n := kept(); n != 0 {
		t.Fatalf("%d kept after Forget", n)
	}
	if l, _ := Stats(dir); len(l) != 5 {
		t.Fatalf("Stats after Forget has %d entries, want 5", len(l))
	}
}

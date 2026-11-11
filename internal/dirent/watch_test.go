//go:build darwin && cgo

package dirent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// statsOf is dir's listing, given back at once, and whether Stats read the
// folder for it.
func statsOf(t *testing.T, dir string) (Listing, bool) {
	t.Helper()
	before := reads.Load()
	l, ok := Stats(dir)
	if !ok {
		t.Fatalf("Stats(%s) failed", dir)
	}
	got := append(Listing(nil), l...)
	l.Done(dir)
	return got, reads.Load() != before
}

// aged has path last written a day ago, as a session long finished is.
func aged(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

// sizeOf is name's size in dir's listing as the next read after a Watch
// of root has it, and whether it is there.
func sizeOf(t *testing.T, root, dir, name string) (int64, bool) {
	t.Helper()
	Watch(root)
	l, _ := statsOf(t, dir)
	s, ok := l.Get(name)
	return s.Size, ok
}

// quiet is whether dir is no longer read, after a few reads to settle.
func quiet(t *testing.T, root, dir string) bool {
	t.Helper()
	for range 3 {
		Watch(root)
		statsOf(t, dir)
	}
	Watch(root)
	_, read := statsOf(t, dir)
	return !read
}

func dayFolder(t *testing.T) (root, day, rollout string) {
	t.Helper()
	root = t.TempDir()
	day = filepath.Join(root, "sessions", "2026", "11", "12")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout = filepath.Join(day, "rollout-a.jsonl")
	if err := os.WriteFile(rollout, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	aged(t, rollout)
	Watch(root)
	t.Cleanup(Forget)
	return root, day, rollout
}

// A watched folder nothing changed in is not read again (every Sessions
// poll read ~18k session files' folders, ~115 ms a poll), and the next
// read after a write to a file in it, a new file or a removed one sees
// it, with no wait.
func TestWatchedFolderReadOnlyWhenChanged(t *testing.T) {
	root, day, rollout := dayFolder(t)
	if !quiet(t, root, day) {
		t.Fatal("an unchanged watched folder was read again")
	}
	for i := range 50 {
		f, err := os.OpenFile(rollout, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(`{"type":"event"}` + "\n")
		f.Close()
		aged(t, rollout) // not hot: only the change heard has it read
		fi, _ := os.Stat(rollout)
		if n, _ := sizeOf(t, root, day, "rollout-a.jsonl"); n != fi.Size() {
			t.Fatalf("append %d: read %d bytes, the file has %d", i, n, fi.Size())
		}
	}
	b := filepath.Join(day, "rollout-b.jsonl")
	if err := os.WriteFile(b, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	aged(t, b)
	if _, ok := sizeOf(t, root, day, "rollout-b.jsonl"); !ok {
		t.Fatal("a new file wasn't seen")
	}
	if err := os.Remove(rollout); err != nil {
		t.Fatal(err)
	}
	if _, ok := sizeOf(t, root, day, "rollout-a.jsonl"); ok {
		t.Fatal("a removed file was still listed")
	}
	if !quiet(t, root, day) {
		t.Fatal("a watched folder was read again after it settled")
	}
}

// A write to a file held open is heard only when it closes, and Codex
// holds its rollout open for the whole session: a folder with a file
// written lately is read every time, and a change in the root's own
// folder (Codex's history.jsonl, at each prompt) has every folder under
// it read.
func TestFileHeldOpenStillSeen(t *testing.T) {
	root, day, rollout := dayFolder(t)
	f, err := os.OpenFile(rollout, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !quiet(t, root, day) {
		t.Fatal("an unchanged watched folder was read again")
	}

	// a session long quiet, taken up again: the root hears the prompt
	f.WriteString(`{"type":"resumed"}` + "\n")
	if err := os.WriteFile(filepath.Join(root, "history.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(rollout)
	if n, _ := sizeOf(t, root, day, "rollout-a.jsonl"); n != fi.Size() {
		t.Fatalf("after a prompt: read %d bytes, the file has %d", n, fi.Size())
	}
	// and from then on the folder is hot: each write is seen, unheard
	for i := range 20 {
		f.WriteString(`{"type":"event"}` + "\n")
		fi, _ := os.Stat(rollout)
		if n, _ := sizeOf(t, root, day, "rollout-a.jsonl"); n != fi.Size() {
			t.Fatalf("write %d to the open file: read %d bytes, the file has %d", i, n, fi.Size())
		}
	}
}

// A folder outside every watched root is read on every Stats.
func TestUnwatchedFolderReadEachTime(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	aged(t, filepath.Join(dir, "a"))
	t.Cleanup(Forget)
	for range 3 {
		if _, read := statsOf(t, dir); !read {
			t.Fatal("an unwatched folder's listing was reused")
		}
	}
}

// Each wait's folder is gone after it: two Watch calls a Sessions poll,
// and nothing of them is left in the probe's folder.
func TestProbeLeavesNothing(t *testing.T) {
	root, _, _ := dayFolder(t)
	for range 5 {
		Watch(root)
	}
	watched.Lock()
	dir := watched.probe.dir
	watched.Unlock()
	if dir == "" {
		t.Skip("no watch here")
	}
	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("the probe's folder holds %d entries after its waits", len(left))
	}
}

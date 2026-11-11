package provider

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Codex's idle check reads each sessions folder in one listing on macOS,
// and answers as the walk with an lstat() a file does: a file written
// within d deep in an old day folder is not quiet, older ones are, and a
// folder that can't be read is not quiet either.
func TestCodexSessionsQuietListed(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "sessions")
	now := time.Now()
	old := now.Add(-time.Hour)
	write := func(rel string, at time.Time) string {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return p
	}
	both := func(want bool, what string) {
		t.Helper()
		if got := codexSessionsQuietFor(home, now, time.Minute); got != want {
			t.Fatalf("%s: quiet %v, want %v", what, got, want)
		}
		got, ok := codexSessionsQuietListed(root, now, time.Minute)
		if runtime.GOOS == "darwin" && !ok {
			t.Fatalf("%s: no one-listing read on macOS", what)
		}
		if ok && got != want {
			t.Fatalf("%s: listed quiet %v, want %v", what, got, want)
		}
	}
	both(true, "no sessions folder")
	for _, day := range []string{"2026/09/01", "2026/10/30", "2026/11/11"} {
		write(day+"/rollout-a.jsonl", old)
		write(day+"/rollout-b.jsonl", old)
	}
	write("2026/11/11/notes.txt", now)
	both(true, "only old sessions, a new file that is no session")
	resumed := write("2026/09/01/rollout-c.jsonl", now)
	both(false, "a session in an old day folder written just now")
	if err := os.Chtimes(resumed, old, old); err != nil {
		t.Fatal(err)
	}
	both(true, "that session an hour old")

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		return
	}
	locked := filepath.Join(root, "2026", "08")
	if err := os.MkdirAll(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if codexSessionsQuietFor(home, now, time.Minute) {
		t.Fatal("a folder that can't be read: quiet")
	}
	if _, ok := codexSessionsQuietListed(root, now, time.Minute); ok {
		t.Fatal("a folder that can't be read: the one-listing read answered for it")
	}
}

package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

// A trashed session is erased for good only when asked (#487): its folder
// in the trash goes, files and note, and the other trashed one stays.
func TestPurgeOne(t *testing.T) {
	_, codex := manageSetup(t)
	cc, err := Delete("claude", mgCC)
	if err != nil {
		t.Fatal(err)
	}
	cx, err := Delete("codex", mgCX)
	if err != nil {
		t.Fatal(err)
	}
	if err := Purge(cc.Key); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(TrashDir(), cc.Key)); !os.IsNotExist(err) {
		t.Fatalf("the trashed folder is still there: %v", err)
	}
	if tr := Trash(); len(tr) != 1 || tr[0].Key != cx.Key {
		t.Fatalf("trash %+v, want the Codex session only", tr)
	}
	if err := Purge(cc.Key); err == nil {
		t.Fatal("purged twice")
	}
	if _, err := Restore(cc.Key); err == nil {
		t.Fatal("restored after it was erased")
	}
	// the Codex session's own folder was never touched, and it comes back
	if _, err := Restore(cx.Key); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(codex, "sessions", "*", "*", "*", "*.jsonl")); len(left) != 2 {
		t.Fatalf("restored %v", left)
	}
}

// Purging every trashed session leaves the trash empty and nothing else
// touched.
func TestPurgeAll(t *testing.T) {
	claude, _ := manageSetup(t)
	for _, d := range [][2]string{{"claude", mgCC}, {"codex", mgCX}} {
		if _, err := Delete(d[0], d[1]); err != nil {
			t.Fatal(err)
		}
	}
	if len(Trash()) != 2 {
		t.Fatalf("trash %+v", Trash())
	}
	for _, tr := range Trash() {
		if err := Purge(tr.Key); err != nil {
			t.Fatal(err)
		}
	}
	if len(Trash()) != 0 {
		t.Fatalf("trash %+v", Trash())
	}
	if _, err := os.Stat(filepath.Join(claude, "projects", "-work-app", "22222222-2222-3333-4444-555555555555.jsonl")); err != nil {
		t.Fatal("a session not in the trash was erased")
	}
	if _, err := os.Stat(TrashDir()); err != nil {
		t.Fatal("the trash folder itself was removed")
	}
}

// A key out of the trash, one naming no trashed session, or a folder that
// is a link out of it, is refused, and nothing outside is erased.
func TestPurgeRefusesOutOfTrash(t *testing.T) {
	manageSetup(t)
	outside := t.TempDir()
	keep := filepath.Join(outside, "keep", "x")
	os.MkdirAll(keep, 0o755)
	os.WriteFile(filepath.Join(keep, manifest), []byte("{}"), 0o644)
	os.MkdirAll(filepath.Join(TrashDir(), "claude"), 0o755)
	os.Symlink(filepath.Join(outside, "keep"), filepath.Join(TrashDir(), "codex"))
	os.Symlink(keep, filepath.Join(TrashDir(), "claude", "linked"))
	os.MkdirAll(filepath.Join(TrashDir(), "claude", "nonote"), 0o755)
	for _, key := range []string{
		"", "claude", "claude/", "claude/..", "claude/.", "claude/../../x", "../claude/x", "claude/a/b", `claude/a\b`,
		"opencode/x", "/etc/passwd", "claude/nope",
		"codex/x",       // its agent's folder is a link out of the trash
		"claude/linked", // the session's folder is
		"claude/nonote", // no trashed session's note in it
	} {
		if err := Purge(key); err == nil {
			t.Fatalf("purged %q", key)
		}
	}
	if _, err := os.Stat(filepath.Join(keep, manifest)); err != nil {
		t.Fatal("erased a folder out of the trash")
	}
	if _, err := os.Stat(filepath.Join(TrashDir(), "claude", "nonote")); err != nil {
		t.Fatal("erased a folder with no note")
	}
}

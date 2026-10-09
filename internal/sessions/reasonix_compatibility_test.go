package sessions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedReasonixStores(t *testing.T) {
	root := t.TempDir()
	t.Setenv("REASONIX_STATE_HOME", root)
	for _, relative := range []string{"sessions-v4/one", "projects/project/sessions-v4/two", "desktop-sessions-v5/by-id/three"} {
		d := filepath.Join(root, relative)
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"manifest.json", "events.frames"} {
			if err := os.WriteFile(filepath.Join(d, n), []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A query cache, an incomplete directory and old JSONL storage are not stores.
	for _, relative := range []string{"sessions-v4/.query-cache/x", "sessions-v4/incomplete", "projects/project/sessions/legacy"} {
		d := filepath.Join(root, relative)
		os.MkdirAll(d, 0700)
		os.WriteFile(filepath.Join(d, "manifest.json"), nil, 0600)
	}
	if n := UnsupportedReasonixStores(); n != 3 {
		t.Fatalf("stores=%d, want 3", n)
	}
	p := filepath.Join(root, "sessions-v4/one/events.frames")
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "unchanged" {
		t.Fatalf("store changed: %q %v", b, err)
	}
	os.Remove(p)
	if n := UnsupportedReasonixStores(); n != 2 {
		t.Fatalf("removed frame still counted: %d", n)
	}
	t.Setenv("REASONIX_STATE_HOME", t.TempDir())
	if n := UnsupportedReasonixStores(); n != 0 {
		t.Fatalf("empty home: %d", n)
	}
}

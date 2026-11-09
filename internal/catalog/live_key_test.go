package catalog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A list fetched again as it was keeps its key: what usage builds on it
// is kept. A list that differs in anything but when it was fetched, or is
// gone, gets another.
func TestLiveKeyOnlyMovesWithTheList(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	key := func() string {
		t.Helper()
		k, ok := LiveKey("acct")
		if !ok {
			t.Fatal("LiveKey not ok")
		}
		return k
	}
	if k := key(); k != "missing" {
		t.Fatalf("no list: %q", k)
	}
	a := []Model{{ID: "gpt-a", Context: 200000}, {ID: "gpt-b"}}
	if err := SaveLive("acct", "https://a/v1", a); err != nil {
		t.Fatal(err)
	}
	first := key()
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(LivePath("acct"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := SaveLive("acct", "https://a/v1", []Model{{ID: "gpt-a", Context: 200000}, {ID: "gpt-b"}}); err != nil {
		t.Fatal(err)
	}
	if f, _ := readLive("acct"); f.Fetched.Before(old.Add(30 * time.Minute)) {
		t.Fatalf("not saved again: fetched %v", f.Fetched)
	}
	if k := key(); k != first {
		t.Errorf("the same list fetched again: key %q, was %q", k, first)
	}
	if err := SaveLive("acct", "https://a/v1", []Model{{ID: "gpt-a", Context: 100000}, {ID: "gpt-b"}}); err != nil {
		t.Fatal(err)
	}
	if k := key(); k == first {
		t.Error("a list with a model changed kept its key")
	}
	os.Remove(LivePath("acct"))
	if k := key(); k != "missing" {
		t.Errorf("list removed: %q", k)
	}
}

// A list saved before Since was kept is dated by its fetch, so its fetch
// time is part of its key.
func TestLiveKeyOfAnOldListKeepsItsFetch(t *testing.T) {
	a := []byte("{\n  \"fetched\": \"2026-01-01T00:00:00Z\",\n  \"models\": [{\"id\":\"m\"}]\n}")
	b := []byte("{\n  \"fetched\": \"2026-02-01T00:00:00Z\",\n  \"models\": [{\"id\":\"m\"}]\n}")
	if liveKey(a, false) == liveKey(b, false) {
		t.Error("an old list's fetch time left out of its key")
	}
	if liveKey(a, true) != liveKey(b, true) {
		t.Error("a list's fetch time in its key")
	}
}

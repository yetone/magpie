package filememo

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A parse not read for a while is let go, so what start up read once
// (every agent's config) isn't kept for the life of the process; one a
// poll keeps reading stays.
func TestUnreadParseLetGo(t *testing.T) {
	mu.Lock()
	was := sweptAt
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); sweptAt = was; mu.Unlock() })
	dir := t.TempDir()
	once, polled := filepath.Join(dir, "once.json"), filepath.Join(dir, "polled.json")
	for _, p := range []string{once, polled} {
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-time.Hour)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	parses := map[string]int{}
	read := func(p string) {
		t.Helper()
		if _, err := Read("idle", p, func(b []byte) (int, error) { parses[p]++; return len(b), nil }); err != nil {
			t.Fatal(err)
		}
	}
	kept := func(p string) bool {
		mu.Lock()
		defer mu.Unlock()
		_, ok := seen[memoKey{"idle", p}]
		return ok
	}
	sweep := func(at time.Time) {
		mu.Lock()
		sweepIdle(at)
		mu.Unlock()
	}
	read(once)
	read(polled)
	now := time.Now()
	sweep(now.Add(idleFor)) // both were read since the last sweep
	if !kept(once) || !kept(polled) {
		t.Fatal("let go of a parse read since the last sweep")
	}
	read(polled)
	sweep(now.Add(time.Minute)) // sooner than idleFor after the last: nothing
	sweep(now.Add(2 * idleFor))
	if kept(once) {
		t.Fatal("a parse unread for idleFor is still kept")
	}
	if !kept(polled) {
		t.Fatal("let go of the parse a poll keeps reading")
	}
	read(once)
	if parses[once] != 2 || parses[polled] != 1 {
		t.Fatalf("parses %v, want once's again and polled's not", parses)
	}
}

package sessions

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A newer empty session must not eat the whole page: List(1) skips it
// and takes the next nonempty one (#1320; reproducer by @Ethereal49).
func TestListLimitSkipsNewerEmptySession(t *testing.T) {
	setup(t)
	now := time.Now()
	for _, f := range allFiles() {
		mod := now.Add(-time.Hour)
		if strings.HasPrefix(f.key, "claude:22222222-") {
			mod = now
		}
		if err := os.Chtimes(f.path, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	Reset()
	if got := List(0); len(got) != 2 {
		t.Fatalf("fixture: got %d nonempty sessions, want 2", len(got))
	}
	if got := List(1); len(got) != 1 {
		t.Fatalf("limit=1 returned %d nonempty sessions, want 1", len(got))
	}
}

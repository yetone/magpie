package dirent

import (
	"testing"
	"unsafe"
)

// TestEntrySize holds an entry at 40 bytes: ~130k of them are kept for a
// long history's listings, and a time.Time Mod made each 56 (#2278).
func TestEntrySize(t *testing.T) {
	if n := unsafe.Sizeof(Entry{}); n != 40 {
		t.Fatalf("an Entry is %d bytes, want 40", n)
	}
}

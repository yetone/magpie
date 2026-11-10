package sessions

import (
	"testing"
	"time"
	"unsafe"
)

// A Sessions poll copies every session file's file into several slices,
// so its size is held: 80 bytes, a size class of its own (#2278).
func TestFileFitsEightyBytes(t *testing.T) {
	if n := unsafe.Sizeof(file{}); n > 80 {
		t.Errorf("a file is %d bytes, want 80 at most", n)
	}
	// a time read back is the one written, and none stays none
	at := time.Unix(1790000000, 123456789)
	if got := modOf(at).Time(); !got.Equal(at) {
		t.Errorf("%v came back %v", at, got)
	}
	if m := modOf(time.Time{}); m != 0 || !m.Time().IsZero() || m.UnixNano() != (time.Time{}).UnixNano() {
		t.Errorf("no time came back as %d", m)
	}
}

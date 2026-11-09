package sessions

import (
	"testing"
	"unsafe"
)

// Every file of a history keeps its state, made one by one (decodeCache's
// reflect.New, parseRead's &state{}), so a state is held in the smallest
// size class it fits: 416 bytes, not the 448 a 424-byte state took (#2278).
func TestStateFitsItsSizeClass(t *testing.T) {
	if n := unsafe.Sizeof(state{}); n > 416 {
		t.Fatalf("a state is %d bytes, over the 416-byte size class", n)
	}
}

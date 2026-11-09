package usage

import (
	"testing"
	"unsafe"
)

// TestMsgKeySize: a long history keeps ~300k of these per pairing, so a
// field that pads it past 16 bytes is a third more of them held.
func TestMsgKeySize(t *testing.T) {
	if n := unsafe.Sizeof(msgKey{}); n != 16 {
		t.Fatalf("msgKey is %d bytes, want 16", n)
	}
}

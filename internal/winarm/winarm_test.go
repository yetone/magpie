package winarm

import (
	"debug/pe"
	"errors"
	"testing"
)

// Only an x64 build whose computer is Arm64 is emulated; a failed
// IsWow64Process2 is native.
func TestEmulated(t *testing.T) {
	for _, c := range []struct {
		arch   string
		native uint16
		err    error
		want   bool
	}{
		{"amd64", pe.IMAGE_FILE_MACHINE_ARM64, nil, true},
		{"amd64", pe.IMAGE_FILE_MACHINE_AMD64, nil, false},
		{"amd64", pe.IMAGE_FILE_MACHINE_ARM64, errors.New("procedure not found"), false},
		{"arm64", pe.IMAGE_FILE_MACHINE_ARM64, nil, false},
		{"386", pe.IMAGE_FILE_MACHINE_ARM64, nil, false},
	} {
		if got := emulated(c.arch, c.native, c.err); got != c.want {
			t.Errorf("emulated(%s, %#x, %v) = %v, want %v", c.arch, c.native, c.err, got, c.want)
		}
	}
}

// Off Windows, and on an x64 Windows computer, this process isn't
// emulated: the CI and test machines are one or the other.
func TestNotEmulatedHere(t *testing.T) {
	if Emulated() {
		t.Fatal("Emulated() on a test machine that isn't Windows on Arm")
	}
}

package proc

import (
	"encoding/binary"
	"testing"
)

// peHeader is the start of a PE image with the given optional-header
// magic and subsystem.
func peHeader(magic, subsystem uint16) []byte {
	b := make([]byte, 0x200)
	b[0], b[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(b[0x3c:], 0x80)
	copy(b[0x80:], "PE\x00\x00")
	opt := 0x80 + 4 + 20
	binary.LittleEndian.PutUint16(b[opt:], magic)
	binary.LittleEndian.PutUint16(b[opt+68:], subsystem)
	return b
}

// Only a PE of the GUI subsystem is a window's program: a console one
// (a CLI, a shim) and anything that isn't a whole PE header are not.
func TestGuiPE(t *testing.T) {
	for name, c := range map[string]struct {
		b    []byte
		want bool
	}{
		"gui pe32+":     {peHeader(0x20b, 2), true},
		"gui pe32":      {peHeader(0x10b, 2), true},
		"console pe32+": {peHeader(0x20b, 3), false},
		"bad magic":     {peHeader(0x107, 2), false},
		"cut short":     {peHeader(0x20b, 2)[:0x90], false},
		"script":        {[]byte("#!/bin/sh\necho 1\n"), false},
		"empty":         {nil, false},
	} {
		if got := guiPE(c.b); got != c.want {
			t.Errorf("%s: guiPE = %v, want %v", name, got, c.want)
		}
	}
}

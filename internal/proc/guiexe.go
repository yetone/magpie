package proc

import (
	"encoding/binary"
	"io"
	"os"
)

// guiExe reports whether the program at path is a Windows program of the
// GUI subsystem (IMAGE_SUBSYSTEM_WINDOWS_GUI in its PE header): one that
// opens a window, has no console, and never answers --version on one.
// OpenCode's desktop app is OpenCode.exe in
// %LOCALAPPDATA%\Programs\ai.opencode.desktop, an Electron program; with
// that folder on PATH, Windows' case-blind search finds it as opencode,
// and magpie's version check put OpenCode's window up at every start and
// after each update (saint carol on Discord). Its CLI (resources\
// opencode-cli.exe), npm's, bun's and scoop's shims are console programs
// and are run. Anything that isn't a PE, or whose header can't be read,
// is run as before.
func guiExe(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var b [4096]byte
	n, _ := io.ReadFull(f, b[:])
	return guiPE(b[:n])
}

const peSubsystemGUI = 2

// guiPE reads the subsystem from a PE image's first bytes: DOS header,
// PE signature at e_lfanew, the 20-byte COFF header, then the optional
// header, whose Subsystem is at 68 in PE32 and PE32+ alike.
func guiPE(b []byte) bool {
	if len(b) < 0x40 || b[0] != 'M' || b[1] != 'Z' {
		return false
	}
	pe := int(binary.LittleEndian.Uint32(b[0x3c:]))
	opt := pe + 4 + 20
	if pe < 0x40 || opt+70 > len(b) || string(b[pe:pe+4]) != "PE\x00\x00" {
		return false
	}
	if magic := binary.LittleEndian.Uint16(b[opt:]); magic != 0x10b && magic != 0x20b {
		return false
	}
	return binary.LittleEndian.Uint16(b[opt+68:]) == peSubsystemGUI
}

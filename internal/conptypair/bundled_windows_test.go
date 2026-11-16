//go:build conptypair

package conptypair

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// TestBundledOpens: the pair a release carries is written whole and its
// conpty.dll opens a pseudo console, which starts the OpenConsole.exe
// beside it.
func TestBundledOpens(t *testing.T) {
	// the loaded conpty.dll can't be removed while the test binary runs,
	// so the folder is removed as far as it can be, not by t.TempDir
	cache, err := os.MkdirTemp("", "conptypair-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cache) })
	dir, err := Ready(cache)
	if err != nil {
		t.Fatal(err)
	}
	dll := windows.NewLazyDLL(filepath.Join(dir, "conpty.dll"))
	create, closePC := dll.NewProc("CreatePseudoConsole"), dll.NewProc("ClosePseudoConsole")
	if err := create.Find(); err != nil {
		t.Fatal(err)
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer inW.Close()
	defer outR.Close()
	size := windows.Coord{X: 80, Y: 25}
	var h windows.Handle
	r, _, _ := create.Call(uintptr(*(*uint32)(unsafe.Pointer(&size))), inR.Fd(), outW.Fd(), 0, uintptr(unsafe.Pointer(&h)))
	inR.Close()
	outW.Close()
	// ClosePseudoConsole waits for what the console draws to be read
	go func() { _, _ = io.Copy(io.Discard, outR) }()
	if r != 0 {
		t.Fatalf("CreatePseudoConsole: 0x%x", r)
	}
	_, _, _ = closePC.Call(uintptr(h))
}

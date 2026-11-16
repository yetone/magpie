package winarm

import (
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

// Emulated says this process is an x64 magpie under Windows on Arm's
// emulation. IsWow64Process2 answers an x64 process there with
// processMachine IMAGE_FILE_MACHINE_UNKNOWN (x64 emulation isn't WOW64)
// and nativeMachine IMAGE_FILE_MACHINE_ARM64.
var Emulated = sync.OnceValue(func() bool {
	var proc, native uint16
	err := windows.IsWow64Process2(windows.CurrentProcess(), &proc, &native)
	return emulated(runtime.GOARCH, native, err)
})

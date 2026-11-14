package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

// endProcess ends magpie with code. On Windows os.Exit is ExitProcess,
// which ends every other thread and then runs each loaded DLL's detach on
// the one left: WebView2's, COM's and the GPU driver's included. One of
// those waiting on a thread already ended never returns, and the process
// stays with that one thread, still holding the gateway's port and its
// WebView2 profile, so the magpie an update restarted into hung "Not
// Responding" (BESTLJP0412 on X, 0.1.4179 to 0.1.4193). Everything magpie
// writes is written by now, and the kernel lets go of every handle, so
// the process ends at once instead (TerminateProcess skips the detaches).
func endProcess(code int) {
	_ = windows.TerminateProcess(windows.CurrentProcess(), uint32(code))
	os.Exit(code) // not reached
}

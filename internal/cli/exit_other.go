//go:build !windows

package cli

import "os"

// endProcess ends magpie with code (on Windows without ExitProcess's
// DLL detaches; see exit_windows.go).
func endProcess(code int) { os.Exit(code) }

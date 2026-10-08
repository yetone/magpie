//go:build !windows

package proc

import "os/exec"

// hide has nothing to do: only Windows gives a child a window of its own.
func hide(*exec.Cmd) {}

// batch has nothing to do: only Windows runs a script through cmd.exe.
func batch(*exec.Cmd) {}

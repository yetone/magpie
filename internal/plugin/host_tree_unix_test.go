//go:build !windows

package plugin

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// living says whether pid runs: one ended and not yet reaped by whoever
// took it in counts as gone.
func living(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	s := strings.TrimSpace(string(out))
	return s != "" && !strings.HasPrefix(s, "Z")
}

func endPID(pid int) { _ = syscall.Kill(pid, syscall.SIGKILL) }

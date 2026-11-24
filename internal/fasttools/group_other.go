//go:build !windows

package fasttools

import (
	"os/exec"
	"syscall"
)

// ownGroup runs cmd in a process group of its own, which its context's end
// kills whole: a check's shell and every program it started (a test
// binary, a watcher) end with it, not only the shell.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// shellArgs run a command line in a shell.
func shellArgs(line string) (string, []string) {
	if sh := lookShell(); sh != "" {
		return sh, []string{"-c", line}
	}
	return "/bin/sh", []string{"-c", line}
}

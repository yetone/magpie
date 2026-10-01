//go:build !windows

package update

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/yetone/magpie/internal/proc"
)

// alive reports whether pid is still running. A relaunched magpie is that
// process's child, so its exit shows as a new parent even before the
// process table lets go of it.
func alive(pid int) bool {
	if os.Getppid() != pid {
		return syscall.Kill(pid, 0) == nil
	}
	return true
}

// detach keeps a relaunched magpie out of this one's process group, so it
// outlives it.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// Reexec runs exe in this process's place with args and env: the same
// process, in the same terminal or under the same service manager, now
// running the new version (magpie web's restart to update).
func Reexec(exe string, args, env []string) error {
	proc.EndProbes() // the new version doesn't know them, nor waits on them
	return syscall.Exec(exe, append([]string{exe}, args...), env)
}

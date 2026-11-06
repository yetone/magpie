//go:build !windows

package proc

import (
	"os"
	"os/exec"
	"syscall"
)

// WaitFollowingStops waits for a started command in the foreground and
// reaps it, as cmd.Wait would, but follows its stops: when the terminal
// stops it (Ctrl+Z), magpie stops itself too, so the shell has the
// terminal back, and fg continues both. cmd.Wait can't see a stop. It
// answers the command's exit code, 128 plus the signal's number for one
// a signal ended. (`magpie claude run`: claude in front, magpie waiting.)
func WaitFollowingStops(cmd *exec.Cmd) (int, error) {
	pid := cmd.Process.Pid
	for {
		var ws syscall.WaitStatus
		if _, err := syscall.Wait4(pid, &ws, syscall.WUNTRACED, nil); err != nil {
			if err == syscall.EINTR {
				continue
			}
			return 0, err
		}
		switch {
		case ws.Stopped():
			_ = syscall.Kill(os.Getpid(), syscall.SIGSTOP)
		case ws.Exited():
			return ws.ExitStatus(), nil
		case ws.Signaled():
			return 128 + int(ws.Signal()), nil
		}
	}
}

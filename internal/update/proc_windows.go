package update

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/yetone/magpie/internal/proc"
)

const (
	synchronize  = 0x00100000
	waitTimeout  = 0x00000102
	detachedProc = 0x00000008
	newProcGroup = 0x00000200
	suspended    = 0x00000004
	noWindow     = 0x08000000
)

// alive reports whether pid is still running.
func alive(pid int) bool {
	h, err := syscall.OpenProcess(synchronize, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	ev, _ := syscall.WaitForSingleObject(h, 0)
	return ev == waitTimeout
}

// detach starts a relaunched magpie outside this one's console, so it
// outlives it. It gets no console at all, so Windows ignores the
// CREATE_NO_WINDOW proc may have set.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= detachedProc | newProcGroup
}

// Reexec starts exe with args and env in this console and returns, for
// the caller to exit: Windows can't run another program in a process's
// place, so the new version goes on printing where this one did.
func Reexec(exe string, args, env []string) error {
	cmd := proc.Command(exe, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return proc.StartReaped(cmd)
}

// trialStart starts path suspended and ends it at once: Windows checks a
// program when it creates its process, so one Smart App Control or
// another application control policy won't run fails here, and none of
// one it will run runs.
func trialStart(path string) error {
	cmd := proc.Command(path)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= suspended | noWindow
	if err := cmd.Start(); err != nil {
		return err
	}
	cmd.Process.Kill()
	cmd.Wait()
	return nil
}

//go:build darwin || linux

package tuidrive

import (
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/yetone/magpie/internal/proc"
	"golang.org/x/sys/unix"
)

// child is the agent, started in a pseudo-terminal.
type child struct{ cmd *exec.Cmd }

// EndSignals are what a driver ends on, its agent with it.
var EndSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP}

// Start runs the program at path with args and the environment env (its
// whole environment, TERM included) in a terminal of its own, and reads
// what it draws.
func (t *Term) Start(path string, args, env []string) error {
	m, name, err := openPTY()
	if err != nil {
		return err
	}
	tty, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return err
	}
	// wide, so a line of the agent's screen is a line
	_ = unix.IoctlSetWinsize(int(tty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: Rows, Col: Cols})
	cmd := proc.Command(path, args...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	// its own session, the terminal its controlling one: the driver ending
	// closes the terminal, and the agent is hung up on with it
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		m.Close()
		tty.Close()
		return err
	}
	tty.Close()
	t.pty, t.cmd = m, cmd
	go func() {
		t.Ended(cmd.Wait(), false)
	}()
	go t.read()
	return nil
}

// Stop ends the agent: asked, then made to.
func (t *Term) Stop() {
	if t.cmd == nil || t.cmd.Process == nil {
		return
	}
	select {
	case <-t.exited:
		return
	default:
	}
	_ = t.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-t.exited:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
	}
}

// OldConsole is whether the terminal is Windows 10's own pseudo console,
// which can lose a paste's marks and blocks of a long write: never here.
func OldConsole() bool { return false }

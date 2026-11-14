//go:build darwin || linux

package claudetui

import (
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/yetone/magpie/internal/proc"
	"golang.org/x/sys/unix"
)

// child is Claude Code, started in a pseudo-terminal.
type child struct{ cmd *exec.Cmd }

// endSignals are what the helper ends on, its Claude Code with it.
var endSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP}

func (s *shim) start(path string, args []string) error {
	m, name, err := openPTY()
	if err != nil {
		return err
	}
	tty, err := os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return err
	}
	// wide, so a line of Claude Code's screen is a line
	_ = unix.IoctlSetWinsize(int(tty.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: screenRows, Col: screenCols})
	cmd := proc.Command(path, args...)
	cmd.Env = childEnv(s.env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	// its own session, the terminal its controlling one: the shim ending
	// closes the terminal, and Claude Code is hung up on with it
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		m.Close()
		tty.Close()
		return err
	}
	tty.Close()
	s.pty, s.cmd = m, cmd
	go func() {
		s.ended(cmd.Wait(), false)
	}()
	go s.read()
	return nil
}

// stop ends Claude Code: asked, then made to.
func (s *shim) stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	select {
	case <-s.exited:
		return
	default:
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-s.exited:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	}
}

// imagePadOS: a terminal here passes the bracketed-paste marks (imagePad).
const imagePadOS = 0

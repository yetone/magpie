package claudetui

import (
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/yetone/magpie/internal/proc"
)

// child is Claude Code, started in a pseudo console (ConPTY): Windows' own
// terminal for a program that draws, as Windows Terminal runs one in.
type child struct {
	proc      windows.Handle
	console   windows.Handle
	closeOnce sync.Once
}

// endSignals are what the helper ends on, its Claude Code with it: Ctrl+C,
// and its console closed. The gateway ends a run by killing its job, which
// no handler hears: the images folder is then left for another helper to
// remove (dropLeft).
var endSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

func (s *shim) start(path string, args []string) error {
	// Windows 10 before 1809 has none, and x/sys panics calling it there
	if err := windows.NewLazySystemDLL("kernel32.dll").NewProc("CreatePseudoConsole").Find(); err != nil {
		return fmt.Errorf("no pseudo console on this Windows: %w", err)
	}
	// a .cmd (npm's shim) is run by cmd.exe, its arguments quoted as
	// Claude Code is run with elsewhere (proc)
	cmd := proc.Command(path, args...)
	if cmd.Err != nil {
		return cmd.Err
	}
	line := windows.ComposeCommandLine(cmd.Args)
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.CmdLine != "" {
		line = cmd.SysProcAttr.CmdLine
	}
	// what is typed goes in at inW, what Claude Code draws comes out at
	// outR; the console holds the other ends
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return err
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return err
	}
	var console windows.Handle
	// wide, so a line of Claude Code's screen is a line
	err := windows.CreatePseudoConsole(windows.Coord{X: screenCols, Y: screenRows}, inR, outW, 0, &console)
	// the console has its own of these
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)
	in, out := os.NewFile(uintptr(inW), "conpty-in"), os.NewFile(uintptr(outR), "conpty-out")
	if err != nil {
		in.Close()
		out.Close()
		return fmt.Errorf("a pseudo console: %w", err)
	}
	h, err := startIn(console, cmd.Path, line, childEnv(s.env...))
	if err != nil {
		windows.ClosePseudoConsole(console)
		in.Close()
		out.Close()
		return err
	}
	s.pty = conpty{in: in, out: out}
	s.proc, s.console = h, console
	go func() {
		_, _ = windows.WaitForSingleObject(h, windows.INFINITE)
		var code uint32
		var exit error
		if err := windows.GetExitCodeProcess(h, &code); err != nil {
			exit = err
		} else if code != 0 {
			exit = fmt.Errorf("exit status %d", code)
		}
		s.ended(exit, true)
	}()
	go s.read()
	return nil
}

// startIn starts the program at app, with the command line line and the
// environment env, attached to console.
func startIn(console windows.Handle, app, line string, env []string) (windows.Handle, error) {
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, err
	}
	defer attrs.Delete()
	// the attribute's value is the console's handle itself
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&console)), unsafe.Sizeof(console)); err != nil {
		return 0, err
	}
	si := &windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(*si))
	// standard handles of none: without this, the helper's own (the
	// bridge's pipes) are Claude Code's, and it draws into them rather
	// than the console (microsoft/terminal#11276)
	si.Flags = windows.STARTF_USESTDHANDLES
	appW, err := windows.UTF16PtrFromString(app)
	if err != nil {
		return 0, err
	}
	lineW, err := windows.UTF16PtrFromString(line)
	if err != nil {
		return 0, err
	}
	block, err := envBlock(env)
	if err != nil {
		return 0, err
	}
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(appW, lineW, nil, nil, false,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT, block, nil, &si.StartupInfo, &pi); err != nil {
		return 0, err
	}
	windows.CloseHandle(pi.Thread)
	return pi.Process, nil
}

// envBlock is env as CreateProcess takes it: each NUL-ended, then a NUL.
func envBlock(env []string) (*uint16, error) {
	var b []uint16
	for _, e := range env {
		u, err := windows.UTF16FromString(e)
		if err != nil {
			return nil, err
		}
		b = append(b, u...)
	}
	b = append(b, 0)
	return &b[0], nil
}

// conpty is a pseudo console's two ends as one terminal.
type conpty struct{ in, out *os.File }

func (c conpty) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c conpty) Write(p []byte) (int, error) { return c.in.Write(p) }

// closeConsole closes Claude Code's console, which tells it so, as closing
// a terminal window does (CTRL_CLOSE_EVENT). It returns at once: before
// Windows 11 24H2, ClosePseudoConsole waits for its output to be read.
func (s *shim) closeConsole() {
	s.closeOnce.Do(func() {
		go windows.ClosePseudoConsole(s.console)
	})
}

// stop ends Claude Code: its console closed, then killed.
func (s *shim) stop() {
	if s.proc == 0 {
		return
	}
	s.closeConsole()
	select {
	case <-s.exited:
	case <-time.After(3 * time.Second):
		_ = windows.TerminateProcess(s.proc, 1)
	}
}

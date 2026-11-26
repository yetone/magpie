package tuidrive

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/conptypair"
	"github.com/yetone/magpie/internal/proc"
)

// child is the agent, started in a pseudo console (ConPTY): Windows' own
// terminal for a program that draws, as Windows Terminal runs one in.
type child struct {
	proc      windows.Handle
	console   windows.Handle
	closeOnce sync.Once
}

// EndSignals are what a driver ends on, its agent with it: Ctrl+C, and
// its console closed. The gateway ends a run by killing its job, which no
// handler hears: what the driver keeps on disk is then left for another to
// remove (claudetui's dropLeft).
var EndSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// pseudoConsole is where CreatePseudoConsole and ClosePseudoConsole come
// from. Windows 10's own (conhost 10.0.19041) drops a paste's bracketed
// paste marks and loses 1024-byte blocks of a long write, so an agent
// (Claude Code) takes a long message as many pastes and gets a part of it
// (yetone/magpie#5205); the ConPTY Windows Terminal ships (conpty.dll,
// which runs the OpenConsole.exe beside it) keeps them. In order:
//   - dsh-cross: a conpty folder beside magpie's executable holding both,
//     the user's own;
//   - on Windows 10, the pair a release carries (internal/conptypair),
//     written to magpie's cache and checked against its pins there;
//   - the system's.
type pseudoConsole struct {
	create, close *windows.LazyProc
	// system: Windows' own, kernel32's
	system bool
}

var console = sync.OnceValue(func() pseudoConsole {
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Join(filepath.Dir(exe), "conpty")
		if _, err := os.Stat(filepath.Join(dir, "OpenConsole.exe")); err == nil {
			if c, ok := conptyIn(dir); ok {
				return c
			}
		}
	}
	if windows.RtlGetVersion().BuildNumber < win11 && conptypair.Bundled() {
		if cache, err := appdir.SystemCache(); err == nil {
			if dir, err := conptypair.Ready(cache); err == nil {
				if c, ok := conptyIn(dir); ok {
					return c
				}
			}
		}
	}
	k := windows.NewLazySystemDLL("kernel32.dll")
	return pseudoConsole{k.NewProc("CreatePseudoConsole"), k.NewProc("ClosePseudoConsole"), true}
})

// win11 is Windows 11's first build: the ConPTY of every one since keeps
// a paste's marks and every block of it.
const win11 = 22000

// conptyIn is the ConPTY of a folder's conpty.dll, ok when it has both.
func conptyIn(dir string) (pseudoConsole, bool) {
	dll := windows.NewLazyDLL(filepath.Join(dir, "conpty.dll"))
	create, closePC := dll.NewProc("CreatePseudoConsole"), dll.NewProc("ClosePseudoConsole")
	if create.Find() != nil || closePC.Find() != nil {
		return pseudoConsole{}, false
	}
	return pseudoConsole{create, closePC, false}, true
}

// OldConsole is whether the terminal is Windows 10's own pseudo console,
// which drops a paste's bracketed-paste marks and loses blocks of a long
// write (#5205): a conpty folder beside magpie.exe takes its place.
func OldConsole() bool {
	return console().system && windows.RtlGetVersion().BuildNumber < win11
}

func createPseudoConsole(size windows.Coord, in, out windows.Handle, h *windows.Handle) error {
	r, _, _ := console().create.Call(uintptr(*(*uint32)(unsafe.Pointer(&size))), uintptr(in), uintptr(out), 0, uintptr(unsafe.Pointer(h)))
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

func closePseudoConsole(h windows.Handle) { _, _, _ = console().close.Call(uintptr(h)) }

// Start runs the program at path with args and the environment env (its
// whole environment, TERM included) in a pseudo console of its own, and
// reads what it draws.
func (t *Term) Start(path string, args, env []string) error {
	// Windows 10 before 1809 has none, and x/sys panics calling it there
	if err := console().create.Find(); err != nil {
		return fmt.Errorf("no pseudo console on this Windows: %w", err)
	}
	// a .cmd (npm's shim) is run by cmd.exe, its arguments quoted as
	// every program magpie runs is (proc)
	cmd := proc.Command(path, args...)
	if cmd.Err != nil {
		return cmd.Err
	}
	line := windows.ComposeCommandLine(cmd.Args)
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.CmdLine != "" {
		line = cmd.SysProcAttr.CmdLine
	}
	// what is typed goes in at inW, what the agent draws comes out at
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
	var pc windows.Handle
	// wide, so a line of the agent's screen is a line
	err := createPseudoConsole(windows.Coord{X: Cols, Y: Rows}, inR, outW, &pc)
	// the console has its own of these
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)
	in, out := os.NewFile(uintptr(inW), "conpty-in"), os.NewFile(uintptr(outR), "conpty-out")
	if err != nil {
		in.Close()
		out.Close()
		return fmt.Errorf("a pseudo console: %w", err)
	}
	h, err := startIn(pc, cmd.Path, line, env)
	if err != nil {
		closePseudoConsole(pc)
		in.Close()
		out.Close()
		return err
	}
	t.pty = conpty{in: in, out: out}
	t.proc, t.console = h, pc
	go func() {
		_, _ = windows.WaitForSingleObject(h, windows.INFINITE)
		var code uint32
		var exit error
		if err := windows.GetExitCodeProcess(h, &code); err != nil {
			exit = err
		} else if code != 0 {
			exit = fmt.Errorf("exit status %d", code)
		}
		t.Ended(exit, true)
	}()
	go t.read()
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
	// bridge's pipes) are the agent's, and it draws into them rather
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

// closeConsole closes the agent's console, which tells it so, as closing
// a terminal window does (CTRL_CLOSE_EVENT). It returns at once: before
// Windows 11 24H2, ClosePseudoConsole waits for its output to be read.
func (t *Term) closeConsole() {
	t.closeOnce.Do(func() {
		go closePseudoConsole(t.console)
	})
}

// Stop ends the agent: its console closed, then killed.
func (t *Term) Stop() {
	if t.proc == 0 {
		return
	}
	t.closeConsole()
	select {
	case <-t.exited:
	case <-time.After(3 * time.Second):
		_ = windows.TerminateProcess(t.proc, 1)
	}
}

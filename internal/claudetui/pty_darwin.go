package claudetui

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPTY opens a pseudo-terminal: its master, and the name of its slave.
//
// The master is opened blocking and wrapped with os.NewFile, so it stays
// out of Go's kqueue poller. os.OpenFile puts a character device in the
// poller, non-blocking, and on some Macs (macOS 14.8 on x86_64 and on
// arm64) the first read of such a ptmx answered EAGAIN instead of
// waiting: the read loop ended, Claude Code filled the terminal's 1024
// bytes and hung, and the run said "showed no prompt" after 45s (#4834).
// A blocking read in its own thread works on every Mac.
func openPTY() (*os.File, string, error) {
	fd, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", &os.PathError{Op: "open", Path: "/dev/ptmx", Err: err}
	}
	m := os.NewFile(uintptr(fd), "/dev/ptmx")
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		m.Close()
		return nil, "", err
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		m.Close()
		return nil, "", err
	}
	var name [128]byte
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), uintptr(unix.TIOCPTYGNAME), uintptr(unsafe.Pointer(&name[0]))); e != 0 {
		m.Close()
		return nil, "", e
	}
	n := 0
	for n < len(name) && name[n] != 0 {
		n++
	}
	return m, string(name[:n]), nil
}

// the requests that read and set a terminal's modes (the tests' fake
// Claude Code sets its own raw, as the real one does)
const ioctlGetTermios, ioctlSetTermios = unix.TIOCGETA, unix.TIOCSETA

package proc

import (
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
)

// Shen Bao on Discord: Windows refused the gateway's port with WSAEACCES
// (a range excluded for Hyper-V, WSL or Docker) and no program had it.
// That error, as net.Listen gives it, is told apart from a port in use
// and from other errors.
func TestPortReserved(t *testing.T) {
	listenErr := func(errno syscall.Errno) error {
		return &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", errno)}
	}
	for _, c := range []struct {
		err  error
		want bool
	}{
		{listenErr(10013), true},  // WSAEACCES
		{listenErr(10048), false}, // WSAEADDRINUSE
		{listenErr(syscall.EADDRINUSE), false},
		{listenErr(syscall.EACCES), false}, // a port under 1024 on Linux or macOS
		{errors.New("bind: An attempt was made to access a socket in a way forbidden by its access permissions."), false},
		{nil, false},
	} {
		if got := PortReserved(c.err); got != c.want {
			t.Errorf("%v: %v, want %v", c.err, got, c.want)
		}
	}
}

package gui

import (
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/settings"
)

// reservedErr is net.Listen's error on Windows for a port in a range it
// excludes for Hyper-V, WSL or Docker: WSAEACCES, with no program on it.
func reservedErr() error {
	return &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", syscall.Errno(10013))}
}

// Shen Bao on Discord: Windows kept 3425 for itself, no program had it
// (netstat showed none), and the Gateway page's Start said "Port 3425 is
// in use by another program". Start now answers "reserved", which the
// page says as Windows keeping the port, and the state's gatewayHeld,
// which the card reads, is marked reserved.
func TestGatewayStartSaysAReservedPort(t *testing.T) {
	sandboxHome(t)
	port := freePort(t)
	addr := "127.0.0.1:" + strconv.Itoa(port)
	t.Setenv("MAGPIE_ADDR", addr)
	was := gateway.ListenTCP
	gateway.ListenTCP = func(network, a string) (net.Listener, error) {
		if a == addr {
			return nil, reservedErr()
		}
		return was(network, a)
	}
	served.Store(nil)
	t.Cleanup(func() {
		gateway.ListenTCP = was
		gatewayMu.Lock()
		stopServedLocked()
		gatewayMu.Unlock()
		gateway.ClearHeld()
	})

	gatewayMu.Lock()
	out := startHere()
	gatewayMu.Unlock()
	if out.OK || out.Reason != "reserved" || out.Port != strconv.Itoa(port) || out.PID != 0 {
		t.Fatalf("start on a reserved port: %+v", out)
	}
	if h := gateway.LastHeld(); h == nil || !h.Reserved {
		t.Fatalf("gatewayHeld %+v", h)
	}
}

// The siblings: a port picked in Settings, and the window's page in a
// browser, are said as Windows keeping them, and a port another program
// has still as that.
func TestReservedPortsAreNotCalledTaken(t *testing.T) {
	sandboxHome(t)
	t.Setenv("MAGPIE_ADDR", "")
	// the gateway's own port one nothing answers on, not this machine's
	// 3425
	cur := settings.Load()
	cur.Port = freePort(t)
	if err := settings.Save(cur); err != nil {
		t.Fatal(err)
	}
	served.Store(nil)
	wasFree := portFree
	t.Cleanup(func() { portFree = wasFree })
	portFree = func(string) error { return reservedErr() }
	_, err := setPort(freePort(t))
	if err == nil || !strings.Contains(err.Error(), "is reserved by Windows for Hyper-V, WSL or Docker") || strings.Contains(err.Error(), "another program") {
		t.Fatalf("Settings' port: %v", err)
	}
	portFree = func(string) error { return errors.New("bind: address already in use") }
	if _, err := setPort(freePort(t)); err == nil || !strings.Contains(err.Error(), "in use by another program") {
		t.Fatalf("a taken port: %v", err)
	}

	if s := busySays(3430, reservedErr()); !strings.HasPrefix(s, "port 3430 is reserved by Windows") || strings.Contains(s, "tries again") {
		t.Fatalf("the page: %q", s)
	}
	if s := busySays(3430, errors.New("bind: address already in use")); !strings.Contains(s, "tries again") {
		t.Fatalf("the page, taken: %q", s)
	}
}

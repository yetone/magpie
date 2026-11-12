package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// Who holds the gateway's port when it can't listen there (pluoki on X:
// Tailscale's `tailscale serve --https` on the same port number kept
// taking 8848 after every restart, and the page said only "another
// program"; it took them a while to find Tailscale). Only named, never
// touched. When the holder can't be read, nothing is said of it: the old
// wording stands.

// PortHeld is what holds the gateway's port.
type PortHeld struct {
	Port string `json:"port"`
	Name string `json:"name,omitempty"` // the program, when it could be read
	PID  int    `json:"pid,omitempty"`
	Path string `json:"path,omitempty"`
	// Tailscale: Tailscale's serve or funnel has the port (the holder is
	// Tailscale's, or `tailscale serve status` lists the port)
	Tailscale bool `json:"tailscale,omitempty"`
	// Alt: a port to give Tailscale's HTTPS proxy in its place
	Alt string `json:"alt,omitempty"`
}

// readHolder and readServes are the system readers, vars for the tests.
var (
	readHolder = proc.PortHolder
	readServes = proc.TailscaleServes
)

var lastHeld atomic.Pointer[PortHeld]

// LastHeld is what held the port the last time the gateway couldn't listen
// on it, nil once it did, or when the holder wasn't read.
func LastHeld() *PortHeld { return lastHeld.Load() }

// ClearHeld forgets LastHeld: the port is free, or served.
func ClearHeld() { lastHeld.Store(nil) }

// NoteHeld reads who holds the gateway's port and keeps it for LastHeld.
// false when that can't be told.
func NoteHeld() (PortHeld, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	b, ok := whoHolds(ctx, Port(), readHolder, readServes)
	if !ok {
		ClearHeld()
		return b, false
	}
	lastHeld.Store(&b)
	return b, true
}

// whoHolds, with the readers given for the tests.
func whoHolds(ctx context.Context, port string,
	holder func(context.Context, int) (proc.Holder, bool),
	serves func(context.Context, int) bool) (PortHeld, bool) {
	n, err := strconv.Atoi(port)
	if err != nil {
		return PortHeld{}, false
	}
	b := PortHeld{Port: port}
	if h, ok := holder(ctx, n); ok {
		b.Name, b.PID, b.Path = h.Name, h.PID, h.Path
		b.Tailscale = h.IsTailscale()
		// a holder that isn't Tailscale's own process may still be
		// Tailscale's serve (a forwarder it runs): only its own list says
	} else if serves(ctx, n) {
		// the daemon runs as root, out of this user's sight: Tailscale's
		// own list says it has the port
		b.Tailscale = true
	} else {
		return b, false
	}
	// an HTTPS port Tailscale serves on, other than the gateway's number
	b.Alt = "8443"
	if port == b.Alt {
		b.Alt = "9443"
	}
	return b, true
}

// Sentence is who holds the port, and for Tailscale what to do, in a
// line for a log or a terminal.
func (b PortHeld) Sentence() string {
	who := "another program"
	switch {
	case b.Tailscale && b.Name != "":
		who = "Tailscale (" + b.Name + ")"
	case b.Tailscale:
		who = "Tailscale"
	case b.Name != "":
		who = b.Name
	}
	if b.PID != 0 {
		who += " (pid " + strconv.Itoa(b.PID) + ")"
	}
	s := fmt.Sprintf("port %s is held by %s", b.Port, who)
	if b.Tailscale {
		s += fmt.Sprintf(": its serve or funnel is set to that port. Point it at magpie from another port: tailscale serve --https=%s http://127.0.0.1:%s (tailscale serve status lists what it serves now)", b.Alt, b.Port)
	}
	return s
}

// addrInUse says whether err is a listen that failed because another
// program has the address.
func addrInUse(err error) bool {
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	s := strings.ToLower(err.Error())
	// Windows' words for it, where errors.Is doesn't reach
	return strings.Contains(s, "address already in use") || strings.Contains(s, "only one usage of each socket address")
}

package gateway

import (
	"context"
	"net"
)

// Handover lets another process listen on the same address beside this
// one, for `make dev`: a restarted backend takes the gateway's port up
// before the one before it lets go, and that one finishes the streams it
// has instead of cutting them. Set before ListenAndServe; off, a second
// magpie can't bind the port, which is how it learns one already serves.
var Handover bool

// ListenTCP is net.Listen; tests, here and in internal/gui, have it fail
// as Windows does for a port it reserves.
var ListenTCP = net.Listen

// Listen listens on addr, beside another process when Handover is on;
// or takes the socket the magpie before this one handed over (Inherit)
// when it listens there.
func Listen(addr string) (net.Listener, error) {
	if ln, ack := takeInherited(addr); ln != nil {
		setAck(ack)
		return ln, nil
	}
	if !Handover {
		return ListenTCP("tcp", addr)
	}
	lc := net.ListenConfig{Control: reusePort}
	return lc.Listen(context.Background(), "tcp", addr)
}

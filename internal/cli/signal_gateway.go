package cli

import (
	"os"
	"sync"
	"sync/atomic"
)

// A command that owns a gateway drains it before the signal handler exits.
// Its return path waits for a signal already handling that drain, so main
// cannot exit (or re-exec) before the handler has finished saving state.
type signalGateway struct {
	stop func(os.Signal)
	done chan struct{}
}

var exitGateway atomic.Pointer[signalGateway]

func stopGatewayOnSignal(stop func()) func() {
	return stopGatewayOnSignalAs(func(os.Signal) { stop() })
}

// stopGatewayOnSignalAs is stopGatewayOnSignal for a command whose drain
// depends on the signal that ends it (serve's SIGTERM, serve_drain.go).
func stopGatewayOnSignalAs(stop func(os.Signal)) func() {
	g := &signalGateway{stop: stop, done: make(chan struct{})}
	exitGateway.Store(g)
	var once sync.Once
	return func() {
		once.Do(func() {
			if !exitGateway.CompareAndSwap(g, nil) {
				<-g.done
			}
		})
	}
}

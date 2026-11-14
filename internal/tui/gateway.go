package tui

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// The terminal serves the gateway while it is open, as the app does, when
// no magpie has it: in WSL with only magpie tui, the agents it connected to
// magpie asked an address nothing listened at, and Claude Code said
// ECONNREFUSED (gnayiab on X). Another magpie that has it (the app, magpie
// web, magpie serve) keeps it, and this one takes it up once that one is
// gone.

// gw is who serves the gateway, for the status line.
var gw struct {
	here atomic.Bool            // this process serves it
	err  atomic.Pointer[string] // why it couldn't, "" or nil when it could
}

// gatewayWatch is how often the terminal looks for a gateway gone.
var gatewayWatch = 15 * time.Second

// serveGateway serves the gateway here until ctx ends whenever no magpie
// answers at its address, and returns a wait for the one it serves to have
// stopped.
func serveGateway(ctx context.Context) (wait func()) {
	var wg sync.WaitGroup
	try := func() {
		if gw.here.Load() || gateway.Running() {
			return
		}
		s := gateway.New()
		up := make(chan error, 1)
		gw.here.Store(true)
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.ListenAndServe(ctx)
			gw.here.Store(false)
			if err != nil && ctx.Err() == nil {
				msg := err.Error()
				gw.err.Store(&msg)
			}
			up <- err
		}()
		// listening, or refused the port, in a moment
		for i := 0; i < 20 && !gateway.Running(); i++ {
			select {
			case <-up:
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		if gateway.Running() {
			gw.err.Store(nil)
		}
	}
	try()
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(gatewayWatch)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				try()
			}
		}
	}()
	return wg.Wait
}

// gatewayStatus is the status line's word on the gateway when it has
// nothing else to say: served here, or why agents can't reach it.
func gatewayStatus() string {
	if gw.here.Load() {
		return sMuted.Render("● " + trf("gateway %s · served while magpie tui is open", gateway.URL()))
	}
	if e := gw.err.Load(); e != nil && *e != "" {
		return sBad.Render("✗ ") + sText.Render(trf("gateway not running: %s", *e))
	}
	return ""
}

// gatewayGoneNote is said as the terminal quits having served the gateway.
func gatewayGoneNote() string {
	return tr("magpie: the gateway stopped with magpie tui; agents connected to magpie reach it while magpie tui, magpie web or magpie serve runs")
}

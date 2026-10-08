package gui

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// served is the gateway this process serves, nil while another magpie
// has it.
var served atomic.Pointer[gateway.Server]

// m365 serves the same gateway over loopback HTTPS while its setting is on.
var m365 = gateway.NewM365Server()

// backendCtx ends the gateway this process serves (stopServing).
var backendCtx, endBackend = context.WithCancel(context.Background())

// serving is the gateway this process serves, until it has finished.
var serving sync.WaitGroup

// stopServing stops this process's gateway taking requests and returns
// once those in flight have finished.
func stopServing() {
	endBackend()
	serving.Wait()
}

// startBackend starts what serves the page and the agents: the gateway,
// unless another magpie has it (then that one serves and this one only
// shows its status, and gw is nil — until that one is gone: a magpie left
// running from before an update, a magpie serve in a terminal), and the
// model lists kept warm.
func startBackend() (gw *gateway.Server) {
	gateway.Window = true // the routing this process serves is shown on its page
	gw = serveGateway()
	go watchGateway(backendCtx, gatewayWatch)
	// Model lists are fetched, never compiled in: whatever the agents can see
	// comes from the models.dev catalog plus each vendor's own /models answer.
	// Keep both halves warm without making the user click anything.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if catalog.Stale() {
			if err := catalog.Sync(ctx); err != nil {
				log.Println("catalog:", err)
			}
		}
		cancel()
		// A signed-in agent's list exists only at the vendor; fill it in the
		// first time so the picker never shows a stale snapshot.
		provider.FetchNew(20 * time.Second)
		// lists an older magpie wrote into agents' files, without what
		// it has learnt since (context windows, providers added)
		agent.SyncCatalog()
		// and fetched again each day it stays open, for new models' prices
		catalog.KeepFresh()
	}()
	return gw
}

// gatewayWatch is how often a magpie without the gateway looks for it gone.
const gatewayWatch = 15 * time.Second

// watchGateway takes the gateway up once the magpie that had it is gone,
// looking every so often until ctx ends: a watch that outlived it would
// start a gateway after this one has stopped serving.
func watchGateway(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		gatewayMu.Lock()
		if ctx.Err() == nil && served.Load() == nil && !gateway.Running() {
			serveGatewayLocked()
		}
		gatewayMu.Unlock()
	}
}

// gatewayMu keeps one start of the gateway here at a time: the watch's,
// a restart's, a take-over's (takeover.go).
var gatewayMu sync.Mutex

// servedRun ends the gateway this process serves, and is closed once it
// has, for a restart; under gatewayMu.
var servedRun struct {
	stop context.CancelFunc
	done chan struct{}
}

// serveGateway starts the gateway here when no magpie has it: the one
// started, or nil.
func serveGateway() *gateway.Server {
	gatewayMu.Lock()
	defer gatewayMu.Unlock()
	return serveGatewayLocked()
}

func serveGatewayLocked() *gateway.Server {
	// handing over, the one there is this one's predecessor, which lets go
	// once this one listens beside it
	if !gateway.Handover {
		if o := gateway.ServedBy(); o.Running {
			if update.Newer(gateway.Version, o.Version) {
				log.Printf("gateway: magpie %s serves %s, older than this one (%s); agents' requests go through it until it quits", o.Version, gateway.URL(), gateway.Version)
			}
			return nil
		}
	}
	gw := gateway.New()
	served.Store(gw)
	serving.Add(1)
	ctx, stop := context.WithCancel(backendCtx)
	done := make(chan struct{})
	servedRun.stop, servedRun.done = stop, done
	if settings.Load().M365 {
		if err := m365.Start(ctx, gw.Handler()); err != nil {
			log.Println("Microsoft 365 gateway:", err)
		}
	}
	go func() {
		defer serving.Done()
		defer close(done)
		defer stop()
		if err := gw.ListenAndServe(ctx); err != nil {
			log.Println("gateway:", err)
			served.CompareAndSwap(gw, nil) // another took the port first
		}
	}()
	return gw
}

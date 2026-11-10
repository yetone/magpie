package gui

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexrelay"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// served is the gateway this process serves, nil while another magpie
// has it.
var served atomic.Pointer[gateway.Server]

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
	// the usage log parsed now, not when the tray panel first asks for
	// today's calls (CyberCaius)
	go func() {
		// its files looked at once a second, as the warm-up's (#1665)
		defer filememo.Hold()()
		usage.ReadAhead()
	}()
	// Codex's background app-server, restarted when it has the list from
	// before a change and no codex session is on it (luci)
	go agent.KeepCodexDaemonCurrent(backendCtx)
	// the compactions magpie made in Codex's threads written as Codex's
	// own, so a thread goes on when Codex talks to OpenAI itself
	go agent.KeepCodexRolloutsPortable(backendCtx)
	// Model lists are fetched, never compiled in: whatever the agents can see
	// comes from the models.dev catalog plus each vendor's own /models answer.
	// Keep both halves warm without making the user click anything.
	go func() {
		// the copy magpie ships with, on a first run or over an older
		// one: models.dev may not be reachable (creeksuen on X)
		catalog.Seed()
		if catalog.Source() == "" {
			// no catalog file at all (the copy couldn't be written):
			// fetched before the lists below are made of it
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := catalog.Sync(ctx); err != nil {
				log.Println("catalog:", err)
			}
			cancel()
		}
		// fetched now when stale, beside the lists below rather than
		// before them, so a models.dev that hangs holds nothing up; a
		// fetch tells the agents (catalog.Changed). Then again each day
		// it stays open, for new models' prices, and soon after a fetch
		// that failed.
		go catalog.KeepFresh()
		// A signed-in agent's list exists only at the vendor; fill it in the
		// first time so the picker never shows a stale snapshot.
		provider.FetchNew(20 * time.Second)
		// lists an older magpie wrote into agents' files, without what
		// it has learnt since (context windows, providers added)
		agent.SyncCatalog()
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
		if ctx.Err() == nil && served.Load() == nil {
			// gone, or an older copy of the app done with its requests
			// (otherServes): served here; else looked at again next time
			serveGatewayLocked()
		}
		gatewayMu.Unlock()
	}
}

// takenSaid: the log has said the gateway's port is taken by one that
// doesn't answer, said once until it is free again.
var takenSaid atomic.Bool

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
	// paused by the user (gatewaypause.go): nothing serves it until resumed
	if pausedNow() {
		return nil
	}
	// handing over, the one there is this one's predecessor, which lets go
	// once this one listens beside it
	// so is the one that handed this one its socket, still finishing its
	// requests on it (gui/handover.go)
	if !gateway.Handover && !gateway.Inherited() {
		// the Codex relay a magpie quitting left at the port (codexrelay)
		// lets go of it, finishing the turns it streams
		codexrelay.Leave()
		// another magpie serving it: said on the page when its version
		// isn't this one's, and quit when it is an older copy of the app
		// with nothing in flight (gateway_older.go)
		if o := gateway.ServedBy(); o.Running {
			if !otherServes(o) {
				return nil
			}
			// the relay the one quit may have left at the port
			codexrelay.Leave()
		}
		// a magpie too busy to answer the question in time (a small
		// server, one starting), or another program, has the port: a
		// second listen only fails (K.Liu on Discord: magpie web beside
		// the service logged "bind: address already in use")
		if gateway.Taken() {
			if !takenSaid.Swap(true) {
				log.Printf("gateway: %s is taken but doesn't answer as magpie yet; this magpie serves the gateway once it is free", gateway.URL())
			}
			return nil
		}
	}
	takenSaid.Store(false)
	gatewayOther.Store(nil)
	gw := gateway.New()
	served.Store(gw)
	serving.Add(1)
	ctx, stop := context.WithCancel(backendCtx)
	done := make(chan struct{})
	servedRun.stop, servedRun.done = stop, done
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

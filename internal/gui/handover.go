package gui

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/handoff"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/update"
)

// A restart to update on a Mac or Linux hands the gateway to the new
// version rather than ending it (xuanwo on X): the new magpie is started
// while this one runs, takes the socket the gateway listens on
// (internal/handoff) and serves the next requests on it, while this one
// stops accepting, finishes the requests it has, and quits. Nothing an
// agent sends is refused or cut short on the way, so the restart waits
// only for what can't go on in another process: the turns of Claude Code
// run through the subscription bridge (restartBusy). Windows can't hand a
// socket over this way and `magpie web` re-executes in place; both still
// wait for the gateway to be idle (#577).
//
// The two never show at once. On a Mac this one lets go of its instance
// lock for the new one and hides its menu bar icon and windows once the
// new one serves. On Linux the D-Bus name Wails holds can't be let go, so
// the new one serves the gateway at once but waits for this one to quit
// before it puts up its tray icon; this one keeps its own until then.

// handoverHost is set by the app (Run): a restart there may hand the
// gateway over. `magpie web` and tests leave it unset.
var handoverHost atomic.Bool

// startExe is the program this magpie started as, symlinks resolved; an
// update puts the new version at the same path (on Linux the running
// one's own path reads "(deleted)" once it has).
var startExe string

// handsOver is whether a restart now would hand the gateway over.
var handsOver = func() bool {
	if !handoverHost.Load() || gateway.Handover || os.Getenv("MAGPIE_NO_HANDOVER") != "" || startExe == "" {
		return false
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return false
	}
	return served.Load() != nil
}

// restartBusy is what a restart now would cut short: the gateway's Busy,
// or, when the restart hands it over, the bridge's turns alone (counted
// as Requests, their end as Last), with the turns parked on tools.
func restartBusy() gateway.Busy { return blocking(gatewayBusy(), handsOver()) }

// blocking is the part of b a restart cuts short; handover, it hands the
// gateway over.
func blocking(b gateway.Busy, handover bool) gateway.Busy {
	if !handover {
		return b
	}
	return gateway.Busy{Requests: b.Bridge, Tools: b.Tools, Last: b.BridgeLast}
}

// pending is the handover under way, nil while none is.
var pending atomic.Pointer[handover]

// handedOver is set once the new magpie serves the gateway: this one's
// end leaves what goes with the gateway (Codex's failover) to it.
var handedOver atomic.Bool

// onHandedOver hides this magpie once the new one serves (the Mac's menu
// bar icon and windows); set by Run.
var onHandedOver func()

// handover is a restart handing the gateway to the new version.
type handover struct {
	offer    *handoff.Offer
	gw       *gateway.Server
	fallback func() error // relaunch once this one has exited, as before
	quit     chan func()  // the caller's quit, called once done (leave)
}

// Waits a handover gives the new magpie: to start and say hello, and to
// serve on the socket once it has it.
var (
	helloWait = 60 * time.Second
	ackWait   = 90 * time.Second
)

// startNew starts the new version beside this one: the Mac's bundle, or
// the binary. Vars for tests.
var (
	openBeside  = update.OpenBeside
	startBeside = update.StartBeside
)

// beginHandover starts the new version and hands the gateway over to it
// in the background, the caller quitting (leave) once that is done. False
// when this restart can't hand over: the caller restarts as before. quiet,
// a restart by itself: the Mac's new one opens in the background.
func beginHandover(bundle, exe string, window bool, view string, quiet bool) bool {
	if !handsOver() {
		return false
	}
	ho := &handover{gw: served.Load(), quit: make(chan func(), 1)}
	if bundle != "" {
		ho.fallback = func() error { return update.Relaunch(bundle, window, quiet) }
	} else {
		ho.fallback = func() error { return relaunchBinary(exe, "", window, view) }
	}
	pending.Store(ho)
	// the restart was let go while this one's requests were in flight,
	// since a handover carries them: one that can't be made goes the old
	// way once they have finished (fail), never by cutting them short
	offer, err := handoff.Open(settings.Dir(), startExe)
	if err != nil {
		go ho.failed(fmt.Errorf("the gateway can't be handed over: %w", err))
		return true
	}
	ho.offer = offer
	if bundle != "" {
		err = openBeside(bundle, window, quiet)
	} else {
		err = startBeside(exe, window, view)
	}
	if err != nil {
		go ho.failed(fmt.Errorf("the new version didn't start beside this one: %w", err))
		return true
	}
	go ho.run()
	return true
}

// failed gives the handover up before it began, and quits.
func (ho *handover) failed(err error) {
	ho.fail(err)
	(<-ho.quit)()
}

// leave quits once a handover under way is done, else at once.
func leave(quit func()) {
	if ho := pending.Load(); ho != nil {
		ho.quit <- quit
		return
	}
	quit()
}

// run hands the gateway over and quits: once the new magpie serves on its
// socket and this one has finished its requests, or, should it not take
// it in time, as a restart did before (the new one opened once this one
// has exited).
func (ho *handover) run() {
	quit := func() { (<-ho.quit)() }
	ctx, cancel := context.WithTimeout(context.Background(), helloWait)
	c, err := ho.offer.Accept(ctx)
	cancel()
	if err != nil {
		ho.fail(err)
		quit()
		return
	}
	defer c.Close()
	// the routing the new one goes on with: which account each
	// conversation stuck to
	gateway.FlushSticks()
	// the tool calls of this one's Claude Code runs, which their helper
	// sends to the port the new one will serve, come back here
	cbSock := filepath.Join(ho.offer.Dir(), "cb")
	cbLn, err := net.Listen("unix", cbSock)
	if err == nil {
		go ho.gw.ServeCallbacks(cbLn)
	} else {
		log.Println("update: no socket for the bridge's tool calls:", err)
		cbSock = ""
	}
	f, err := ho.gw.ListenerFile()
	if err == nil {
		releaseInstance() // for the new one to take as it starts
		err = c.Give(f, handoff.Meta{Addr: ho.gw.Addr(), Callbacks: cbSock, PID: os.Getpid()})
		f.Close()
		if err == nil {
			err = c.AwaitAck(ackWait)
		}
	}
	if err != nil {
		if cbLn != nil {
			cbLn.Close()
		}
		retakeInstance()
		ho.fail(err)
		quit()
		return
	}
	ho.offer.Withdraw()
	handedOver.Store(true)
	// and is gone by the end of its drain and a little after, whatever
	// holds its quit up: the copy an update left lived on hidden for
	// three hours in #4938, writing beside the new one
	quitBy(gateway.HandedOffDrain() + quitGrace)
	log.Printf("update: magpie %s (pid %d) serves the gateway now; this one finishes its requests and quits", c.Hello.Version, c.Hello.PID)
	if onHandedOver != nil {
		onHandedOver()
	}
	ho.gw.HandOff()
	endBackend()
	serving.Wait()
	if cbLn != nil {
		cbLn.Close()
	}
	ho.offer.Close()
	quit()
}

// quitGrace is how long past the gateway's drain a magpie that handed it
// over has to quit (quitBy), for what its end flushes; exitNow is
// os.Exit, which tests stand in for.
var (
	quitGrace = 3 * time.Minute
	exitNow   = os.Exit
)

// quitDeadline is the deadline quitBy set, nil before a handover.
var quitDeadline atomic.Pointer[time.Timer]

// quitBy exits the process after d unless it has ended by then: a
// magpie that handed the gateway over is hidden, serves nothing, and has
// nothing left for anyone, and its quit waiting on something that never
// comes left it running for hours (#4938).
func quitBy(d time.Duration) {
	t := time.AfterFunc(d, func() {
		log.Printf("update: still running %s after handing the gateway over; exiting", d)
		exitNow(0)
	})
	if old := quitDeadline.Swap(t); old != nil {
		old.Stop()
	}
}

// fallbackWait is how long a handover given up waits for the requests in
// flight before restarting as before: the drain a handed-over gateway
// gives them (gateway.handedOffDrain).
var fallbackWait = 15 * time.Minute

// fail gives the handover up: once the requests in flight have finished
// (the restart didn't wait for them), the new version opens once this one
// has exited, as a restart did before.
func (ho *handover) fail(err error) {
	log.Println("update: the gateway wasn't handed over, restarting as before once its requests have finished:", err)
	if ho.offer != nil {
		ho.offer.Close()
	}
	for deadline := time.Now().Add(fallbackWait); gatewayBusy().Any() && time.Now().Before(deadline); {
		time.Sleep(200 * time.Millisecond)
	}
	if ferr := ho.fallback(); ferr != nil {
		log.Println("update:", ferr)
	}
}

// takeOver, in the new magpie, takes the gateway from the one that
// started it, when that one hands it over: its socket is this one's to
// serve, and the tool calls of its Claude Code runs go back to it. The
// pid of the magpie before, 0 when none hands over.
func takeOver(exe string) int {
	t, err := handoff.Take(settings.Dir(), exe, handoff.Hello{PID: os.Getpid(), Version: Version}, 30*time.Second)
	if err != nil {
		log.Println("update: the gateway wasn't taken over:", err)
		return 0
	}
	if t == nil {
		return 0
	}
	var once sync.Once
	gateway.Inherit(t.Listener, func() {
		once.Do(func() {
			if err := t.Ack(); err != nil {
				log.Println("update: the magpie before didn't hear that this one serves:", err)
			}
		})
	})
	gateway.ForwardCallbacks(t.Meta.Callbacks)
	log.Printf("update: took the gateway on %s over from pid %d", t.Meta.Addr, t.Meta.PID)
	return t.Meta.PID
}

// handedOffWait is how long the new magpie on Linux waits for the one
// before to quit: its requests' drain (gateway.handedOffDrain) and a
// minute.
var handedOffWait = 16 * time.Minute

// awaitExit waits, most at most, for the process pid to end.
func awaitExit(pid int, most time.Duration) {
	for deadline := time.Now().Add(most); handoffAlive(pid) && time.Now().Before(deadline); {
		time.Sleep(200 * time.Millisecond)
	}
}

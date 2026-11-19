package cli

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

// endProbesOnSignal: the probes magpie asks a CLI with lead a session of
// their own (proc.ProbeContext), so Ctrl+C in the terminal, the terminal
// closing or a service manager's SIGTERM reach magpie and not them, and
// magpie ending on the signal left them to init. An owned gateway drains
// and saves its sessions, probes end, and magpie then dies of the signal. A command that waits on Ctrl+C itself
// (interruptContext) is left to finish its own way, its probes ended all the
// same; the app and the TUI, which quit on SIGINT and SIGTERM their own way,
// take those back (ownSignals). A signal magpie was started ignoring (nohup,
// a background job) stays ignored: asking for it would undo that.
func endProbesOnSignal() {
	caught = nil
	for _, s := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !signal.Ignored(s) {
			caught = append(caught, s)
		}
	}
	if len(caught) == 0 {
		return
	}
	onSignal = make(chan os.Signal, 1)
	signal.Notify(onSignal, caught...)
	go func(sigs chan os.Signal) {
		for s := range sigs {
			// asked before the probes are ended, which can take a moment
			// the command may spend finishing, and stop asking
			handled := s == os.Interrupt && interrupts.Load() > 0
			if !handled {
				finishSignalGateway(s)
			} else {
				proc.EndProbes()
			}
		}
	}(onSignal)
}

func finishSignalGateway(s os.Signal) {
	g := exitGateway.Swap(nil)
	if g != nil {
		defer close(g.done)
		g.stop(s)
	}
	proc.EndProbes()
	provider.FlushServed() // a gateway's last answers, noted and not yet written
	gateway.FlushSticks()  // and who answered which conversation
	// and the session index, or the next start reads every
	// file a scan read since the last save again
	savedWithin(sessionsSaved, saveOnSignal)
	die(s)
}

// saveOnSignal bounds how long a signal waits for the session index to be
// written: a long history's takes a second or two, and a signal is asked to
// end magpie now.
var saveOnSignal = 5 * time.Second

// hostsOnSignal bounds how long a signal waits for the plugin hosts to
// end: past the 15s EndHosts lets a host renewing a sign-in finish.
var hostsOnSignal = 20 * time.Second

// sessionsSaved is sessions.Saved; a var so tests can hold it.
var sessionsSaved = sessions.Saved

// savedWithin runs save and waits for it up to d.
func savedWithin(save func(), d time.Duration) {
	done := make(chan struct{})
	go func() { save(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// caught are the signals endProbesOnSignal asked for; onSignal is where
// they come.
var (
	caught   []os.Signal
	onSignal chan os.Signal
)

// die ends magpie of s, as s would have had there been no handler: the
// shell that ran it sees a process a signal ended (a loop running magpie
// stops on Ctrl+C), not one that exited. Where a signal can't be sent to
// oneself (Windows), it exits as a shell reports such a process. A var so
// tests can see what a signal would do.
var die = func(s os.Signal) {
	// the plugin hosts it started end with it, as at any exit (#7146):
	// one renewing a sign-in is given the time EndHosts gives it, and
	// one past that is killed with what it started
	savedWithin(plugin.EndHosts, hostsOnSignal)
	raise(s)
	code := 1
	if n, ok := s.(syscall.Signal); ok {
		code = 128 + int(n)
	}
	os.Exit(code)
}

// ownSignals hands SIGINT and SIGTERM back, for a command that quits on
// them its own way: Wails ends the app through OnShutdown (which ends the
// probes, and installs a downloaded update), bubbletea leaves the TUI's
// screen as it found it, and each comes back to main, where the probes are
// ended too. Exiting first skipped all of that. SIGHUP stays: neither
// handles it, and the terminal closing would end magpie there and then,
// with its probes, out of the terminal's reach, left running.
func ownSignals() {
	if onSignal == nil {
		return
	}
	signal.Stop(onSignal)
	for _, s := range caught {
		if s == syscall.SIGHUP {
			signal.Notify(onSignal, s)
		}
	}
}

// leaveSignals hands every signal endProbesOnSignal asked for back, for a
// command that starts no probe and ends on each its own way.
func leaveSignals() {
	if onSignal != nil {
		signal.Stop(onSignal)
	}
}

// interrupts counts the commands ending themselves on Ctrl+C.
var interrupts atomic.Int32

// interruptContext is signal.NotifyContext(…, os.Interrupt), for a command
// that ends itself on Ctrl+C rather than being ended by it.
func interruptContext() (context.Context, context.CancelFunc) {
	interrupts.Add(1)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	var once sync.Once
	return ctx, func() {
		stop()
		once.Do(func() { interrupts.Add(-1) })
	}
}

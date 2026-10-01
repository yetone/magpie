package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/yetone/magpie/internal/proc"
)

// endProbesOnSignal: the probes magpie asks a CLI with lead a session of
// their own (proc.ProbeContext), so Ctrl+C in the terminal, the terminal
// closing or a service manager's SIGTERM reach magpie and not them, and
// magpie ending on the signal left them to init. They are ended first, and
// magpie then dies of the signal. A command that waits on Ctrl+C itself
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
			proc.EndProbes()
			if !handled {
				die(s)
			}
		}
	}(onSignal)
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

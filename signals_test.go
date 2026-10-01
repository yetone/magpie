//go:build !windows

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/proc"
)

// deaths records what the signal handler would die of, and takes the
// handler down after the test.
func deaths(t *testing.T, sigs ...os.Signal) chan os.Signal {
	t.Helper()
	for _, s := range sigs {
		if signal.Ignored(s) {
			t.Skipf("%v is ignored here", s)
		}
	}
	died := make(chan os.Signal, 4)
	old := die
	die = func(s os.Signal) { died <- s }
	t.Cleanup(func() {
		if onSignal != nil {
			signal.Stop(onSignal)
			onSignal = nil
		}
		die = old
	})
	return died
}

func send(t *testing.T, s syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), s); err != nil {
		t.Fatal(err)
	}
}

func diesOf(t *testing.T, died chan os.Signal, want os.Signal) {
	t.Helper()
	select {
	case s := <-died:
		if s != want {
			t.Fatalf("died of %v, want %v", s, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("%v didn't end magpie", want)
	}
}

func livesOn(t *testing.T, died chan os.Signal, why string) {
	t.Helper()
	select {
	case s := <-died:
		t.Fatalf("magpie died of %v %s", s, why)
	case <-time.After(300 * time.Millisecond):
	}
}

// a command that doesn't quit on a signal itself ends its probes and dies
// of the signal
func TestSignalEndsMagpie(t *testing.T) {
	died := deaths(t, syscall.SIGTERM)
	endProbesOnSignal()
	send(t, syscall.SIGTERM)
	diesOf(t, died, syscall.SIGTERM)
}

// the app and the TUI take SIGINT and SIGTERM back: the signal reaches their
// own handling (Wails's, bubbletea's) and magpie doesn't end first, which
// skipped the app's OnShutdown and left the terminal on the TUI's screen
func TestOwnSignalsHandsThemBack(t *testing.T) {
	died := deaths(t, syscall.SIGTERM)
	endProbesOnSignal()
	ownSignals()
	theirs := make(chan os.Signal, 1) // the app's or the TUI's own
	signal.Notify(theirs, syscall.SIGTERM)
	defer signal.Stop(theirs)
	send(t, syscall.SIGTERM)
	select {
	case <-theirs:
	case <-time.After(3 * time.Second):
		t.Fatal("the command's own handling didn't get SIGTERM")
	}
	livesOn(t, died, "before the command could quit its own way")
}

// but not SIGHUP, which neither Wails nor bubbletea handles: the terminal
// closing under the app or the TUI still ends the probes before magpie
func TestOwnSignalsKeepsHangUp(t *testing.T) {
	died := deaths(t, syscall.SIGHUP)
	endProbesOnSignal()
	ownSignals()
	send(t, syscall.SIGHUP)
	diesOf(t, died, syscall.SIGHUP)
}

// a command ending itself on Ctrl+C is left to, even when it is done by the
// time the probes are ended (which can take a moment): that it was there is
// asked first
func TestInterruptWhileEndingProbes(t *testing.T) {
	died := deaths(t, os.Interrupt)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	proc.ProbeContext(ctx, "true") // never run: EndProbes waits a while on it
	endProbesOnSignal()
	_, stop := interruptContext()
	send(t, syscall.SIGINT)
	time.Sleep(300 * time.Millisecond) // the handler is ending the probes
	stop()                             // and the command is done
	select {
	case s := <-died:
		t.Fatalf("magpie died of %v in a command ending itself on Ctrl+C", s)
	case <-time.After(2 * time.Second):
	}
}

// the TUI asks CLIs while it loads, before bubbletea listens for signals:
// one then is still magpie's, which ends those probes, and the TUI takes the
// signals once it is ready
func TestTUIKeepsSignalsWhileLoading(t *testing.T) {
	died := deaths(t, syscall.SIGTERM)
	theirs := make(chan os.Signal, 1) // bubbletea's, once it runs
	signal.Notify(theirs, syscall.SIGTERM)
	defer signal.Stop(theirs)
	old := tuiRun
	tuiRun = func(ready func()) error {
		send(t, syscall.SIGTERM) // while loading
		diesOf(t, died, syscall.SIGTERM)
		<-theirs
		ready()
		send(t, syscall.SIGTERM) // once running
		<-theirs
		livesOn(t, died, "while the TUI quits on it its own way")
		return nil
	}
	defer func() { tuiRun = old }()
	endProbesOnSignal()
	if err := runTUI(); err != nil {
		t.Fatal(err)
	}
}

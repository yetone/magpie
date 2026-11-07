package awake

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/beat"
	"github.com/yetone/magpie/internal/filememo"
)

func TestWant(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		st   State
		want bool
	}{
		{State{}, false},
		{State{Busy: true}, true},
		{State{Last: now.Add(-time.Minute)}, true},
		{State{Last: now.Add(-Grace - time.Second)}, false},
	} {
		if got := want(c.st, now); got != c.want {
			t.Errorf("want(%+v) = %v, want %v", c.st, got, c.want)
		}
	}
}

// TestKeep holds sleep off while the agents work and the setting is on, once,
// and lets go when they are idle, when it is turned off and when Keep ends.
func TestKeep(t *testing.T) {
	oldHold, oldTick := hold, tick
	defer func() { hold, tick = oldHold, oldTick }()
	tick = time.Millisecond
	var held, takes atomic.Int32
	hold = func(bool) (func(), error) {
		held.Add(1)
		takes.Add(1)
		return func() { held.Add(-1) }, nil
	}
	var mu sync.Mutex
	on, st := true, State{}
	// the setting is written as settings.Save writes it (filememo.Forget)
	set := func(o bool, s State) { mu.Lock(); on, st = o, s; mu.Unlock(); filememo.Forget() }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Keep(ctx, func() Level {
			mu.Lock()
			defer mu.Unlock()
			if on {
				return System
			}
			return Off
		},
			func() State { mu.Lock(); defer mu.Unlock(); return st })
		close(done)
	}()
	wait := func(n int32, what string) {
		t.Helper()
		for end := time.Now().Add(2 * time.Second); held.Load() != n; {
			if time.Now().After(end) {
				t.Fatalf("%s: held %d, want %d", what, held.Load(), n)
			}
			time.Sleep(time.Millisecond)
		}
	}
	wait(0, "idle")
	set(true, State{Busy: true})
	wait(1, "busy")
	time.Sleep(20 * time.Millisecond)
	if takes.Load() != 1 {
		t.Fatalf("taken %d times while busy, want once", takes.Load())
	}
	set(true, State{Last: time.Now()})
	time.Sleep(20 * time.Millisecond)
	wait(1, "within grace")
	set(true, State{Last: time.Now().Add(-Grace - time.Minute)})
	wait(0, "past grace")
	set(false, State{Busy: true})
	time.Sleep(20 * time.Millisecond)
	wait(0, "off")
	set(true, State{Busy: true})
	wait(1, "on again")
	cancel()
	<-done
	wait(0, "ended")
}

// TestKeepDisplay holds the display on only at Display, and takes the hold
// anew when the level changes while the agents work (#975).
func TestKeepDisplay(t *testing.T) {
	oldHold, oldTick := hold, tick
	defer func() { hold, tick = oldHold, oldTick }()
	tick = time.Millisecond
	var mu sync.Mutex
	var holds []bool // display of each hold taken
	live := 0
	hold = func(display bool) (func(), error) {
		mu.Lock()
		defer mu.Unlock()
		holds = append(holds, display)
		live++
		return func() { mu.Lock(); live--; mu.Unlock() }, nil
	}
	level := System
	set := func(l Level) { mu.Lock(); level = l; mu.Unlock(); filememo.Forget() }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Keep(ctx, func() Level { mu.Lock(); defer mu.Unlock(); return level },
			func() State { return State{Busy: true} })
		close(done)
	}()
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(2 * time.Second); ; {
			mu.Lock()
			good := ok()
			mu.Unlock()
			if good {
				return
			}
			if time.Now().After(end) {
				mu.Lock()
				defer mu.Unlock()
				t.Fatalf("%s: holds %v, live %d", what, holds, live)
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitFor("system", func() bool { return len(holds) == 1 && !holds[0] && live == 1 })
	set(Display)
	waitFor("display", func() bool { return len(holds) == 2 && holds[1] && live == 1 })
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	if len(holds) != 2 {
		t.Errorf("taken again at the same level: %v", holds)
	}
	mu.Unlock()
	set(System)
	waitFor("system again", func() bool { return len(holds) == 3 && !holds[2] && live == 1 })
	set(Off)
	waitFor("off", func() bool { return live == 0 })
	cancel()
	<-done
}

// Turned off, Keep looks at the setting again when it is written, not each
// few seconds (#3185), and turning it on is still taken at once.
func TestKeepOffWaitsForTheSetting(t *testing.T) {
	oldHold, oldTick := hold, tick
	defer func() { hold, tick = oldHold, oldTick }()
	tick = time.Millisecond
	var held atomic.Int32
	hold = func(bool) (func(), error) { held.Add(1); return func() { held.Add(-1) }, nil }
	var looks atomic.Int32
	var on atomic.Bool
	// turned off, Keep also looks at each beat (beat.Next: the multiples of
	// OthersEvery on the clock); the 50ms counted below stay clear of one,
	// or its look counts as a second (#3687)
	every := filememo.OthersEvery()
	if time.Until(time.Now().Truncate(every).Add(every)) < time.Second {
		<-beat.Next(every)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Keep(ctx, func() Level {
			looks.Add(1)
			if on.Load() {
				return System
			}
			return Off
		}, func() State { return State{Busy: true} })
		close(done)
	}()
	defer func() { cancel(); <-done }()
	for end := time.Now().Add(2 * time.Second); looks.Load() == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("Keep never looked at the setting")
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := looks.Load(); n != 1 {
		t.Fatalf("turned off, Keep looked at the setting %d times in 50ms with nothing written", n)
	}
	on.Store(true)
	filememo.Forget()
	for end := time.Now().Add(2 * time.Second); held.Load() != 1; time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("turning the setting on held nothing")
		}
	}
}

// Turned off, a setting turned on while Keep looks, after the look read it,
// is looked at again at once, not at the beat after: Keep waits on the
// Written it took before the look, which that write closes.
func TestKeepOffSeesAWriteDuringTheLook(t *testing.T) {
	oldHold := hold
	defer func() { hold = oldHold }()
	hold = func(bool) (func(), error) { return func() {}, nil }
	type write struct{ at, next time.Time }
	wrote := make(chan write, 1)
	// when this test's level is asked again: a hold could be taken by a
	// Keep that another test's failure left running
	again := make(chan time.Time, 1)
	var on atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Keep(ctx, func() Level {
			if on.Load() {
				select {
				case again <- time.Now():
				default:
				}
				return System
			}
			// the beat Keep waits for, turned off (beat.Next: the next
			// multiple of OthersEvery on the clock), 2s away at least, so
			// that a look at once has room before it
			every := filememo.OthersEvery()
			next := time.Now().Truncate(every).Add(every)
			if time.Until(next) < 2*time.Second {
				<-beat.Next(every)
				next = time.Now().Truncate(every).Add(every)
			}
			// the user turns it on just after this read, as settings.save
			// writes it (filememo.Forget)
			at := time.Now()
			on.Store(true)
			filememo.Forget()
			wrote <- write{at, next}
			return Off
		}, func() State { return State{Busy: true} })
		close(done)
	}()
	defer func() { cancel(); <-done }()
	var w write
	select {
	case w = <-wrote:
	case <-time.After(10 * time.Second):
		t.Fatal("Keep never looked at the setting")
	}
	select {
	case at := <-again:
		// both on the wall clock, as beat.Next goes by it (Truncate drops
		// the monotonic reading)
		if !at.Before(w.next) {
			t.Fatalf("turned on during the look, the setting was looked at again %v after it was written, at the beat (%s), not at once", at.Sub(w.at), w.next.Format(time.TimeOnly))
		}
	case <-time.After(time.Until(w.next) + 10*time.Second):
		t.Fatal("turned on during the look, the setting was never looked at again")
	}
}

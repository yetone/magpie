package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// serve restarts into an update only once the gateway is idle: nothing in
// flight and nothing ended in the last serveIdleSettle, as the app's
// restart to update waits (#577). After serveWaitMost it no longer waits
// for that gap, but still never cuts a request in flight. A program not
// replaced, or a build of one's own, is never restarted.
func TestServeFollowWaitsForIdle(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "magpie")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(exe)
	if err != nil {
		t.Fatal(err)
	}
	var busy gateway.Busy
	stops := 0
	f := &serveFollow{exe: exe, started: st, busy: func() gateway.Busy { return busy }, released: true, stop: func() { stops++ }}
	now := time.Now()

	if f.check(now) || stops != 0 || f.restartTo() != "" {
		t.Fatal("restarted with nothing put in")
	}
	if a := f.answer(""); a.State != "current" {
		t.Fatalf("magpie update's ask with nothing put in: %+v", a)
	}

	if err := os.WriteFile(exe, []byte("the new version"), 0o755); err != nil {
		t.Fatal(err)
	}
	busy = gateway.Busy{Requests: 1}
	if f.check(now) || stops != 0 {
		t.Fatal("restarted with a request in flight")
	}
	if a := f.answer(""); a.State != "waiting" || a.Busy == nil || a.Busy.Requests != 1 {
		t.Fatalf("magpie update's ask while busy: %+v", a)
	}
	busy = gateway.Busy{Last: now.Add(-time.Second)}
	if f.check(now) || stops != 0 {
		t.Fatal("restarted in a tool chain's gap")
	}
	// past serveWaitMost, the gap no longer holds it, a request still does
	busy = gateway.Busy{Requests: 1, Last: now}
	if f.check(now.Add(serveWaitMost+time.Minute)) || stops != 0 {
		t.Fatal("restarted with a request in flight, after waiting long")
	}
	busy = gateway.Busy{Last: now.Add(serveWaitMost)}
	if !f.check(now.Add(serveWaitMost+time.Minute)) || stops != 1 || f.restartTo() != exe {
		t.Fatalf("not restarted once nothing was in flight after waiting long: stops %d, restartTo %q", stops, f.restartTo())
	}

	g := &serveFollow{exe: exe, started: st, busy: func() gateway.Busy { return gateway.Busy{} }, released: true, stop: func() { stops++ }}
	if !g.check(time.Now()) || stops != 2 || g.restartTo() != exe {
		t.Fatal("not restarted when idle at once")
	}

	// a build of one's own: a rebuild in place isn't an update
	d := &serveFollow{exe: exe, started: st, busy: func() gateway.Busy { return gateway.Busy{} }, stop: func() { stops++ }}
	d.run(t.Context())
	if a := d.answer(""); a.State != "unsupported" || stops != 2 || d.restartTo() != "" {
		t.Fatalf("a build of one's own: %+v, stops %d", a, stops)
	}
}

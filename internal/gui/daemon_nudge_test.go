package gui

import (
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gateway"
)

// Norethcy on X: the app updated and the background service stayed on the
// version before, until `magpie daemon restart` by hand. The app now asks
// its own service, once, to restart into the version on disk.
func TestNudgeAsksOwnOlderDaemonOnce(t *testing.T) {
	was := gateway.Version
	gateway.Version = "0.1.2000"
	defer func() { gateway.Version = was }()
	wasOwn, wasAsk := ownDaemon, askRestart
	defer func() { ownDaemon, askRestart = wasOwn, wasAsk }()
	nudgedMu.Lock()
	nudged = map[int]bool{}
	nudgedMu.Unlock()

	asked := make(chan string, 4)
	askRestart = func(v string) (gateway.RestartReply, error) {
		asked <- v
		return gateway.RestartReply{State: "restarting"}, nil
	}
	own := true
	ownDaemon = func(gateway.Served) bool { return own }

	older := gateway.Served{Running: true, Version: "0.1.1990", Pid: 4321, Serves: "serve"}
	nudgeDaemon(older, skewOf(older))
	select {
	case v := <-asked:
		if v != "0.1.2000" {
			t.Errorf("asked to restart into %q", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("an older service of this install wasn't asked to restart")
	}
	// the same process is asked once
	nudgeDaemon(older, skewOf(older))
	// a newer one, and one that isn't this install's service, never
	newer := gateway.Served{Running: true, Version: "0.1.2010", Pid: 4322, Serves: "serve"}
	nudgeDaemon(newer, skewOf(newer))
	own = false
	other := gateway.Served{Running: true, Version: "0.1.1990", Pid: 4323, Serves: "serve"}
	nudgeDaemon(other, skewOf(other))
	select {
	case v := <-asked:
		t.Fatalf("asked again: %q", v)
	case <-time.After(300 * time.Millisecond):
	}
}

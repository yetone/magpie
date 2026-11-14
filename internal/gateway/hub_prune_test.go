package gateway

import (
	"sync/atomic"
	"testing"
	"time"
)

// heldHub's Prune holds until released, and says while it runs.
type heldHub struct {
	fakeHub
	entered chan struct{}
	release chan struct{}
	running atomic.Bool
}

func (h *heldHub) Prune() {
	if !h.running.CompareAndSwap(false, true) {
		return
	}
	select {
	case h.entered <- struct{}{}:
	default:
	}
	<-h.release
	h.running.Store(false)
}

// The hub's pruner reads MCPHub, which tests swap: once ListenAndServe has
// returned, the pruner it started has ended, so a test that swaps MCPHub
// after a gateway's end races nothing (TestInfoSaysMCPHub, -race on CI at
// 3dbbb173e). A Prune held mid-call keeps the gateway's end waiting.
func TestGatewayEndEndsHubPruner(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", freeAddr(t))
	was := hubPruneEvery
	hubPruneEvery = time.Millisecond
	t.Cleanup(func() { hubPruneEvery = was })
	h := &heldHub{entered: make(chan struct{}, 1), release: make(chan struct{})}
	old := MCPHub
	MCPHub = h
	t.Cleanup(func() { MCPHub = old })
	stop, done := listening(t, New())
	select {
	case <-h.entered:
	case <-time.After(10 * time.Second):
		stop()
		<-done
		t.Fatal("the hub was never pruned")
	}
	stop()
	select {
	case <-done:
		close(h.release)
		t.Fatal("the gateway ended while its pruner was still in Prune")
	case <-time.After(time.Second):
	}
	close(h.release)
	<-done
	if h.running.Load() {
		t.Error("the pruner still ran after the gateway ended")
	}
}

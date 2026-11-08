package plugin

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// heavyFor sets the host's memory bound for one test (heavyMB and its
// timing), checked every 20ms from the host's start.
func heavyFor(t *testing.T, mb int) {
	t.Helper()
	was, after, every := heavyMB, heavyAfter, heavyEvery
	heavyMB, heavyAfter, heavyEvery = mb, 0, 20*time.Millisecond
	t.Cleanup(func() { heavyMB, heavyAfter, heavyEvery = was, after, every })
}

func currentHost() *host {
	hostMu.Lock()
	defer hostMu.Unlock()
	return current
}

// A host holding more than heavyMB after a full collection is replaced:
// it ends, and the next call is answered by a new host. A host that grew
// with what its plugins kept was otherwise left to grow until the system
// killed it (783 MiB after 2h25m, OOM-killed on a small Linux server).
// Any host holds more than 1 MiB.
func TestHeavyHostIsReplaced(t *testing.T) {
	heavyFor(t, 1) // restored after sandbox's Settle has ended the hosts
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	was := recycles.Load()
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(30 * time.Second)
	for recycles.Load() == was {
		if time.Now().After(until) {
			t.Fatal("a host past its memory bound was not replaced 30s on")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// the one replaced ends, and a new host answers (replaced in its turn,
	// past the bound too)
	until = time.Now().Add(30 * time.Second)
	for {
		hostMu.Lock()
		n := len(retiring)
		hostMu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(until) {
			t.Fatal("the replaced host was still finishing its calls 30s on")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := Call(ctx, "providers", map[string]any{}, nil); err != nil {
		t.Fatalf("a call after the heavy host was replaced: %v", err)
	}
}

// A host under the bound is left alone, however often it checks.
func TestLightHostIsKept(t *testing.T) {
	heavyFor(t, 1<<20)
	sandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("testdata/fake/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	was := recycles.Load()
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	h := currentHost()
	if h == nil {
		t.Fatal("no host")
	}
	// fifty checks
	time.Sleep(time.Second)
	if recycles.Load() != was || currentHost() != h || !h.alive() {
		t.Fatal("a host under its memory bound was replaced")
	}
}

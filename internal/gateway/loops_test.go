package gateway

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// What the gateway starts beside the server ends before ListenAndServe
// returns: a loop still saving as ctx ended wrote into the config folder
// after the gateway had ended, which a test was removing (#7232,
// KeepAutoPicksHeld's auto-picks.json).
func TestGatewayEndWaitsForItsLoops(t *testing.T) {
	fresh(t)
	late := filepath.Join(t.TempDir(), "saved")
	var began atomic.Bool
	oldWork := WhileServing
	WhileServing = append([]func(context.Context){func(ctx context.Context) {
		began.Store(true)
		<-ctx.Done()
		// a save under way as the gateway ends
		time.Sleep(300 * time.Millisecond)
		os.WriteFile(late, []byte("x"), 0o644)
	}}, oldWork...)
	t.Cleanup(func() { WhileServing = oldWork })

	t.Setenv("MAGPIE_ADDR", "127.0.0.1:0")
	stop, done := listening(t, New())
	defer stop()
	waitFor(t, "the serving work to begin", began.Load)
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(late); err != nil {
		t.Fatalf("ListenAndServe returned with a loop it started still saving: %v", err)
	}
}

// A loop that doesn't end with its ctx is left, and the gateway's end
// doesn't wait on it for ever.
func TestGatewayEndBoundsItsWaitForLoops(t *testing.T) {
	was := loopsEndFor
	loopsEndFor = 50 * time.Millisecond
	t.Cleanup(func() { loopsEndFor = was })
	var bg loops
	release := make(chan struct{})
	defer close(release)
	bg.Go("stuck", func() { <-release })
	ended := make(chan struct{})
	go func() { bg.end(); close(ended) }()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the end waited on a loop that never ends")
	}
	// and nothing starts once it has ended
	bg.Go("late", func() { <-release })
	bg.mu.Lock()
	n := bg.running["late"]
	bg.mu.Unlock()
	if n != 0 {
		t.Fatal("a loop started after the end")
	}
}

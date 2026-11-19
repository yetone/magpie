package gateway

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/filelock"
)

// A Kubernetes rolling update runs the new pod beside the old one on one
// volume, each gateway on a port of its own. The new one answers requests
// at once, but the work only the serving magpie does (renewing sign-ins,
// check-ins, WhileServing) waits until the old one's gateway ends: two
// never do it at once. Without the serving lock both did.
func TestServingWorkWaitsForTheOtherMagpie(t *testing.T) {
	fresh(t)
	oldRetry := servingRetry
	servingRetry = 10 * time.Millisecond
	t.Cleanup(func() { servingRetry = oldRetry })
	var working atomic.Int32
	var most atomic.Int32
	var began atomic.Int32
	oldWork := WhileServing
	WhileServing = append([]func(context.Context){func(ctx context.Context) {
		began.Add(1)
		n := working.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		<-ctx.Done()
		working.Add(-1)
	}}, oldWork...)
	t.Cleanup(func() { WhileServing = oldWork })

	t.Setenv("MAGPIE_ADDR", "127.0.0.1:0")
	old := New()
	stopOld, oldDone := listening(t, old)
	defer stopOld()
	waitFor(t, "the first gateway's work to begin", func() bool { return began.Load() == 1 })

	t.Setenv("MAGPIE_ADDR", "127.0.0.1:0")
	next := New()
	stopNext, nextDone := listening(t, next)
	defer stopNext()
	next.lnMu.Lock()
	addr := next.ln.Addr().String()
	next.lnMu.Unlock()
	// the new one answers while the old one serves
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("the second gateway answered %d while the first served", resp.StatusCode)
	}
	// many retries' worth: its work hasn't begun
	time.Sleep(30 * servingRetry)
	if n := began.Load(); n != 1 {
		t.Fatalf("the serving work began %d times with the first gateway still serving", n)
	}

	stopOld()
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second gateway's work to begin once the first ended", func() bool { return began.Load() == 2 })
	if m := most.Load(); m != 1 {
		t.Fatalf("the serving work ran in %d gateways at once", m)
	}
	stopNext()
	if err := <-nextDone; err != nil {
		t.Fatal(err)
	}
}

// A gateway that ends while it waits for the lock never does the work,
// and lets go of nothing it doesn't hold.
func TestServingWaitEndsWithItsGateway(t *testing.T) {
	fresh(t)
	oldRetry := servingRetry
	servingRetry = 10 * time.Millisecond
	t.Cleanup(func() { servingRetry = oldRetry })
	unlock, held := filelock.Lock(servingLockPath(), 0)
	if !held {
		t.Skip("no file locks here")
	}
	var began atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	release := holdServing(ctx, servingLockPath(), func() { began.Add(1) })
	time.Sleep(10 * servingRetry)
	cancel()
	release()
	unlock()
	time.Sleep(10 * servingRetry)
	if n := began.Load(); n != 0 {
		t.Fatalf("the work began %d times in a gateway that ended waiting", n)
	}
	// and the lock is free for the next
	again := holdServing(context.Background(), servingLockPath(), func() { began.Add(1) })
	defer again()
	if n := began.Load(); n != 1 {
		t.Fatalf("a free lock: the work began %d times, want at once", n)
	}
}

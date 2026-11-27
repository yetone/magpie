package provider

import (
	"context"
	"time"
)

// KeepNewListed asks for the lists not fetched yet when the gateway
// starts serving and every NewListedEvery after, for as long as it
// serves, so an account signed in from the CLI or another magpie after
// every list came is listed without a restart (#7945; kkkzbh's patch for
// #1805). FetchNew's own rules hold: a list fetched is not asked again,
// and one that failed waits newFetchRetry.
func KeepNewListed(ctx context.Context) {
	keepNewListed(ctx, NewListedEvery)
}

// NewListedEvery is how often KeepNewListed looks; a test shortens it.
var NewListedEvery = time.Minute

func keepNewListed(ctx context.Context, every time.Duration) {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if ctx.Err() != nil {
			return
		}
		// a page's fetch holding the lock does this round's work; waiting
		// on it would hold the gateway's stop behind it
		if newFetches.TryLock() {
			newRunning.Add(1)
			fetchNewHeld(ctx, 20*time.Second)
			newRunning.Add(-1)
			newFetches.Unlock()
		}
		t.Reset(every)
	}
}

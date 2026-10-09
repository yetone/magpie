package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A history write holds no waiter on a reading (#1358): caller B parks on
// the reading while it is still in flight, and while the note is held, one
// of the two callers comes back — the reading's done channel is closed
// before any note runs. A note that runs inside the reading's goroutine
// before that close holds both callers, which is what this test is for.
func TestHistoryWriteHoldsNoReadingWaiter(t *testing.T) {
	azureHome(t)
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": "solo@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-solo"}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "r-solo", "account_id": "acct-solo",
		},
	})

	// the vendor holds caller A's reading until B has parked on it
	var vendorHits atomic.Int32
	asked := make(chan struct{})
	vendorHold := make(chan struct{})
	var vendorReleaseOnce sync.Once
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if vendorHits.Add(1) == 1 {
			close(asked)
			<-vendorHold
		}
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": 10, "limit_window_seconds": 18000}}})
	}))
	t.Cleanup(fake.Close)
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	loginsMu.Lock()
	loginsSeenAt = time.Time{}
	loginsMu.Unlock()

	noteEntered := make(chan struct{})
	held := make(chan struct{})
	var writes atomic.Int32
	var heldReleaseOnce sync.Once
	oldWrite := noteHistoryWrite
	noteHistoryWrite = func(qs []SubscriptionQuota, now time.Time) {
		if writes.Add(1) == 1 { // only the first caller pays for the write
			close(noteEntered)
			<-held // the slow disk, held until the test lets go
		}
	}
	t.Cleanup(func() {
		noteHistoryWrite = oldWrite
		heldReleaseOnce.Do(func() { close(held) })
		vendorReleaseOnce.Do(func() { close(vendorHold) })
	})

	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		LoginUsage(context.Background(), "codex")
	}()
	select {
	case <-asked: // caller A's reading is in flight at the vendor
	case <-time.After(10 * time.Second):
		t.Fatal("the reading never reached the vendor")
	}

	doneB := make(chan struct{})
	go func() {
		defer close(doneB)
		LoginUsage(context.Background(), "codex")
	}()
	// B's goroutine only has two map operations before it parks on the
	// reading's done channel; the margin is for scheduling, not for the
	// vendor, which is still held.
	time.Sleep(250 * time.Millisecond)
	vendorReleaseOnce.Do(func() { close(vendorHold) })

	select {
	case <-noteEntered: // the note started and is stuck on the slow write
	case <-time.After(10 * time.Second):
		t.Fatal("history write never started")
	}
	select {
	case <-doneA: // the caller paying for the write came back
	case <-doneB: // the reading's waiter was not held by the write
	case <-time.After(3 * time.Second):
		t.Fatal("both callers held by one history write")
	}
	heldReleaseOnce.Do(func() { close(held) })
	for _, done := range []chan struct{}{doneA, doneB} {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("a caller did not return once the write was let go")
		}
	}
}

// A batch of cached readings notes nothing new: noting it again must not
// read and parse the history file (#1358).
func TestCachedBatchNoteSkipsTheRead(t *testing.T) {
	azureHome(t)
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      fakeJWT(map[string]any{"email": "solo@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-solo"}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "r-solo", "account_id": "acct-solo",
		},
	})
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": 10, "limit_window_seconds": 18000}}})
	}))
	t.Cleanup(fake.Close)
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	loginsMu.Lock()
	loginsSeenAt = time.Time{}
	loginsMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if got := LoginUsage(ctx, "codex"); len(got) == 0 {
		t.Fatal("LoginUsage read no account")
	}
	reads := 0
	oldRead := readQuotaHist
	readQuotaHist = func() quotaHist {
		reads++
		return oldRead()
	}
	t.Cleanup(func() { readQuotaHist = oldRead })
	// the same accounts, all answered from the minute's cache
	if got := LoginUsage(ctx, "codex"); len(got) == 0 {
		t.Fatal("cached LoginUsage read no account")
	}
	if reads != 0 {
		t.Fatalf("a cached batch read the history file %d times", reads)
	}
}

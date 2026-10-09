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

// A background reading of an account is a point of the account's quota
// history too, as a Usage-page reading is: a magpie only ever serving
// other magpies has nobody on its Usage page, so its routing read each
// account every few minutes while GET /v1/magpie/quotas/history stayed
// empty (#1313; yetone on #1317).
func TestBackgroundLoginReadNotesQuotaHistory(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// rememberLogins is memoized for 30s process-wide; another test may
	// have just run it, and then this account is never saved and read.
	loginsMu.Lock()
	loginsSeenAt = time.Time{}
	loginsMu.Unlock()

	// routing's read, no Usage page involved
	if got := LoginUsage(ctx, "codex"); len(got) == 0 {
		t.Fatal("LoginUsage read no account")
	}

	hs := QuotaHistories(time.Now().Add(-time.Hour), "codex", "solo@example.com")
	if len(hs) == 0 || len(hs[0].Lines) == 0 || len(hs[0].Lines[0].Points) == 0 {
		t.Fatalf("background read noted no history: %+v", hs)
	}
}

// A LoginUsage over several accounts notes the batch's history in one
// write, not one read/parse/rewrite of the file per account (#1318
// review).
func TestBackgroundReadsNoteHistoryOnce(t *testing.T) {
	home := signIn(t) // me@example.com, acct-1
	rememberLogins(true)
	codexSignIn(t, home, "work@example.com", "r-work")
	rememberLogins(true)
	var calls atomic.Int32
	var batch atomic.Int32
	oldWrite := noteHistoryWrite
	noteHistoryWrite = func(qs []SubscriptionQuota, now time.Time) {
		calls.Add(1)
		batch.Store(int32(len(qs)))
		noteQuotaHistory(qs, now)
	}
	t.Cleanup(func() { noteHistoryWrite = oldWrite })

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
			"primary_window": map[string]any{"used_percent": 10, "limit_window_seconds": 18000}}})
	}))
	t.Cleanup(fake.Close)
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if got := LoginUsage(ctx, "codex"); len(got) != 2 {
		t.Fatalf("LoginUsage read %d accounts, want 2", len(got))
	}
	if calls.Load() != 1 {
		t.Fatalf("history noted in %d writes, want 1", calls.Load())
	}
	if batch.Load() != 2 {
		t.Fatalf("history noted a batch of %d accounts, want 2", batch.Load())
	}
	for _, user := range []string{"me@example.com", "work@example.com"} {
		hs := QuotaHistories(time.Now().Add(-time.Hour), "codex", user)
		if len(hs) == 0 || len(hs[0].Lines) == 0 || len(hs[0].Lines[0].Points) == 0 {
			t.Fatalf("no history for %s: %+v", user, hs)
		}
	}
}

// A slow history write holds no routing waiter: the first LoginUsage
// pays for the batch's note after its readings have all completed, so a
// second caller on the same account answers while the note is still
// being written (#1318 review).
func TestHistoryWriteHoldsNoWaiter(t *testing.T) {
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

	release := make(chan struct{})
	entered := make(chan struct{})
	var writes atomic.Int32
	oldWrite := noteHistoryWrite
	noteHistoryWrite = func(qs []SubscriptionQuota, now time.Time) {
		if writes.Add(1) == 1 {
			close(entered)
			<-release // the slow disk, held until the test lets go
		}
	}
	var releaseOnce sync.Once
	t.Cleanup(func() {
		noteHistoryWrite = oldWrite
		releaseOnce.Do(func() { close(release) })
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		LoginUsage(context.Background(), "codex")
	}()
	select {
	case <-entered: // the note started and is stuck on the slow write
	case <-time.After(10 * time.Second):
		t.Fatal("history write never started")
	}
	select {
	case <-done:
		t.Fatal("LoginUsage returned before the held write finished")
	case <-time.After(200 * time.Millisecond):
	}
	// The first caller is still paying for the write, but its readings
	// completed and released their in-flight markers before the note
	// began, so a second caller on the same account answers from them
	// instead of waiting on the write.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if got := LoginUsage(ctx, "codex"); len(got) == 0 {
		t.Fatal("second LoginUsage held by the history write")
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("LoginUsage did not return once the write was let go")
	}
}

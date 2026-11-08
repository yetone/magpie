package provider

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// An allowance read Allowances leaves running past firstWait ends with the
// test that started it: left to run on, it read Claude Code's sign-in in the
// next test's home before that test wrote it, cached that there was none,
// and TestClaudeAccountServesModelsListedSinceItsFetch found no Claude
// account (#3174).
func TestReadLeftOutEndsWithItsTest(t *testing.T) {
	var ended atomic.Bool
	// put back by the outer test: LoginUsageVia waits for the stand-in it
	// replaces to return, which would wait the read out for the subtest
	t.Cleanup(func() { LoginUsageVia(nil) })
	t.Run("leaves a read out", func(t *testing.T) {
		claudeHome(t)
		usedCache.Lock()
		usedCache.m, usedCache.at, usedCache.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
		usedCache.Unlock()
		old := firstWait
		firstWait = time.Millisecond
		t.Cleanup(func() { firstWait = old })
		release := make(chan struct{})
		LoginUsageVia(func(context.Context, string) map[string]SubscriptionQuota {
			<-release
			// what the read does once let go takes time a test that
			// doesn't wait for it is long gone in
			time.Sleep(200 * time.Millisecond)
			claudeCredential()
			ended.Store(true)
			return nil
		})
		// run before isolate's: the read is let go as the test ends
		t.Cleanup(func() { close(release) })
		Allowances("claude")
		if ended.Load() {
			t.Fatal("the read ended before the test did: nothing was left out")
		}
	})
	if !ended.Load() {
		t.Fatal("the allowance read Allowances left out ran on after its test")
	}
}

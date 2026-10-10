package agent

import (
	"context"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// codexUsedUpReal is codexUsedUp as the sync runs it: TestMain stands in for
// it (a test must never ask OpenAI whether the account is out), and a package
// variable is initialized before TestMain runs, so this keeps the real one
// for the test below to check.
var codexUsedUpReal = codexUsedUp

// The catalog sync runs inside a write's own request: a routing group's Save
// answers only after agent.SyncCatalog has walked every agent, codex's among
// them. So codex's sync asks for what is known of the account already, never
// for a reading — asking waits on the vendor, and a save waited 1.1s of one
// (the miss of the minute's cache). KeepOnAnAccountWithRoom makes the
// reading, on its own loop, and noteCodexUsedUp tells the catalog when the
// answer changes.
func TestCodexSyncAsksNoReadingOfTheVendor(t *testing.T) {
	asked := 0
	provider.LoginUsageVia(func(ctx context.Context, agent string) map[string]provider.SubscriptionQuota {
		asked++
		return map[string]provider.SubscriptionQuota{}
	})
	t.Cleanup(func() { provider.LoginUsageVia(nil) })
	if codexUsedUpReal() {
		t.Fatal("the sync's reading says held with no reading made")
	}
	if asked != 0 {
		t.Fatalf("the sync asked for a reading %d times, want none", asked)
	}
}

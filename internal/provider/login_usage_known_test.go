package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// CodexUsedUpKnown is what the catalog sync asks (agent.codexUsedUp), and it
// asks the vendor nothing. SyncCatalog runs inside a write's own request — a
// routing group's Save answers only once every agent has synced — and a
// reading there is one the vendor answers over the network: a save waited
// 1.1s of the reading of the account by the miss of the minute's cache.
// KeepOnAnAccountWithRoom makes the reading, on a loop that has the time for
// it, and noteCodexUsedUp tells the catalog when the answer changes. What a
// reading has already is answered here all the same.
func TestCodexUsedUpKnownAsksTheVendorNothing(t *testing.T) {
	signIn(t)
	rememberLogins(true)
	body := heldUsage(t)
	var hits atomic.Int64
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		json.NewEncoder(w).Encode(body)
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	loginUsageCache.Lock()
	loginUsageCache.m, loginUsageCache.pending = nil, nil
	loginUsageCache.Unlock()

	// nothing read yet: the account is not known to be held, and no one is
	// asked — CodexUsedUp's own answer for one it can't tell about
	if CodexUsedUpKnown() {
		t.Fatal("CodexUsedUpKnown = true with nothing read")
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("CodexUsedUpKnown asked the vendor %d times, want none", n)
	}
	// the reading the loop makes: it asks the vendor, and the account is held
	if !CodexUsedUp(context.Background()) {
		t.Fatal("CodexUsedUp = false on a held reading")
	}
	if hits.Load() == 0 {
		t.Fatal("CodexUsedUp asked the vendor nothing")
	}
	// and known answers with that reading, asking no one again
	before := hits.Load()
	if !CodexUsedUpKnown() {
		t.Fatal("CodexUsedUpKnown = false on the reading just made")
	}
	if n := hits.Load(); n != before {
		t.Fatalf("CodexUsedUpKnown asked the vendor %d more times, want none", n-before)
	}
}

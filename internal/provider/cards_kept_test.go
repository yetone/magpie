package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// cardOf is the card cs has of provider and user, nil when none.
func cardOf(cs []SubscriptionQuota, provider, user string) *SubscriptionQuota {
	for i := range cs {
		if cs[i].Provider == provider && cs[i].User == user {
			return &cs[i]
		}
	}
	return nil
}

// #1313 (jorben): a magpie serving only other magpies has nobody on its
// Usage page, while its Codex accounts' usage is read all the time behind
// the requests (the account switching, CodexUsedUp). What another magpie
// is told of its cards (CachedCards) is that reading, not "nothing read";
// after a restart, before anything is read again, it is the reading kept
// on disk, dated. Telling it asks nobody.
func TestCachedCardsTellBackgroundReadings(t *testing.T) {
	signIn(t) // Codex on me@example.com
	rememberLogins(true)
	var asked atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		asked.Add(1)
		w.Write([]byte(`{"plan_type":"pro","rate_limit":{"allowed":true,"limit_reached":false,
			"primary_window":{"used_percent":37,"limit_window_seconds":18000,"reset_after_seconds":3600},
			"secondary_window":{"used_percent":12,"limit_window_seconds":604800,"reset_after_seconds":86400}}}`))
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	forget := func() {
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
		subscriptionUsageCache.Lock()
		subscriptionUsageCache.data, subscriptionUsageCache.at = nil, time.Time{}
		subscriptionUsageCache.Unlock()
		ForgetKeptCardsForTest()
	}
	forget()
	t.Cleanup(forget)

	if q := cardOf(CachedCards(time.Now()), "codex", "me@example.com"); q != nil {
		t.Fatalf("a card before anything was read: %+v", q)
	}
	if CodexUsedUp(context.Background()) {
		t.Fatal("held at 37%")
	}
	used := func(q *SubscriptionQuota) float64 {
		if q == nil || len(q.Windows) == 0 {
			return -1
		}
		return q.Windows[0].Used
	}
	q := cardOf(CachedCards(time.Now()), "codex", "me@example.com")
	if used(q) != 37 || q.Name != "Codex" || q.Kind != "subscription" || q.AsOf != nil || q.Error != "" {
		t.Fatalf("after the background read: %+v", q)
	}
	// restarted: nothing read yet, the reading kept on disk, dated
	loginUsageCache.Lock()
	loginUsageCache.m = nil
	loginUsageCache.Unlock()
	ForgetKeptCardsForTest()
	q = cardOf(CachedCards(time.Now()), "codex", "me@example.com")
	if used(q) != 37 || q.AsOf == nil || q.Name != "Codex" {
		t.Fatalf("after a restart: %+v", q)
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("ChatGPT asked %d times, want 1 (the background read)", n)
	}
}

// The page's card stands when it was read after the background's reading.
func TestCachedCardsKeepALaterPageRead(t *testing.T) {
	signIn(t)
	rememberLogins(true)
	t.Cleanup(func() {
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
		subscriptionUsageCache.Lock()
		subscriptionUsageCache.data = nil
		subscriptionUsageCache.Unlock()
		ForgetKeptCardsForTest()
	})
	loginUsageCache.Lock()
	loginUsageCache.m = map[string]loginUsageEntry{"codex/me@example.com": {at: time.Now(),
		read: SubscriptionQuota{Provider: "codex", User: "me@example.com", Windows: []QuotaWindow{{Name: "5h", Used: 10}}, readSeq: 5}}}
	loginUsageCache.Unlock()
	subscriptionUsageCache.Lock()
	subscriptionUsageCache.data = []SubscriptionQuota{{Provider: "codex", Name: "Codex", User: "me@example.com",
		Windows: []QuotaWindow{{Name: "5h", Used: 20}}, readSeq: 6}}
	subscriptionUsageCache.Unlock()
	if q := cardOf(CachedCards(time.Now()), "codex", "me@example.com"); q == nil || q.Windows[0].Used != 20 {
		t.Fatalf("the later page read: %+v", q)
	}
	subscriptionUsageCache.Lock()
	subscriptionUsageCache.data[0].readSeq = 4
	subscriptionUsageCache.Unlock()
	if q := cardOf(CachedCards(time.Now()), "codex", "me@example.com"); q == nil || q.Windows[0].Used != 10 || q.Name != "Codex" {
		t.Fatalf("the later background read: %+v", q)
	}
}

// The sibling for keys: routing reads a sub2api key's windows behind the
// requests (KeyAllowance); another magpie is told them on the key's card,
// one made for it when the Usage page never read it.
func TestCachedCardsTellKeyReadings(t *testing.T) {
	keyLimitsHome(t)
	week := time.Now().Add(72 * time.Hour).Truncate(time.Second)
	srv, asked := sub2apiServer(t, func() ([]byte, int) { return weekUsed(t, "720", week), 200 })
	p := sub2apiKey(t, srv)
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if q := cardOf(CachedCards(time.Now()), p.ID, ""); q != nil {
		t.Fatalf("a card before anything was read: %+v", q)
	}
	waitAllowance(t, p, func(Allowance) bool { return true })
	q := cardOf(CachedCards(time.Now()), p.ID, "")
	if q == nil || q.Kind != "balance" || len(q.Windows) != 2 || q.Windows[1].Name != "7 days" || q.Windows[1].Used != 90 || q.Name != "Sub2API" || q.ReadAt == nil {
		t.Fatalf("after routing's read: %+v", q)
	}
	if n := asked.Load(); n != 1 {
		t.Fatalf("sub2api asked %d times, want 1", n)
	}
}

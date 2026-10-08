package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePeer is another magpie's gateway as RemoteCards asks it: its kept
// cards at RemoteCardsPath, a card read again at RemoteRefreshPath.
type fakePeer struct {
	sync.Mutex
	cards     []SubscriptionQuota
	status    int
	gets      int
	refreshes []string // provider|user of each refresh asked
}

func (f *fakePeer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Lock()
	defer f.Unlock()
	if r.Header.Get("Authorization") != "Bearer sk-magpie-office" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET " + RemoteCardsPath:
		f.gets++
	case "POST " + RemoteRefreshPath:
		f.refreshes = append(f.refreshes, r.URL.Query().Get("provider")+"|"+r.URL.Query().Get("user"))
	case "GET /v1/magpie/quotas/history":
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []QuotaHistory{{Provider: "codex", User: "a@x.com", Lines: []QuotaLine{{Name: "5h"}}}}})
		return
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": f.cards})
}

func remoteQuotaHome(t *testing.T, peer http.Handler) {
	t.Helper()
	azureHome(t)
	srv := httptest.NewServer(peer)
	t.Cleanup(srv.Close)
	if err := Save(Provider{ID: "office", Name: "Office", Preset: RemoteMagpiePreset, Key: "sk-magpie-office", Chat: srv.URL}); err != nil {
		t.Fatal(err)
	}
	ForgetRemoteCardsForTest()
	t.Cleanup(ForgetRemoteCardsForTest)
}

// 莫 on Discord: a computer whose providers are another's magpie showed
// no quota at all. It now shows the other magpie's cards, named with it
// and kept apart from its own by id, in their kinds; what a card's
// buttons would do to this computer's accounts is left off, and a card
// the other magpie has from a third isn't passed on.
func TestRemoteMagpieCards(t *testing.T) {
	peer := &fakePeer{cards: []SubscriptionQuota{
		{Provider: "codex", Name: "Codex", Icon: "codex", User: "a@x.com", Kind: "subscription", Windows: []QuotaWindow{{Name: "5h", Used: 40}},
			Resets: &ResetCredits{Count: 1}, Checkins: true},
		{Provider: "zhipu", Name: "GLM Coding", Kind: "plan", Windows: []QuotaWindow{{Name: "5h", Used: 10}}},
		{Provider: "deepseek", Name: "DeepSeek", Kind: "balance", Balance: "¥12.00"},
		{Provider: "home/codex", Name: "Codex · Home", Kind: "subscription", From: "Home"},
	}}
	remoteQuotaHome(t, peer)
	ctx := context.Background()
	subs, plans, balances := quotas(ctx)
	find := func(qs []SubscriptionQuota, id string) *SubscriptionQuota {
		for i := range qs {
			if qs[i].Provider == id {
				return &qs[i]
			}
		}
		return nil
	}
	c := find(subs, "office/codex")
	if c == nil || c.Name != "Codex · Office" || c.From != "Office" || c.User != "a@x.com" || c.Icon != "codex" || len(c.Windows) != 1 || c.Windows[0].Used != 40 {
		t.Fatalf("subscription card: %+v", c)
	}
	if c.Resets != nil || c.Checkins {
		t.Errorf("a remote card's buttons are kept: %+v", c)
	}
	if p := find(plans, "office/zhipu"); p == nil || p.Name != "GLM Coding · Office" {
		t.Errorf("plan: %+v", plans)
	}
	if b := find(balances, "office/deepseek"); b == nil || b.Balance != "¥12.00" {
		t.Errorf("balance: %+v", balances)
	}
	for _, qs := range [][]SubscriptionQuota{subs, plans, balances} {
		for _, q := range qs {
			if strings.Contains(q.Provider, "home") {
				t.Errorf("a third magpie's card passed on: %+v", q)
			}
		}
	}
	// the menu bar and alerts ask often; the other magpie is asked once in
	// remoteCardsFor
	Quotas(ctx)
	RemoteCards(ctx)
	if peer.gets != 1 {
		t.Errorf("asked %d times, want 1", peer.gets)
	}
	// magpie quota names it too
	var found bool
	for _, q := range QuotaReport(ctx, time.Now()) {
		if q.Provider == "office/codex" {
			found = q.From == "Office" && q.Kind == "subscription"
		}
	}
	if !found {
		t.Error("QuotaReport has no office/codex from Office")
	}
}

// A remote card's refresh asks the other magpie to read that card again,
// and then what it has now; the remote's own card (nothing read there
// yet) asks it to read every card.
func TestRemoteMagpieCardRefresh(t *testing.T) {
	peer := &fakePeer{}
	remoteQuotaHome(t, peer)
	ctx := context.Background()
	cs := RemoteCards(ctx)
	if len(cs) != 1 || cs[0].Provider != "office" || cs[0].Error != errRemoteNothingRead || cs[0].Icon != "magpie" {
		t.Fatalf("nothing read there: %+v", cs)
	}
	peer.Lock()
	peer.cards = []SubscriptionQuota{{Provider: "codex", Name: "Codex", User: "a@x.com", Windows: []QuotaWindow{{Name: "5h", Used: 70}}}}
	peer.Unlock()
	RefreshUsage(ctx, "office", "")
	RefreshUsage(ctx, "office/codex", "a@x.com")
	if got := strings.Join(peer.refreshes, ","); got != "|,codex|a@x.com" {
		t.Errorf("refreshes asked: %q", got)
	}
	cs = RemoteCards(ctx)
	if len(cs) != 1 || cs[0].Provider != "office/codex" || cs[0].Windows[0].Used != 70 {
		t.Errorf("after refresh: %+v", cs)
	}
	if peer.gets != 3 {
		t.Errorf("cards asked %d times, want 3 (once, then after each refresh)", peer.gets)
	}
	// a card of this computer's own isn't sent there
	RefreshUsage(ctx, "codex", "")
	if len(peer.refreshes) != 2 {
		t.Errorf("a local refresh went to the remote: %v", peer.refreshes)
	}
}

// A remote that can't be asked keeps its last cards, as old as they are;
// with none, its card says why, and a magpie too old to share its quotas
// is told to be updated.
func TestRemoteMagpieUnreachable(t *testing.T) {
	peer := &fakePeer{cards: []SubscriptionQuota{{Provider: "codex", Name: "Codex", Windows: []QuotaWindow{{Name: "5h", Used: 5}}}}}
	remoteQuotaHome(t, peer)
	ctx := context.Background()
	RemoteCards(ctx)
	peer.Lock()
	peer.status = http.StatusBadGateway
	peer.Unlock()
	if cs := remoteCards(ctx, remoteMagpies()[0], true); len(cs) != 1 || cs[0].Provider != "office/codex" || cs[0].Error != "" {
		t.Errorf("last cards not kept: %+v", cs)
	}
	ForgetRemoteCardsForTest()
	peer.Lock()
	peer.status = http.StatusNotFound
	peer.Unlock()
	if cs := RemoteCards(ctx); len(cs) != 1 || cs[0].Error != errRemoteNoShare {
		t.Errorf("an old magpie: %+v", cs)
	}
}

// The cards told to another magpie are this one's as last read: nothing
// is read for it, however old they are, and another magpie's aren't
// passed on.
func TestCachedCardsAskNobody(t *testing.T) {
	azureHome(t)
	c := &subscriptionUsageCache
	c.Lock()
	keep, keepAt := c.data, c.at
	c.data = []SubscriptionQuota{{Provider: "codex", Name: "Codex", User: "a@x.com", Windows: []QuotaWindow{{Name: "5h", Used: 1}}}}
	c.at = time.Now().Add(-time.Hour)
	c.Unlock()
	t.Cleanup(func() {
		c.Lock()
		c.data, c.at = keep, keepAt
		c.Unlock()
	})
	cs := CachedCards(time.Now())
	if len(cs) != 1 || cs[0].Provider != "codex" || cs[0].Kind != "subscription" {
		t.Errorf("cards: %+v", cs)
	}
	if SubscriptionUsageReading() {
		t.Error("telling the cards started a read")
	}
	c.Lock()
	at := c.at
	c.Unlock()
	if time.Since(at) < 30*time.Minute {
		t.Error("the cache was read again")
	}
}

// A remote magpie's history comes with its cards' ids.
func TestRemoteQuotaHistories(t *testing.T) {
	remoteQuotaHome(t, &fakePeer{})
	hs := RemoteQuotaHistories(context.Background(), "7")
	if len(hs) != 1 || hs[0].Provider != "office/codex" || hs[0].User != "a@x.com" {
		t.Errorf("%+v", hs)
	}
}

// A background usage read (CodexUsedUp, the routing loop's) lands on a
// remote magpie's cards too: only the Usage page's reads fed the cards'
// cache, so a remote saw nothing until someone opened the page or pressed
// refresh (#1313, jorben's repro).
func TestCachedCardsAfterBackgroundRead(t *testing.T) {
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
	_ = CodexUsedUp(ctx)
	if cs := CachedCards(time.Now()); len(cs) == 0 {
		t.Error("CachedCards empty after a background read")
	}
}

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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
	// limits are RemoteKeyLimitPath's answers by the key asking (#1547);
	// nil is a magpie too old to answer it. limitAsks counts them.
	limits    map[string]string
	limitAsks int
	// held is a remote that holds the key to a budget, models or
	// accounts (#1981): it answers everything but the key's own limit
	// with 403 gateway_key_held.
	held bool
}

// peerCards are the cards the remote has, without its keys' own.
func peerCards(cs []SubscriptionQuota) []SubscriptionQuota {
	var out []SubscriptionQuota
	for _, q := range cs {
		if q.Provider != "office/"+remoteKeyCard {
			out = append(out, q)
		}
	}
	return out
}

func (f *fakePeer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Lock()
	defer f.Unlock()
	if r.URL.Path == RemoteKeyLimitPath && f.status == 0 {
		f.limitAsks++
		if f.limits == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, ok := f.limits[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(body))
		return
	}
	if r.Header.Get("Authorization") != "Bearer sk-magpie-office" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	if f.held {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"message":"held","type":"permission_error","code":"` + KeyHeldCode + `"}}`))
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
		{Provider: "zhipu", Name: "GLM Coding", Kind: "plan", Windows: []QuotaWindow{{Name: "5h", Used: 10}},
			Resets: &ResetCredits{Count: 2, ByWindow: true}},
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
	// a Codex reset is used there when pressed here (#3850); a check-in
	// isn't asked of another computer
	if c.Checkins || c.Resets == nil || c.Resets.Count != 1 {
		t.Errorf("a remote Codex card's buttons: %+v", c)
	}
	if p := find(plans, "office/zhipu"); p == nil || p.Name != "GLM Coding · Office" || p.Resets != nil {
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
	cs := peerCards(RemoteCards(ctx))
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
	cs = peerCards(RemoteCards(ctx))
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
	if cs := peerCards(RemoteCards(ctx)); len(cs) != 1 || cs[0].Error != errRemoteNoShare {
		t.Errorf("an old magpie: %+v", cs)
	}
	// one that serves models but keeps its quotas, or refuses the key,
	// says which, so its card can say what to do there
	for status, want := range map[int]string{http.StatusForbidden: errRemoteNotShared, http.StatusUnauthorized: errRemoteKey} {
		ForgetRemoteCardsForTest()
		peer.Lock()
		peer.status = status
		peer.Unlock()
		if cs := peerCards(RemoteCards(ctx)); len(cs) != 1 || cs[0].Error != want {
			t.Errorf("%d: %+v", status, cs)
		}
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

// The status line's report (#1997) is the cards as last read, as Quota:
// nothing is read for it, however old they are.
func TestKeptQuotaReportAsksNobody(t *testing.T) {
	azureHome(t)
	c := &subscriptionUsageCache
	c.Lock()
	keep, keepAt := c.data, c.at
	c.data = []SubscriptionQuota{{Provider: "codex", Name: "Codex", User: "a@x.com", Windows: []QuotaWindow{{Name: "5 hours", Used: 62, ResetSecs: 3600}}}}
	c.at = time.Now().Add(-time.Hour)
	c.Unlock()
	t.Cleanup(func() {
		c.Lock()
		c.data, c.at = keep, keepAt
		c.Unlock()
	})
	qs := KeptQuotaReport(time.Now())
	if len(qs) != 1 || qs[0].Provider != "codex" || qs[0].Kind != "subscription" || len(qs[0].Windows) != 1 || qs[0].Windows[0].Used != 62 || qs[0].Windows[0].ResetsAt == nil {
		t.Fatalf("report: %+v", qs)
	}
	if SubscriptionUsageReading() {
		t.Error("the report started a read")
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

// keyCards are the remote's keys' own cards (#1547), by the name each
// card goes by.
func keyCards(cs []SubscriptionQuota) map[string]SubscriptionQuota {
	out := map[string]SubscriptionQuota{}
	for _, q := range cs {
		if q.Provider == "office/"+remoteKeyCard {
			out[q.User] = q
		}
	}
	return out
}

// #1547 (PennyVibe): a computer that uses another magpie with gateway
// keys it gave saw that magpie's subscriptions, but not what each key may
// still spend there. Each key now has a card of its own, asked with that
// key alone: its caps, what it used and has left, when it resets; a key
// held to no limit says so; a magpie too old to tell says that, and a
// failed read is never shown as nothing used.
func TestRemoteMagpieKeyLimits(t *testing.T) {
	reset := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	peer := &fakePeer{limits: map[string]string{
		"sk-magpie-office": `{"limited":true,"key":"laptop","limit":{"period":"day","start":"2026-10-11T00:00:00Z","reset":"2026-10-12T00:00:00Z","calls":3,"tokens":1050,"cost":0.4567,"tokenLimit":10000,"costLimit":2,"tokensLeft":8950,"costLeft":1.5433}}`,
		"sk-magpie-spare":  `{"limited":false,"key":"spare"}`,
	}}
	remoteQuotaHome(t, peer)
	p, err := Find("office")
	if err != nil {
		t.Fatal(err)
	}
	p.KeyName = "laptop"
	p.Keys = []KeyAccount{{Name: "spare", Key: "sk-magpie-spare"}}
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	ForgetRemoteCardsForTest()
	ctx := context.Background()
	ks := keyCards(RemoteCards(ctx))
	if len(ks) != 2 {
		t.Fatalf("key cards: %+v", ks)
	}
	l := ks["laptop"]
	if l.Name != "Office" || l.From != "Office" || l.Kind != "plan" || l.Error != "" || l.GatewayKey == nil || !l.GatewayKey.Limited || l.GatewayKey.Period != "day" ||
		l.GatewayKey.TokensLeft == nil || *l.GatewayKey.TokensLeft != 8950 || l.GatewayKey.CostLeft == nil || *l.GatewayKey.CostLeft != 1.5433 || l.GatewayKey.Calls != 3 {
		t.Fatalf("laptop: %+v %+v", l, l.GatewayKey)
	}
	if len(l.Windows) != 2 || l.Windows[0].Name != "Tokens today" || l.Windows[0].Amount != 1050 || l.Windows[0].Limit != 10000 || l.Windows[0].Used != 10.5 ||
		l.Windows[0].ResetsAt == nil || !l.Windows[0].ResetsAt.Equal(reset) ||
		l.Windows[1].Name != "Spend today" || l.Windows[1].Amount != 0.46 || l.Windows[1].Limit != 2 || l.Windows[1].Unit != "USD" {
		t.Errorf("laptop windows: %+v", l.Windows)
	}
	s := ks["spare"]
	if s.Error != "" || s.GatewayKey == nil || s.GatewayKey.Limited || len(s.Windows) != 1 || !s.Windows[0].Unlimited {
		t.Errorf("a key with no limit: %+v", s)
	}
	// kept like the remote's cards: asked once in remoteCardsFor
	RemoteCards(ctx)
	if peer.limitAsks != 2 {
		t.Errorf("asked %d times, want 2 (once per key)", peer.limitAsks)
	}
	// its refresh reads again the key named
	peer.Lock()
	peer.limits["sk-magpie-office"] = `{"limited":true,"key":"laptop","limit":{"period":"day","reset":"2026-10-12T00:00:00Z","tokens":2000,"tokenLimit":10000,"tokensLeft":8000}}`
	peer.Unlock()
	RefreshUsage(ctx, "office/"+remoteKeyCard, "laptop")
	if peer.limitAsks != 3 {
		t.Errorf("refresh asked %d times in all, want 3", peer.limitAsks)
	}
	if l := keyCards(RemoteCards(ctx))["laptop"]; len(l.Windows) != 1 || l.Windows[0].Amount != 2000 || l.GatewayKey == nil || *l.GatewayKey.TokensLeft != 8000 {
		t.Errorf("after refresh: %+v", l)
	}
	// a read that fails keeps the last numbers, dated
	peer.Lock()
	peer.status = http.StatusBadGateway
	peer.Unlock()
	RefreshUsage(ctx, "office/"+remoteKeyCard, "")
	ks = keyCards(RemoteCards(ctx))
	if l := ks["laptop"]; l.AsOf == nil || l.Error != "" || len(l.Windows) != 1 || l.Windows[0].Amount != 2000 {
		t.Errorf("a failed read lost the numbers: %+v", l)
	}
	// with nothing read, the card says why: never "no limit" or zeros
	ForgetRemoteCardsForTest()
	ks = keyCards(RemoteCards(ctx))
	for _, n := range []string{"laptop", "spare"} {
		if q := ks[n]; q.Error == "" || q.GatewayKey != nil || len(q.Windows) != 0 {
			t.Errorf("%s unread: %+v", n, q)
		}
	}
	// a magpie too old to tell a key its limit
	ForgetRemoteCardsForTest()
	peer.Lock()
	peer.status, peer.limits = 0, nil
	peer.Unlock()
	ks = keyCards(RemoteCards(ctx))
	if len(ks) != 2 {
		t.Errorf("old magpie: %+v", ks)
	}
	for n, q := range ks {
		if q.Error != errRemoteNoKeyLimit || q.GatewayKey != nil {
			t.Errorf("%s on an old magpie: %+v", n, q)
		}
	}
}

// Two keys under one name each show their own, told apart by the key.
func TestRemoteKeyNames(t *testing.T) {
	names, _ := remoteKeys(Provider{KeyName: "a", Key: "sk-magpie-1111111111", Keys: []KeyAccount{{Name: "a", Key: "sk-magpie-2222222222"}, {Key: "sk-magpie-3333333333"}, {Name: "off", Key: "x", Off: true}}})
	if len(names) != 3 || names[0] == names[1] || !strings.HasPrefix(names[0], "a · ") || names[2] != Mask("sk-magpie-3333333333") {
		t.Errorf("%q", names)
	}
}

// #1981: a remote magpie that holds this computer's key to a budget,
// models or accounts tells it none of its accounts. The cards read before
// the key was held go, and only the key's own card stays; a 403 for that
// reason doesn't say the remote isn't shared.
func TestRemoteMagpieHeldKeyShowsOnlyItsOwnCard(t *testing.T) {
	peer := &fakePeer{
		cards:  []SubscriptionQuota{{Provider: "codex", Name: "Codex", User: "boss@x.com", Kind: "subscription", Windows: []QuotaWindow{{Name: "5h", Used: 40}}}},
		limits: map[string]string{"sk-magpie-office": `{"limited":true,"key":"laptop","limit":{"period":"day","reset":"2026-10-12T00:00:00Z","tokens":10,"tokenLimit":100,"tokensLeft":90}}`},
	}
	remoteQuotaHome(t, peer)
	ctx := context.Background()
	if cs := peerCards(RemoteCards(ctx)); len(cs) != 1 || cs[0].User != "boss@x.com" {
		t.Fatalf("before the key is held: %+v", cs)
	}
	peer.Lock()
	peer.held = true
	peer.Unlock()
	p, err := Find("office")
	if err != nil {
		t.Fatal(err)
	}
	remoteCards(ctx, *p, true)
	all := RemoteCards(ctx)
	if cs := peerCards(all); len(cs) != 0 {
		t.Errorf("a held key still shows the remote's accounts: %+v", cs)
	}
	for _, q := range all {
		if strings.Contains(q.Error, "isn't shared") || strings.Contains(q.User, "boss") {
			t.Errorf("held card: %+v", q)
		}
	}
	if ks := keyCards(all); len(ks) != 1 {
		t.Errorf("the key's own card: %+v", ks)
	} else if k := ks[Mask(p.Key)]; k.GatewayKey == nil || !k.GatewayKey.Limited {
		t.Errorf("the key's own card: %+v", ks)
	}
	// a read asked before the held one came back after it: not kept
	keepRemoteCards(remoteCardsID(*p), time.Now().Add(-time.Hour), peer.cards, nil)
	if cs := peerCards(RemoteCards(ctx)); len(cs) != 0 {
		t.Errorf("a slow earlier read brought the cards back: %+v", cs)
	}
}

// #1981: cards are kept by provider and key, so a key changed to another
// doesn't show what the first key was told.
func TestRemoteMagpieCardsKeptByKey(t *testing.T) {
	peer := &fakePeer{cards: []SubscriptionQuota{{Provider: "codex", Name: "Codex", User: "boss@x.com", Kind: "subscription"}}}
	remoteQuotaHome(t, peer)
	ctx := context.Background()
	if cs := peerCards(RemoteCards(ctx)); len(cs) != 1 {
		t.Fatalf("first key: %+v", cs)
	}
	p, err := Find("office")
	if err != nil {
		t.Fatal(err)
	}
	p.Key = "sk-magpie-other"
	if err := Save(*p); err != nil {
		t.Fatal(err)
	}
	for _, q := range RemoteCards(ctx) {
		if q.User == "boss@x.com" {
			t.Errorf("another key shows the first key's card: %+v", q)
		}
	}
}

// #3850: a reset asked of a remote magpie that is too old to spend one,
// or that doesn't answer in time, says what it means for the card: update
// magpie there, or the reset may have been used there, so read the card
// before asking again (never asked again by itself).
func TestRemoteCodexResetAnswers(t *testing.T) {
	var asks atomic.Int32
	stall := make(chan struct{})
	t.Cleanup(func() { close(stall) })
	var mode atomic.Value
	mode.Store("old")
	remoteQuotaHome(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != RemoteCodexResetPath {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		asks.Add(1)
		if r.Method != http.MethodPost || r.URL.Query().Get("user") != "a@x.com" || r.URL.Query().Get("credit") != "c-2" || r.Header.Get("Authorization") != "Bearer sk-magpie-office" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch mode.Load() {
		case "old":
			w.WriteHeader(http.StatusNotFound)
		case "stall":
			select {
			case <-stall:
			case <-r.Context().Done():
			}
		case "readonly":
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"message":"read-only","code":"` + KeyReadOnlyCode + `"}}`))
		case "ok":
			w.Write([]byte(`{"code":"reset","windows":2}`))
		}
	}))
	ctx := context.Background()
	if _, err := UseRemoteCodexReset(ctx, "office/codex", "a@x.com", "c-2"); err == nil || !strings.HasPrefix(err.Error(), "remote magpie doesn't share its quotas") {
		t.Fatalf("an old remote: %v", err)
	}
	mode.Store("readonly")
	if _, err := UseRemoteCodexReset(ctx, "office/codex", "a@x.com", "c-2"); err == nil || !strings.HasPrefix(err.Error(), "this key is read-only on the remote magpie") {
		t.Fatalf("a read-only key: %v", err)
	}
	mode.Store("stall")
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	_, err := UseRemoteCodexReset(short, "office/codex", "a@x.com", "c-2")
	cancel()
	if err == nil || !strings.Contains(err.Error(), "may have been") {
		t.Fatalf("no answer in time: %v", err)
	}
	mode.Store("ok")
	out, err := UseRemoteCodexReset(ctx, "office/codex", "a@x.com", "c-2")
	if err != nil || out.Code != "reset" || out.Windows != 2 {
		t.Fatalf("spent: %+v %v", out, err)
	}
	if n := asks.Load(); n != 4 {
		t.Fatalf("asked %d times, want once each", n)
	}
	for _, id := range []string{"office/zhipu", "office", "codex", "nowhere/codex"} {
		if _, err := UseRemoteCodexReset(ctx, id, "a@x.com", ""); err == nil {
			t.Errorf("%s was asked", id)
		}
	}
	if n := asks.Load(); n != 4 {
		t.Fatalf("a card not a remote's Codex card asked the remote (%d)", n)
	}
}

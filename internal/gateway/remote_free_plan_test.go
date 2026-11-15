package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #6708 (zzh11223311): Free quota by expiry told a free plan by the
// account's own Plan, which a Remote magpie member has none of, so a
// remote signed in to a free plan was weighed as a paid one. Its card's
// plan tells it now: the free one goes first, though the paid one has
// more left and renews sooner, and the trace says it is free.
func TestRemoteMagpieFreePlanSpentFirst(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	provider.ForgetRemoteCardsForTest()
	t.Cleanup(provider.ForgetRemoteCardsForTest)
	now := time.Now()
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	remote := func(key, plan string, week float64, renews time.Duration) *httptest.Server {
		cards := []provider.SubscriptionQuota{{Provider: "claude", Name: "Claude Code", User: key + "@x.com", Plan: plan, Kind: "subscription", Windows: []provider.QuotaWindow{
			{Name: "7 days", Used: week, ResetsAt: at(renews), Span: 7 * 24 * time.Hour},
		}}}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("x-api-key") != key && r.Header.Get("Authorization") != "Bearer "+key {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Method+" "+r.URL.Path != "GET "+provider.RemoteCardsPath {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": provider.ToldRouting(cards)})
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	paid := remote("sk-magpie-paid", "Pro", 20, 30*time.Hour)
	free := remote("sk-magpie-free", "Free", 60, 100*time.Hour)
	for _, p := range []provider.Provider{
		{ID: "mac-paid", Name: "Mac Paid", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-paid", Chat: paid.URL},
		{ID: "mac-free", Name: "Mac Free", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-free", Chat: free.URL},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	provider.RemoteCards(context.Background())
	member := func(id string) candidate {
		p, err := provider.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		return candidate{p: *p, model: "claude/claude-sonnet-5-5", rest: id + "-free"}
	}
	a, b := member("mac-paid"), member("mac-free")
	g := provider.Provider{ID: "auto-free", Routing: provider.FreeExpiring}
	got, wg := weigh(g, []candidate{a, b}, "claude-sonnet-5-5", provider.Anthropic)
	if got[0].p.ID != "mac-free" {
		t.Errorf("%s first, want the Remote magpie on a free plan", got[0].p.ID)
	}
	if !wg.free[b.rest] || wg.free[a.rest] {
		t.Errorf("free %v, want mac-free's alone", wg.free)
	}
	if _, _, isFree, ok := provider.RemoteSeat(a.p, a.model, false); !ok || isFree {
		t.Errorf("the Pro card: ok %v free %v, want known and paid", ok, isFree)
	}
}

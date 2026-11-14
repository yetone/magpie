package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #6708 (zzh11223311 on X): a group of two Remote magpies, each serving
// a Claude subscription signed in there. Smart and Use before expiry had
// both "not known" (used 0 in the routing log) and went by tokens and
// stickiness alone. Each is weighed now by its Claude card, as last read
// from it: B, with more of a week that renews sooner, goes first. A
// magpie passes on what routing reads of the cards it has from another,
// so a third magpie weighs them too.
func TestRemoteMagpiesWeighedByTheirCards(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	provider.ForgetRemoteCardsForTest()
	t.Cleanup(provider.ForgetRemoteCardsForTest)
	now := time.Now()
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	claude := func(user string, week float64, renews time.Duration) []provider.SubscriptionQuota {
		return []provider.SubscriptionQuota{{Provider: "claude", Name: "Claude Code", User: user, Kind: "subscription", Windows: []provider.QuotaWindow{
			{Name: "5 hours", Used: 5, ResetsAt: at(4 * time.Hour), Span: 5 * time.Hour},
			{Name: "7 days", Used: week, ResetsAt: at(renews), Span: 7 * 24 * time.Hour},
			{Name: "7 days · Opus", Used: 99, ResetsAt: at(renews), Span: 7 * 24 * time.Hour, Model: "opus"},
		}}}
	}
	remote := func(key string, cards []provider.SubscriptionQuota) *httptest.Server {
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
	ra := remote("sk-magpie-a", claude("a@x.com", 51, 117*time.Hour))
	rb := remote("sk-magpie-b", claude("b@x.com", 25, 44*time.Hour))
	for _, p := range []provider.Provider{
		{ID: "mac-a", Name: "Mac A", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-a", Chat: ra.URL},
		{ID: "mac-b", Name: "Mac B", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-b", Chat: rb.URL},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	// the Usage page's read, which routing goes by from then on
	provider.RemoteCards(context.Background())
	member := func(id string) candidate {
		p, err := provider.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		return candidate{p: *p, model: "claude/claude-sonnet-5-5", rest: id + "-6708"}
	}
	a, b := member("mac-a"), member("mac-b")
	for _, routing := range []string{provider.Pace, ""} {
		g := provider.Provider{ID: "auto-6708", Routing: routing}
		got, wg := weigh(g, []candidate{a, b}, "claude-sonnet-5-5", provider.Anthropic)
		if got[0].p.ID != "mac-b" {
			t.Errorf("routing %q: %s first, want mac-b", routing, got[0].p.ID)
		}
		la, oka := wg.lefts[a.allowanceKey()]
		lb, okb := wg.lefts[b.allowanceKey()]
		if !oka || !okb || la.used != 51 || lb.used != 25 {
			t.Errorf("routing %q: used a %v (%v) b %v (%v), want 51 and 25", routing, la.used, oka, lb.used, okb)
		}
		if !lb.due.Equal(*at(44 * time.Hour)) {
			t.Errorf("routing %q: b due %v, want its week's renewal", routing, lb.due)
		}
	}

	// this gateway tells a magpie using it what routing reads of the cards
	// it has from mac-a and mac-b
	gw := httptest.NewServer(lanGuard(New().Handler()))
	defer gw.Close()
	res, err := http.Get(gw.URL + provider.RemoteCardsPath)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), `"routing"`) || !strings.Contains(string(body), `"mac-b/claude"`) {
		t.Fatalf("the cards told on: %d %s", res.StatusCode, body)
	}
	via := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != provider.RemoteCardsPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(body)
	}))
	defer via.Close()
	if err := provider.Save(provider.Provider{ID: "home", Name: "Home", Preset: provider.RemoteMagpiePreset, Key: "sk-magpie-via", Chat: via.URL}); err != nil {
		t.Fatal(err)
	}
	provider.RemoteCards(context.Background())
	home, err := provider.Find("home")
	if err != nil {
		t.Fatal(err)
	}
	if al, inner, ok := provider.RemoteAllowance(*home, "mac-b/claude/claude-sonnet-5-5", false); !ok || inner != "claude-sonnet-5-5" {
		t.Errorf("a third magpie doesn't know mac-b's Claude: %v %q", ok, inner)
	} else if u, _ := al.For(inner, now); u != 25 {
		t.Errorf("a third magpie reads mac-b's Sonnet used %v, want 25", u)
	}
}

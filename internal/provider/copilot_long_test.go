package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// copilotVersionedAPI answers /models with the real reply Copilot gave
// at the version asked (testdata/copilot-models-<version>.json: the
// owner's account, four of its models), and keeps the versions asked.
func copilotVersionedAPI(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var asked []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := r.Header.Get("X-GitHub-Api-Version")
		mu.Lock()
		asked = append(asked, v)
		mu.Unlock()
		b, err := os.ReadFile("testdata/copilot-models-" + v + ".json")
		if err != nil {
			// a version these fixtures don't have: the oldest's reply
			b, _ = os.ReadFile("testdata/copilot-models-2026-01-09.json")
		}
		w.Write(b)
	}))
	t.Cleanup(api.Close)
	return api, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

func copilotTestModels(t *testing.T, s copilotSession) map[string]catalog.Model {
	t.Helper()
	copilotSessions = map[string]copilotSession{"gho_long": s}
	ms, err := copilotModels(context.Background(), copilotApp{User: "octocat", Token: "gho_long"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]catalog.Model{}
	for _, m := range ms {
		got[m.ID] = m
	}
	return got
}

func copilotTestSession(api string, direct bool) copilotSession {
	s := copilotSession{Token: "sess", ExpiresAt: time.Now().Add(time.Hour).Unix(), direct: direct}
	s.Endpoints.API = api
	return s
}

// Copilot Chat asks /models at the version its @vscode/copilot-api sets
// on every request (2026-08-01), where a model's long-context tier is
// listed; at 2026-01-09 Claude Opus 5.5 was 200K alone (gtliuu on X). A
// long tier at the default's prices is the window agents are told
// (Opus 5.5's 1M). One that costs more, as Copilot Chat does, keeps the
// default tier's prompt as the window and the long one as the most it
// may be raised to (GPT-6 Luna's 272K and 872K), priced above 272K at
// the long tier's prices.
func TestCopilotListsTheLongContextTier(t *testing.T) {
	signIn(t)
	api, asked := copilotVersionedAPI(t)
	got := copilotTestModels(t, copilotTestSession(api.URL, false))
	if v := asked(); len(v) != 1 || v[0] != "2026-08-01" {
		t.Fatalf("/models asked at %q; want 2026-08-01", v)
	}
	p := Provider{ID: "copilot"}

	opus := got["claude-opus-5.5"]
	if w := p.WindowOf(opus); w != 1000000 {
		t.Errorf("claude-opus-5.5 window %d; want 1000000", w)
	}
	if opus.Price == nil || opus.Price.Input != 4 || opus.Price.Output != 20 || opus.Price.CacheRead != 0.2 ||
		opus.Price.CacheWrite != 5 || opus.Price.CacheWrite1h != 8 || len(opus.Price.Tiers) != 0 {
		t.Errorf("claude-opus-5.5 price %+v; want 4/20, cache 0.2/5/8, no tier", opus.Price)
	}

	luna := got["gpt-6-luna"]
	if w := p.WindowOf(luna); w != 272000 || luna.MaxContext != 872000 {
		t.Errorf("gpt-6-luna window %d most %d; want 272000 and 872000", w, luna.MaxContext)
	}
	want := catalog.Price{Input: 0.1, Output: 0.5, CacheRead: 0.01, CacheWrite: 0.125,
		Tiers: []catalog.Tier{{Above: 272000, Input: 0.2, Output: 0.75, CacheRead: 0.02, CacheWrite: 0.25}}}
	if luna.Price == nil || !samePrice(*luna.Price, want) {
		t.Errorf("gpt-6-luna price %+v; want %+v", luna.Price, want)
	}
	// raised to its long tier by the user, it is told that
	p.Contexts = map[string]int{"gpt-6-luna": 872000}
	if w := p.WindowOf(luna); w != 872000 {
		t.Errorf("gpt-6-luna set to 872000: window %d", w)
	}

	// Grok 4.7's long tier says 500000, its limits 372000: Copilot takes
	// the limits' prompt
	if m := got["grok-4.7"]; m.Context != 200000 || m.MaxContext != 372000 {
		t.Errorf("grok-4.7 window %d most %d; want 200000 and 372000", m.Context, m.MaxContext)
	}

	// GPT-4.1 lists every price 0 (batch_size 0): not a price at all
	if m := got["gpt-4.1"]; m.Price != nil || m.Context != 128000 {
		t.Errorf("gpt-4.1 price %+v window %d; want none and 128000", m.Price, m.Context)
	}
}

// 2026-06-01 names a tier's prompt context_max and its cache read
// cache_price; read the same.
func TestCopilotTiersAtTheFirstTieredVersion(t *testing.T) {
	b, err := os.ReadFile("testdata/copilot-models-2026-06-01.json")
	if err != nil {
		t.Fatal(err)
	}
	signIn(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(b) }))
	defer api.Close()
	luna := copilotTestModels(t, copilotTestSession(api.URL, false))["gpt-6-luna"]
	if luna.Context != 272000 || luna.MaxContext != 872000 || luna.Price == nil ||
		luna.Price.CacheRead != 0.01 || len(luna.Price.Tiers) != 1 || luna.Price.Tiers[0].CacheRead != 0.02 {
		t.Errorf("gpt-6-luna at 2026-06-01: window %d most %d price %+v", luna.Context, luna.MaxContext, luna.Price)
	}
}

// The Copilot CLI's sign-in asks /models at the CLI's own version, as
// the CLI does; and a reply in the older flat shape changes nothing.
func TestCopilotCLIKeepsItsModelsVersion(t *testing.T) {
	signIn(t)
	api, asked := copilotVersionedAPI(t)
	got := copilotTestModels(t, copilotTestSession(api.URL, true))
	if v := asked(); len(v) != 1 || v[0] != copilotCLIHeaders["X-GitHub-Api-Version"] {
		t.Fatalf("CLI /models asked at %q; want %q", v, copilotCLIHeaders["X-GitHub-Api-Version"])
	}
	// served the 2026-01-09 reply: flat nano-AIU prices, no tiers
	if m := got["claude-opus-5.5"]; m.Context != 200000 || m.MaxContext != 0 || m.Price != nil {
		t.Errorf("flat reply: claude-opus-5.5 window %d most %d price %+v; want 200000, 0, none", m.Context, m.MaxContext, m.Price)
	}
}

// What Copilot listed the model at is its list price, over models.dev's.
func TestCopilotListPriceIsCopilots(t *testing.T) {
	signIn(t)
	luna := catalog.Model{ID: "gpt-6-luna", Context: 272000, MaxContext: 872000,
		Price: &catalog.Price{Input: 0.1, Output: 0.5, Tiers: []catalog.Tier{{Above: 272000, Input: 0.2, Output: 0.75}}}}
	if err := catalog.SaveLive("copilot", copilotBase, []catalog.Model{luna}); err != nil {
		t.Fatal(err)
	}
	pr, ok := Provider{ID: "copilot"}.ListPrice("gpt-6-luna")
	if !ok || !samePrice(pr, *luna.Price) {
		t.Errorf("ListPrice %+v %v; want Copilot's %+v", pr, ok, *luna.Price)
	}
}

func samePrice(a, b catalog.Price) bool {
	if a.Input != b.Input || a.Output != b.Output || a.CacheRead != b.CacheRead || a.CacheWrite != b.CacheWrite ||
		a.CacheWrite1h != b.CacheWrite1h || len(a.Tiers) != len(b.Tiers) {
		return false
	}
	for i := range a.Tiers {
		if a.Tiers[i] != b.Tiers[i] {
			return false
		}
	}
	return true
}

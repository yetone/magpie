package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// sparkling_lemur_62868 on Discord: a team's OpenAI key with credits that
// other services spend from too, and magpie to stop at a share of them. A
// key that has spent its spend cap, as the Usage page prices magpie's calls
// on it, is passed over: the provider's next key serves the request, then
// its fallback; with neither, the request is answered 429 with which key is
// at which cap and how to go on. The key never reaches the upstream. A
// spend cap is no usage cap: Stop at the caps doesn't hold the fallbacks
// back from it.
func TestKeySpendCapPassesTheKeyOver(t *testing.T) {
	fresh(t)
	s := settings.Load()
	s.ModelPrices = map[string]settings.ModelPrice{
		"plan/m1": {Input: new(1.0), Output: new(1.0), CacheRead: new(0.0), CacheWrite: new(0.0)},
	}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var keys []string
	serve := func(name string) *httptest.Server {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			keys = append(keys, name+" "+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
		}))
		t.Cleanup(up.Close)
		return up
	}
	planUp, spareUp := serve("plan"), serve("spare")
	since := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	plan := provider.Provider{ID: "plan", Name: "Plan", Key: "sk-team", KeyName: "Team", Keys: []provider.KeyAccount{{Name: "Spare", Key: "sk-spare"}},
		Chat: planUp.URL + "/v1", Models: []string{"m1"}, Routing: provider.Ordered,
		KeySpendCaps: map[string]provider.SpendCap{provider.KeyID("sk-team"): {USD: 5, Period: provider.SpendTotal, Since: since}}}
	if err := provider.Save(plan); err != nil {
		t.Fatal(err)
	}
	// $6 spent on Team since its cap was set, $1 before it
	usage.Append(usage.Record{Time: time.Now().Add(-30 * time.Minute), Agent: "codex", Provider: "plan", Model: "m1", ProviderKeyID: provider.KeyID("sk-team"), Input: 6e6, Status: 200})
	usage.Append(usage.Record{Time: since.Add(-time.Hour), Agent: "codex", Provider: "plan", Model: "m1", ProviderKeyID: provider.KeyID("sk-spare"), Input: 1e6, Status: 200})
	h := New().Handler()
	post := func() (int, string, []string) {
		t.Helper()
		mu.Lock()
		keys = nil
		mu.Unlock()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(chatReq)))
		mu.Lock()
		defer mu.Unlock()
		return w.Code, w.Body.String(), keys
	}
	// Team, first, is at its cap: Spare serves
	if code, body, got := post(); code != 200 || len(got) != 1 || got[0] != "plan sk-spare" {
		t.Fatalf("Team at its cap: %d %s, upstream got %v", code, body, got)
	}
	// raised past what it spent, Team serves again
	if err := provider.SetKeySpendCap("plan", "Team", &provider.SpendCap{USD: 50, Period: provider.SpendTotal}); err != nil {
		t.Fatal(err)
	}
	if code, body, got := post(); code != 200 || len(got) != 1 || got[0] != "plan sk-team" {
		t.Fatalf("Team under its raised cap: %d %s, upstream got %v", code, body, got)
	}
	// both at their caps, with no fallback: a 429 says which and why
	if err := provider.SetKeySpendCap("plan", "Team", &provider.SpendCap{USD: 5, Period: provider.SpendTotal}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetKeySpendCap("plan", "Spare", &provider.SpendCap{USD: 0.000001, Period: provider.SpendDay}); err != nil {
		t.Fatal(err)
	}
	code, body, got := post()
	var e struct {
		Error struct{ Message, Type string }
	}
	json.Unmarshal([]byte(body), &e)
	if code != 429 || len(got) != 0 || !strings.Contains(e.Error.Message, "spend cap reached") ||
		!strings.Contains(e.Error.Message, "Team") || !strings.Contains(e.Error.Message, "$6.00 of the $5.00 spend cap") ||
		!strings.Contains(e.Error.Message, "Spare") || !strings.Contains(e.Error.Message, "for today") || !strings.Contains(e.Error.Message, "spend-cap") {
		t.Fatalf("both at their caps: %d %s, upstream got %v", code, body, got)
	}
	// with a fallback, it serves
	p, _ := provider.Find("plan")
	p.Fallback = []string{"spare/m2"}
	if err := provider.Save(*p); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "spare", Name: "Spare plan", Key: "sk-other", Chat: spareUp.URL + "/v1", Models: []string{"m2"}}); err != nil {
		t.Fatal(err)
	}
	if code, body, got := post(); code != 200 || len(got) != 1 || got[0] != "spare sk-other" {
		t.Fatalf("both at their caps, with a fallback: %d %s, upstream got %v", code, body, got)
	}
	// a spend cap is not a usage cap: Stop at the caps doesn't end there
	if err := provider.SetCapStop("plan", true); err != nil {
		t.Fatal(err)
	}
	if code, body, got := post(); code != 200 || len(got) != 1 || got[0] != "spare sk-other" {
		t.Fatalf("both at their caps, Stop at the caps on: %d %s, upstream got %v", code, body, got)
	}
}

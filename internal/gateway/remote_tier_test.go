package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

// JackyFill on X: Codex → magpie1 → (Remote magpie) → magpie2 → a ChatGPT
// account, Fast on in magpie2 alone (here its gateway key's tier).
// magpie1's log said "default", the ChatGPT backend's word in the body
// that tells nothing, and nothing of the priority magpie2 sent. It now
// records what magpie2 sent its vendor, and the served tier unknown, as
// magpie2 does; a request magpie2 sends at none says none.
func TestRemoteMagpieTierRecorded(t *testing.T) {
	tried := tierAccounts(t, nil, nil, func(string) (string, string) { return "auto", "default" })
	to, _ := url.Parse(provider.CodexBase)
	old := provider.CodexBase
	provider.CodexBase = "https://chatgpt.com/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = old })
	cfg := settings.Load()
	cfg.LAN = true
	if err := settings.Save(cfg); err != nil {
		t.Fatal(err)
	}
	secret, err := access.Update("add-key", access.Change{Name: "Remote"})
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := access.List()
	var keyID string
	for _, k := range keys {
		if k.Name == "Remote" {
			keyID = k.ID
		}
	}

	s := New()
	s.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "chatgpt.com" {
			r.URL.Scheme, r.URL.Host = to.Scheme, to.Host
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	h := s.Handler()
	there := httptest.NewServer(h)
	t.Cleanup(there.Close)
	id, err := provider.Add(provider.Provider{ID: "office", Name: "Office", Key: secret, Preset: provider.RemoteMagpiePreset, Chat: strings.TrimPrefix(there.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	office, err := provider.Find(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := office.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct{ set, want string }{{"fast", "priority"}, {"standard", ""}} {
		if _, err := access.Update("tier-key", access.Change{Key: keyID, ServiceTier: c.set}); err != nil {
			t.Fatal(err)
		}
		model := id + "/codex/gpt-6-luna"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"`+model+`","stream":true,"input":"ping"}`)))
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", c.set, rec.Code, rec.Body)
		}
		if got := tried(); len(got) == 0 || got[len(got)-1] != "acct-plus:"+c.want {
			t.Fatalf("%s: backend asked %v, want acct-plus:%s", c.set, got, c.want)
		}
		var here *usage.Record
		recs := usage.Load(time.Time{})
		for i := len(recs) - 1; i >= 0; i-- {
			if recs[i].Provider == id {
				here = &recs[i]
				break
			}
		}
		if here == nil {
			t.Fatalf("%s: no record of the request here: %+v", c.set, recs)
		}
		if here.TierSent != c.want || here.TierServed != "" {
			t.Errorf("%s: recorded here sent %q served %q, want sent %q served unknown", c.set, here.TierSent, here.TierServed, c.want)
		}
		s.trace.mu.Lock()
		var tr *Route
		for _, r := range s.trace.routes {
			if r.Model == model {
				tr = r
			}
		}
		var try Try
		if tr != nil && len(tr.Tries) > 0 {
			try = tr.Tries[len(tr.Tries)-1]
		}
		s.trace.mu.Unlock()
		if tr == nil || try.Tier != c.want || try.TierServed != "" {
			t.Errorf("%s: route's try %+v, want tier %q", c.set, try, c.want)
		}
	}
}

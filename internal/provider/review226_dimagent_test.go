package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/dimagent"
)

// The gateway's request, signed by the account, reaches the fake upstream's
// chat endpoint with the account's Bearer and the desktop client's headers.
func TestReview226SignForwards(t *testing.T) {
	var got http.Header
	var path string
	dimagentSite(t, func(w http.ResponseWriter, r *http.Request) {
		got, path = r.Header.Clone(), r.URL.Path
		_, _ = io.WriteString(w, `{"choices":[]}`)
	})
	dimagentKeep(t, "me")
	p, ok := dimagentAccount()
	if !ok {
		t.Fatal("no account provider")
	}
	req, _ := http.NewRequest("POST", p.Base(Chat)+"/chat/completions", strings.NewReader(`{}`))
	req.Header.Set("x-api-key", "magpie")
	if err := p.Sign(context.Background(), req, Chat, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if path != "/v1/chat/completions" || got.Get("Authorization") != "Bearer acc" ||
		got.Get("User-Agent") != dimagent.ChatUserAgent || got.Get("X-Title") != dimagent.TitleChat {
		t.Fatalf("path %s headers %v", path, got)
	}
}

// Twenty requests at once on a token inside the lead: one refresh, all get the new token.
func TestReview226ParallelSignRefreshOnce(t *testing.T) {
	var asks atomic.Int32
	dimagentTokenRound(t, "new-acc", "new-ref", &asks)
	signIn(t)
	dimagentAdd(t, "me", "old", "ref", time.Now().Add(time.Hour))
	p, _ := dimagentAccount()
	var wg sync.WaitGroup
	var bad atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", p.Base(Chat)+"/chat/completions", nil)
			if p.Sign(context.Background(), req, Chat, nil) != nil || req.Header.Get("Authorization") != "Bearer new-acc" {
				bad.Add(1)
			}
		}()
	}
	wg.Wait()
	if asks.Load() != 1 || bad.Load() != 0 {
		t.Fatalf("refreshes %d, bad %d", asks.Load(), bad.Load())
	}
	if a, r, _ := dimagentKept(t, "me"); a != "new-acc" || r != "new-ref" {
		t.Fatalf("kept %s %s", a, r)
	}
}

// A transient refresh failure while the access token still has hours left
// should not fail the request (the code's "a hiccup" branch intends this).
func TestReview226RefreshHiccupInsideLead(t *testing.T) {
	dimagentSite(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	signIn(t)
	dimagentAdd(t, "me", "still-good", "ref", time.Now().Add(12*time.Hour))
	c, err := dimagentFresh(context.Background(), "me")
	if err != nil || c.Access != "still-good" {
		t.Fatalf("a 502 on refresh with 12h left failed the request: %v", err)
	}
}

// Usage reached through the shared LoginUsage switch.
func TestReview226LoginUsage(t *testing.T) {
	dimagentUsage(t, `{"success":true,"data":{"subscription":{"product":{"name":"Pro"}},
	 "credits":{"subscription_bucket":{"total_units":700,"used_units":210,
	  "window_states":[{"window_duration_hours":5,"window_token_cap":1000,"window_token_used":250}]}},
	 "credits_display":{"credit_name":"Credits"}}}`)
	dimagentKeep(t, "me")
	qs := LoginUsage(context.Background(), "dimagent")
	q, ok := qs["me"]
	if !ok {
		for k := range qs {
			t.Log("key", k)
		}
		t.Fatalf("no quota for me: %v", qs)
	}
	if q.Error != "" || q.Plan != "Pro" || len(q.Windows) != 2 || q.Windows[0].Used != 30 || q.Windows[1].Used != 25 || q.Windows[1].Name != "5 hours" {
		t.Fatalf("%+v", q)
	}
}

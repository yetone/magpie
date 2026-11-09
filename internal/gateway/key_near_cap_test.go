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

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/provider"
)

// ox0pine on X: a gateway key held to one provider sometimes hung in
// routing. The key's MaxConcurrency of 1 and its one account's turn near
// its usage cap (cap_near.go) were each taken by one of two turns: the
// first, its try answered with a passing failure, kept the key's slot
// for the same account again while the second took the account's turn
// it gave back and waited for the key's. Each waited for the other's
// till the agents hung up. The second waits for the key's slot before it
// takes the account's turn, so the first goes again and both are
// answered.
func TestKeySlotAndNearCapTurnNeverWaitOnEachOther(t *testing.T) {
	codexSignedIn(t)
	now := time.Now()
	five, week := now.Add(2*time.Hour).Unix(), now.Add(4*24*time.Hour).Unix()
	var mu sync.Mutex
	asked := 0
	second := make(chan struct{}) // the second turn waits for its turn
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/codex/responses":
			io.ReadAll(r.Body)
			mu.Lock()
			asked++
			n := asked
			mu.Unlock()
			if n == 1 {
				// the first try: overloaded, asked again after the pause,
				// the second turn sent meanwhile
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(503)
				io.WriteString(w, overloadedStatus)
				return
			}
			io.WriteString(w, sse(
				`event: codex.rate_limits`+"\n"+`data: {"type":"codex.rate_limits","plan_type":"plus","rate_limits":{"allowed":true,"limit_reached":false,"primary":{"used_percent":89,"window_minutes":300,"reset_at":`+jsonInt(five)+`},"secondary":{"used_percent":10,"window_minutes":10080,"reset_at":`+jsonInt(week)+`}},"credits":null,"metered_limit_name":null,"limit_name":null}`,
				`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
				`data: {"type":"response.output_text.delta","delta":"pong"}`,
				`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
		case "/backend-api/wham/usage":
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus",
				"rate_limit": map[string]any{"allowed": true,
					"primary_window":   map[string]any{"used_percent": 89, "limit_window_seconds": 18000, "reset_at": five},
					"secondary_window": map[string]any{"used_percent": 10, "limit_window_seconds": 604800, "reset_at": week}}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	nearCapAccounts(t, 90, 89)
	pause := retryPause
	retryPause = 300 * time.Millisecond
	t.Cleanup(func() { retryPause = pause })

	keys, secrets := newCaller(t, "Held")
	if _, err := access.Update("models-key", access.Change{Key: keys[0].ID, Models: []string{"codex/*"}}); err != nil {
		t.Fatal(err)
	}
	keyCap(t, keys[0].ID, 1)
	s := New()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	post := func() nearReply {
		req, _ := http.NewRequest("POST", srv.URL+CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping"}`))
		req.Header.Set("Authorization", "Bearer "+secrets[0])
		res, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
		if err != nil {
			return nearReply{-1, err.Error()}
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return nearReply{res.StatusCode, string(b)}
	}
	replies := make(chan nearReply, 2)
	go func() { replies <- post() }()
	waitFor(t, "the first try answered", func() bool { mu.Lock(); defer mu.Unlock(); return asked == 1 })
	go func() {
		close(second)
		replies <- post()
	}()
	<-second
	for range 2 {
		if r := <-replies; r.code != 200 || !strings.Contains(r.body, "pong") {
			t.Fatalf("%d %s", r.code, r.body)
		}
	}
}

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// accountsBackend is a ChatGPT backend with several accounts, by
// chatgpt-account-id, each turn spending what cost says of its five hours
// for the request's body, counted as the turn ends. It tells the usage as
// ChatGPT does: in whole percents, in the x-codex-* headers of a reply as
// it begins (without its own turn), and at /wham/usage.
type accountsBackend struct {
	mu         sync.Mutex
	used       map[string]float64
	turns      map[string]int
	cost       func(body []byte) float64
	five, week int64
}

func newAccountsBackend(t *testing.T, used map[string]float64, cost func([]byte) float64, hold time.Duration) *accountsBackend {
	t.Helper()
	now := time.Now()
	b := &accountsBackend{used: used, turns: map[string]int{}, cost: cost,
		five: now.Add(2 * time.Hour).Unix(), week: now.Add(4 * 24 * time.Hour).Unix()}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acct := r.Header.Get("chatgpt-account-id")
		switch r.URL.Path {
		case "/backend-api/codex/responses":
			body, _ := io.ReadAll(r.Body)
			b.mu.Lock()
			told := math.Floor(b.used[acct])
			if told >= 100 {
				b.mu.Unlock()
				w.WriteHeader(429)
				io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_in_seconds":7200}}`)
				return
			}
			b.turns[acct]++
			b.mu.Unlock()
			w.Header().Set("x-codex-primary-used-percent", strconv.FormatFloat(told, 'f', -1, 64))
			w.Header().Set("x-codex-primary-window-minutes", "300")
			w.Header().Set("x-codex-primary-reset-at", jsonInt(b.five))
			w.Header().Set("x-codex-secondary-used-percent", "10")
			w.Header().Set("x-codex-secondary-window-minutes", "10080")
			w.Header().Set("x-codex-secondary-reset-at", jsonInt(b.week))
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, sse(`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`))
			w.(http.Flusher).Flush()
			select {
			case <-time.After(hold):
			case <-r.Context().Done():
			}
			b.mu.Lock()
			b.used[acct] += b.cost(body)
			b.mu.Unlock()
			io.WriteString(w, sse(`data: {"type":"response.output_text.delta","delta":"pong"}`,
				`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
		case "/backend-api/wham/usage":
			b.mu.Lock()
			told := math.Floor(b.used[acct])
			b.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus",
				"rate_limit": map[string]any{"allowed": true,
					"primary_window":   map[string]any{"used_percent": told, "limit_window_seconds": 18000, "reset_at": b.five},
					"secondary_window": map[string]any{"used_percent": 10, "limit_window_seconds": 604800, "reset_at": b.week}}})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(up.Close)
	was := provider.CodexBase
	provider.CodexBase = up.URL + "/backend-api/codex"
	t.Cleanup(func() { provider.CodexBase = was })
	return b
}

// capOverrun has sessions send turns at once, size(session, turn) bytes
// of input each, through the signed-in account capped at 90% from start
// (and the spares, uncapped), each till one is refused, and fails
// when the capped one spent past its cap: by more than the point its
// reading in whole percents may be under what it has spent.
func capOverrun(t *testing.T, sessions int, start float64, cost func([]byte) float64, size func(session, turn int) int, spares ...string) {
	t.Helper()
	codexSignedIn(t, spares...)
	const capAt = 90
	used := map[string]float64{"acct-1": start}
	for i := range spares {
		used[fmt.Sprintf("acct-%d", i+2)] = start
	}
	b := newAccountsBackend(t, used, cost, 40*time.Millisecond)
	t.Cleanup(func() {
		// a refused turn has the account read again, which outlives it:
		// done before CodexBase is put back
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		provider.LoginReadsDone(ctx)
	})
	nearCapAccounts(t, capAt, start, spares...)
	srv := New()
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for s := range sessions {
		wg.Go(func() {
			for n := range 60 {
				rec := httptest.NewRecorder()
				body := fmt.Sprintf(`{"model":"gpt-5.5","stream":true,"input":%q}`, strings.Repeat("x", size(s, n)))
				req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer chatgpt-token")
				req.Header.Set("chatgpt-account-id", "acct-1")
				srv.Handler().ServeHTTP(rec, req)
				mu.Lock()
				codes[rec.Code]++
				mu.Unlock()
				if rec.Code != 200 {
					return
				}
			}
		})
	}
	wg.Wait()
	b.mu.Lock()
	got, turns := b.used["acct-1"], maps.Clone(b.turns)
	b.mu.Unlock()
	t.Logf("turns %v, codes %v, used %.2f%%", turns, codes, got)
	if got > capAt+1 {
		t.Fatalf("used %.2f%% of a %d%% cap (codes %v, turns %v); want at most %d%%", got, capAt, codes, turns, capAt+1)
	}
	if turns["acct-1"] == 0 {
		t.Fatal("the capped account took no turn")
	}
}

// Every turn spends four points, as a long conversation's turn does on
// ChatGPT Plus, with nothing learned yet of what a turn there spends (a
// magpie just started): the five sessions' turns, and the next five, all
// went before any reading had what one spent, and took the account from
// 60% to 92-96% of its 90% cap.
func TestCapOverrunHeavyTurns(t *testing.T) {
	capOverrun(t, 5, 60, func([]byte) float64 { return 4 }, func(int, int) int { return 10 })
}

// yalifesign on X, again: turns that don't spend alike, told in whole
// percents (one in four a long one at four points, the rest half a
// point). From 81%, the fourth turn took it to 86.5%, and the eighth was
// let in at 88% by the reading alone and spent four points on its own, to
// 92%. A long turn once seen, the account stops short of the cap by it;
// one longer than any before it in the window is the overrun left
// (docs/subsystems/gateway-routing.md).
func TestCapOverrunUnevenTurns(t *testing.T) {
	capOverrun(t, 1, 81, func(b []byte) float64 { return float64(bytes.Count(b, []byte("x"))) / 1000 }, func(_, n int) int {
		if n%4 == 3 {
			return 4000
		}
		return 500
	})
}

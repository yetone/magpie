package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// resetBackend stands in for the ChatGPT backend of accounts that are out
// of their allowance: their usage (the week's and five hours'), the
// resets each holds, and spending one, which lets the account answer again.
type resetBackend struct {
	mu    sync.Mutex
	week  map[string]float64 // used, by account id; the five hours are full
	held  map[string]int     // resets held
	out   map[string]bool    // out of its allowance
	spent []string           // accounts a reset was spent on
	gone  bool               // every reset spent elsewhere since the usage was read
	tried []string           // accounts asked for a turn
	// credits: holds credits, which the backend spends once the account
	// is out of its allowance, after turning away the first edge turns
	// at the moment it ran out; fails: answers every turn with it
	credits map[string]bool
	edge    map[string]int
	fails   map[string]int
	// sayCredits: replies say the account's credits as ChatGPT's do,
	// x-codex-credits-has-credits and -unlimited
	sayCredits bool
}

func newResetBackend(t *testing.T, accounts ...string) *resetBackend {
	t.Helper()
	b := &resetBackend{week: map[string]float64{}, held: map[string]int{}, out: map[string]bool{},
		credits: map[string]bool{}, edge: map[string]int{}, fails: map[string]int{}}
	for _, a := range accounts {
		b.week[a], b.held[a], b.out[a] = 100, 1, true
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acct := r.Header.Get("chatgpt-account-id")
		b.mu.Lock()
		defer b.mu.Unlock()
		now := time.Now()
		switch r.URL.Path {
		case "/backend-api/codex/responses":
			io.ReadAll(r.Body)
			b.tried = append(b.tried, acct)
			if n := b.fails[acct]; n > 0 {
				w.WriteHeader(n)
				io.WriteString(w, `{"error":{"type":"server_error","message":"something went wrong"}}`)
				return
			}
			// out of its allowance: refused, unless it holds credits and
			// is past the turns turned away as it ran out
			refused := b.out[acct] && (!b.credits[acct] || b.edge[acct] > 0)
			if refused && b.credits[acct] {
				b.edge[acct]--
			}
			if b.sayCredits {
				w.Header().Set("x-codex-credits-has-credits", strconv.FormatBool(b.credits[acct]))
				w.Header().Set("x-codex-credits-unlimited", "false")
			}
			if refused {
				w.WriteHeader(429)
				io.WriteString(w, `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","plan_type":"plus","resets_in_seconds":7200}}`)
				return
			}
			io.WriteString(w, sse(
				`data: {"type":"response.created","response":{"id":"r1","model":"gpt-5.5"}}`,
				`data: {"type":"response.output_text.delta","delta":"pong"}`,
				`data: {"type":"response.completed","response":{"id":"r1","usage":{"input_tokens":7,"output_tokens":1}}}`))
		case "/backend-api/wham/usage":
			five := 100.0
			if !b.out[acct] {
				five = 0
			}
			json.NewEncoder(w).Encode(map[string]any{"plan_type": "plus",
				"rate_limit": map[string]any{
					"primary_window":   map[string]any{"used_percent": five, "limit_window_seconds": 18000, "reset_at": now.Add(2 * time.Hour).Unix()},
					"secondary_window": map[string]any{"used_percent": b.week[acct], "limit_window_seconds": 604800, "reset_at": now.Add(3 * 24 * time.Hour).Unix()}},
				"rate_limit_reset_credits": map[string]any{"available_count": b.held[acct]},
				"credits":                  map[string]any{"has_credits": b.credits[acct], "unlimited": false, "balance": map[bool]string{true: "120", false: "0"}[b.credits[acct]]}})
		case "/backend-api/wham/rate-limit-reset-credits":
			var credits []any
			for i := range b.held[acct] {
				credits = append(credits, map[string]any{"id": acct + "-" + string(rune('a'+i)), "reset_type": "codex_rate_limits",
					"status": "available", "granted_at": "2026-09-01T00:00:00Z", "expires_at": now.Add(20 * 24 * time.Hour).Format(time.RFC3339)})
			}
			json.NewEncoder(w).Encode(map[string]any{"available_count": b.held[acct], "credits": credits})
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			if b.held[acct] == 0 || b.gone {
				io.WriteString(w, `{"code":"no_credit","windows_reset":0}`)
				return
			}
			b.held[acct]--
			b.spent = append(b.spent, acct)
			b.out[acct], b.week[acct] = false, 0
			io.WriteString(w, `{"code":"reset","windows_reset":2}`)
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

func (b *resetBackend) seen() (tried, spent string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.tried, ","), strings.Join(b.spent, ",")
}

// resetPost posts a Codex turn to srv as codexPost does.
func resetPost(t *testing.T, srv *Server) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", CodexPath+"/responses", strings.NewReader(`{"model":"gpt-5.5","stream":true,"input":"ping"}`))
	req.Header.Set("Authorization", "Bearer chatgpt-token")
	req.Header.Set("chatgpt-account-id", "acct-1")
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// resetsTraced is the resets the routing trace says were spent by
// themselves, as who: what.
func resetsTraced(srv *Server) []string {
	var out []string
	for _, r := range srv.Trace(context.Background(), 0, 0).Routes {
		for _, t := range r.Tries {
			if t.Reset != nil {
				out = append(out, t.Reset.Who+": "+t.Reset.Text)
			}
		}
	}
	return out
}

// Codex's one account, its week used up, with the user's leave to spend
// its resets by itself: one is spent and the turn goes through (StringKe
// on Discord: resets gifted to the account, used once the week is out).
func TestCodexAutoResetOneAccount(t *testing.T) {
	codexSignedIn(t)
	b := newResetBackend(t, "acct-1")
	if err := provider.SetCodexAutoReset("Me@example.com", true); err != nil {
		t.Fatal(err)
	}
	srv := New()
	code, body := resetPost(t, srv)
	tried, spent := b.seen()
	if code != 200 || !strings.Contains(body, "pong") || tried != "acct-1,acct-1" || spent != "acct-1" {
		t.Fatalf("%d %s tried %s spent %s", code, body, tried, spent)
	}
	if got := resetsTraced(srv); len(got) != 1 || got[0] != "me@example.com: 2 windows started again" {
		t.Fatalf("traced %v", got)
	}
	if calls := srv.Recent(); len(calls) == 0 || !strings.Contains(calls[0].Fallback, "used one of its resets") {
		t.Fatalf("request log %+v", calls)
	}
	// out again in the same week: no second reset spent
	b.mu.Lock()
	b.out["acct-1"], b.week["acct-1"], b.held["acct-1"] = true, 100, 1
	b.mu.Unlock()
	code, _ = resetPost(t, srv)
	if _, spent := b.seen(); code != 429 || spent != "acct-1" {
		t.Fatalf("second time in the week %d, spent %s", code, spent)
	}
}

// Two accounts out, the one Codex is signed in to spending its resets by
// itself: tried both, it spends one and answers.
func TestCodexAutoResetAfterEveryAccount(t *testing.T) {
	codexSignedIn(t, "spare@example.com")
	b := newResetBackend(t, "acct-1", "acct-2")
	if err := provider.SetCodexAutoReset("me@example.com", true); err != nil {
		t.Fatal(err)
	}
	srv := New()
	code, body := resetPost(t, srv)
	tried, spent := b.seen()
	if code != 200 || !strings.Contains(body, "pong") || spent != "acct-1" || !strings.HasSuffix(tried, ",acct-1") || strings.Count(tried, ",") != 2 {
		t.Fatalf("%d %s tried %s spent %s", code, body, tried, spent)
	}
	if got := resetsTraced(srv); len(got) != 1 || !strings.HasPrefix(got[0], "me@example.com: ") {
		t.Fatalf("traced %v", got)
	}
}

// No reset is spent when the user didn't say so, when they kept it to
// the one about to expire (#2078), when only the five hours are used up,
// or when the account holds none: ChatGPT's refusal goes back as it came.
func TestCodexAutoResetNot(t *testing.T) {
	for _, c := range []struct {
		name string
		on   bool
		week float64
		held int
		gone bool
		// expiringOnly: on before one expires, off when the week runs out
		expiringOnly bool
	}{
		{"not turned on", false, 100, 1, false, false},
		{"only before one expires", true, 100, 1, false, true},
		{"only the five hours", true, 60, 1, false, false},
		{"none held", true, 100, 0, false, false},
		{"no credit when spent", true, 100, 1, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			codexSignedIn(t)
			b := newResetBackend(t, "acct-1")
			b.week["acct-1"], b.held["acct-1"], b.gone = c.week, c.held, c.gone
			if c.on {
				if err := provider.SetCodexAutoReset("me@example.com", true); err != nil {
					t.Fatal(err)
				}
			}
			if c.expiringOnly {
				if err := provider.SetCodexAutoResetCase("me@example.com", provider.ResetCaseWeek, false); err != nil {
					t.Fatal(err)
				}
			}
			srv := New()
			code, body := resetPost(t, srv)
			tried, spent := b.seen()
			if code != 429 || !strings.Contains(body, "usage_limit_reached") || tried != "acct-1" || spent != "" {
				t.Fatalf("%d %s tried %s spent %s", code, body, tried, spent)
			}
			if got := resetsTraced(srv); len(got) != 0 {
				t.Fatalf("traced %v", got)
			}
		})
	}
}

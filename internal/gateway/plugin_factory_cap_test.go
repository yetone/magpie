package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/testenv"
)

// factoryCore is the Droid Core models Factory's plugin gives its Core
// windows (packages/factory/index.mjs CORE, 0.1.38).
var factoryCore = []string{"glm-5.3", "glm-5.3-flash", "glm-5.2", "kimi-k3", "deepseek-v4.1-flash", "qwen3.8-max", "minimax-m3", "minimax-m2.7", "mistral-large-4", "mistral-medium-3.5", "nemotron-3-ultra"}

// standardOnlyWindows are an account's windows as Factory's plugin 0.1.38
// reports them with its Standard usage only option on (limitWindows(l,
// true), plugins#349): Standard's 5 hours and 7 days count every model,
// Droid Core's are aside (shown only), and so is the extra usage.
func standardOnlyWindows(five, week float64, fiveReset, weekReset time.Time, core float64) []map[string]any {
	return []map[string]any{
		{"name": "5 hours", "used": five, "span": 18000, "resetsAt": fiveReset.UTC().Format(time.RFC3339)},
		{"name": "7 days", "used": week, "span": 604800, "resetsAt": weekReset.UTC().Format(time.RFC3339)},
		{"name": "Droid Core · 5 hours", "used": core, "span": 18000, "resetsAt": fiveReset.UTC().Format(time.RFC3339), "aside": true, "models": factoryCore},
		{"name": "Droid Core · 7 days", "used": core, "span": 604800, "resetsAt": weekReset.UTC().Format(time.RFC3339), "aside": true, "models": factoryCore},
		{"name": "Extra usage", "used": 0, "display": "$12.50", "aside": true},
	}
}

// Factory served by its plugin, with Standard usage only on, two accounts
// A then B in order routing and each capped at 99% (plugins#349): an
// account any of whose Standard windows reaches 99% isn't sent the
// request, B takes it; both capped, the agent is told so with a 429 and
// when to try again, and nothing reaches Factory; a window that renews
// (read again below the cap, or a reading whose reset time has passed)
// gives the account back. A conversation on A moves to B when A is capped;
// once A is back, it goes back to A unless what B cached of it is worth
// keeping, or it stays a whole session on whoever answered it.
func TestPluginFactoryStandardOnlyCaps(t *testing.T) {
	bun := testenv.Bun(t)
	fresh(t)
	t.Setenv("MAGPIE_BUN", bun)
	t.Setenv("FAKE_ID", "factory")
	t.Setenv("FAKE_GLM", "1") // glm-5.3, one of Droid Core's
	windows := filepath.Join(t.TempDir(), "windows.json")
	t.Setenv("FAKE_WINDOWS", windows)
	t.Cleanup(plugin.Settle)
	// the allowances the plugin tells, not routing_test's none
	old := allowances
	allowances = provider.Allowances
	t.Cleanup(func() { allowances = old })

	var mu sync.Mutex
	var tried []string // each request's account, as its token names it
	cached := false    // the vendor says it read most of the prompt from cache
	up := testenv.FakeVendor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		who := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer fresh-r-")
		mu.Lock()
		tried = append(tried, who)
		withCache := cached
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if r.URL.Path == "/v1/messages" {
			for _, e := range []string{
				`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"fake-claude","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"from ` + who + `"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
				`{"type":"message_stop"}`,
			} {
				fmt.Fprintf(w, "data: %s\n\n", e)
			}
			return
		}
		fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{"role":"assistant","content":"from `+who+`"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		if withCache {
			fmt.Fprint(w, `data: {"id":"c","object":"chat.completion.chunk","model":"fake-1","choices":[],"usage":{"prompt_tokens":6000,"completion_tokens":2,"total_tokens":6002,"prompt_tokens_details":{"cached_tokens":5000}}}`+"\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer up.Close()

	// marked moved, as every Factory user is since the retire (10-17)
	dir := filepath.Dir(provider.Path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mig, _ := json.Marshal(map[string]any{"factory": map[string]any{"state": provider.MovePlugin}})
	if err := os.WriteFile(filepath.Join(dir, "migrations.json"), mig, 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	fiveReset, weekReset := now.Add(2*time.Hour), now.Add(4*24*time.Hour)
	// write is what Factory says of each account now
	write := func(t *testing.T, a, b []map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"a@fake": a, "b@fake": b})
		tmp := windows + ".tmp"
		if err := os.WriteFile(tmp, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, windows); err != nil {
			t.Fatal(err)
		}
	}
	// usage writes it, and waits for routing to have read it again
	usage := func(t *testing.T, a, b []map[string]any) {
		t.Helper()
		write(t, a, b)
		// each window's share and reset, as the plugin said them
		said := func(ws []map[string]any) []string {
			var out []string
			for _, w := range ws {
				if w["name"] == "5 hours" || w["name"] == "7 days" {
					r, _ := time.Parse(time.RFC3339, w["resetsAt"].(string))
					out = append(out, fmt.Sprintf("%s %g %d", w["name"], w["used"], r.Unix()))
				}
			}
			return out
		}
		has := func(a provider.Allowance, ws []string) bool {
			for _, w := range ws {
				if !slices.ContainsFunc(a, func(l provider.Limit) bool { return fmt.Sprintf("%s %g %d", l.Name, l.Used, l.Resets.Unix()) == w }) {
					return false
				}
			}
			return true
		}
		want := map[string][]string{"a@fake": said(a), "b@fake": said(b)}
		for _, u := range []string{"a@fake", "b@fake"} {
			provider.StaleAllowance("plugin:factory", u)
		}
		deadline := time.Now().Add(time.Minute)
		for {
			got := provider.Allowances("plugin:factory")
			n := 0
			for u, w := range want {
				if has(got[u], w) {
					n++
				}
			}
			if n == len(want) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("routing never read the windows: %+v", got)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	write(t, standardOnlyWindows(40, 30, fiveReset, weekReset, 100), standardOnlyWindows(10, 5, fiveReset, weekReset, 0))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	for _, team := range []string{"a", "b"} {
		st, err := provider.StartPluginSignIn("factory", 1, map[string]string{"where": "work", "team": team})
		if err != nil {
			t.Fatal(err)
		}
		if err := provider.SubmitSignInCallback(st.ID, "good"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	if p, err := provider.Find("factory"); err != nil || !p.IsPlugin() || !provider.PluginServes("factory") {
		t.Fatalf("Find(factory) = %+v, %v; want the plugin's", p, err)
	}
	if ls := provider.Logins("factory"); len(ls) != 2 {
		t.Fatalf("accounts %+v", ls)
	}
	// A then B, each at 99% of each window, as the user set them with
	// magpie provider account-cap factory <account> 99
	if err := provider.SwitchLogin("factory", "a@fake"); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetRouting("factory", provider.Ordered); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"a@fake", "b@fake"} {
		if err := provider.SetAccountCap("factory", u, 99); err != nil {
			t.Fatal(err)
		}
	}
	if p, _ := provider.Find("factory"); p.AccountCaps["a@fake"] != 99 || p.AccountCaps["b@fake"] != 99 {
		t.Fatalf("caps kept as %v", p.AccountCaps)
	}

	s := New()
	ask := func(t *testing.T, session, model string) (int, http.Header, string, []string) {
		t.Helper()
		mu.Lock()
		tried = nil
		mu.Unlock()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","stream":true,"messages":[{"role":"user","content":"hello"}]}`))
		req.Header.Set("x-session-id", session)
		s.Handler().ServeHTTP(rec, req)
		mu.Lock()
		defer mu.Unlock()
		return rec.Code, rec.Header(), rec.Body.String(), append([]string(nil), tried...)
	}
	served := func(t *testing.T, session, model, want string) {
		t.Helper()
		code, _, body, seen := ask(t, session, model)
		if code != 200 || !strings.Contains(body, "from "+want) || strings.Join(seen, " ") != want {
			t.Fatalf("%s %s: %d %s (tried %v), want %s alone", session, model, code, body, seen, want)
		}
	}

	for _, way := range []struct {
		name     string
		affinity string
		cached   bool
		back     string // who the conversation goes to once A is back
	}{
		{"auto", "", false, "a"},
		// In order gives it back to A at its next turn whatever B
		// cached (CryptoSarom on X)
		{"auto, cached on B", "", true, "a"},
		{"session", provider.AffinitySession, false, "b"},
	} {
		t.Run(way.name, func(t *testing.T) {
			if err := provider.SetAffinity("factory", way.affinity); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			cached = way.cached
			mu.Unlock()
			conv := "conv-" + strings.ReplaceAll(way.name, " ", "-")
			usage(t, standardOnlyWindows(40, 30, fiveReset, weekReset, 100), standardOnlyWindows(10, 5, fiveReset, weekReset, 0))
			// A first in the order serves, a Droid Core model too: its
			// Core windows at 100% are shown only
			served(t, conv, "factory/fake-1", "a")
			served(t, conv, "factory/glm-5.3", "a")

			// (a) A's 7 days reaches its 99% cap, its 5 hours far below:
			// held before the request is sent, B takes it, and the
			// conversation on A goes to B
			usage(t, standardOnlyWindows(40, 99, fiveReset, weekReset, 0), standardOnlyWindows(10, 5, fiveReset, weekReset, 100))
			served(t, conv, "factory/fake-1", "b")
			served(t, conv, "factory/fake-claude", "b") // every model counts
			served(t, conv, "factory/glm-5.3", "b")     // Droid Core's too
			served(t, conv+"-new", "factory/fake-1", "b")
			var left []string
			for _, r := range s.Trace(t.Context(), 0, 0).Routes {
				for _, w := range r.Left {
					if w.Capped > 0 && w.CapBack != nil {
						left = append(left, fmt.Sprintf("%s %g/%d", w.Who, w.Used, w.Capped))
					}
				}
			}
			if len(left) == 0 || left[0] != "a@fake 99/99" {
				t.Fatalf("A traced as capped: %v", left)
			}

			// (c) A's 7 days renews: read again at 0%, A serves again; the
			// conversation now on B goes back to A, unless it stays
			usage(t, standardOnlyWindows(40, 0, fiveReset, now.Add(7*24*time.Hour), 0), standardOnlyWindows(10, 5, fiveReset, weekReset, 0))
			served(t, conv, "factory/fake-1", way.back)
			served(t, conv+"-newer", "factory/fake-1", "a")

			// (b) both at their caps, A by its 5 hours, B by its week: no
			// request reaches Factory, the agent is told a 429 rate limit
			// naming each account's share and cap, and when to try again
			usage(t, standardOnlyWindows(99, 0, fiveReset, weekReset, 0), standardOnlyWindows(10, 100, fiveReset, weekReset, 0))
			code, h, body, seen := ask(t, conv, "factory/fake-1")
			if code != 429 || len(seen) != 0 {
				t.Fatalf("both capped: %d %s (tried %v)", code, body, seen)
			}
			for _, want := range []string{"rate_limit", "usage cap reached", "a@fake", "at 99% of a usage window, past its 99% cap", "b@fake", "at 100% of a usage window", "account-cap"} {
				if !strings.Contains(body, want) {
					t.Errorf("no %q in %s", want, body)
				}
			}
			// the sooner back is A, at its 5 hours' reset
			ra, err := strconv.Atoi(h.Get("Retry-After"))
			if err != nil || time.Duration(ra)*time.Second > time.Until(fiveReset)+2*time.Second || time.Duration(ra)*time.Second < time.Until(fiveReset)-time.Minute {
				t.Fatalf("Retry-After %q, want A's 5 hours' reset in %v", h.Get("Retry-After"), time.Until(fiveReset))
			}
			if calls := s.Recent(); len(calls) == 0 || calls[0].Status != 429 || calls[0].Error != "every account at its usage cap" {
				t.Fatalf("request log %+v", calls)
			}

			// (c) a reading from before A's 5 hours renewed, still at 99%:
			// past its reset time, the window has started again
			usage(t, standardOnlyWindows(99, 0, now.Add(-time.Minute), weekReset, 0), standardOnlyWindows(10, 100, fiveReset, weekReset, 0))
			served(t, conv, "factory/fake-1", "a")
		})
	}
}

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// The account Codex is signed in to is out (CodexUsedUp, which makes magpie
// Codex's provider and the Codex app lose its ChatGPT sign-in) only when the
// Codex app itself holds it: the backend says it isn't allowed, and it is at
// a spend cap or past its overage, credits or not, or has no credits to go
// on with, a workspace one past its overage too. A window at
// 100% alone isn't: a Pro account with credits read 100% on its week and
// "allowed": false, and the app kept sending, the backend answering 200,
// while magpie had taken Codex out of ChatGPT ("Sign in to ChatGPT to start
// a durable thread"). The two fixtures are real /wham/usage replies, ids
// replaced.
func TestCodexUsedUpIsWhatTheAppHolds(t *testing.T) {
	read := func(name string) map[string]any {
		t.Helper()
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	with := func(m map[string]any, edit func(m map[string]any)) map[string]any {
		edit(m)
		return m
	}
	credits := func(m map[string]any) map[string]any { return m["credits"].(map[string]any) }
	// a workspace on no credits that the app sends for: within its overage,
	// not at a spend cap, held for its allowance alone
	within := func(plan string, edit func(m map[string]any)) map[string]any {
		return with(read("codex_usage_team_spend_cap.json"), func(m map[string]any) {
			m["plan_type"] = plan
			credits(m)["has_credits"] = false
			m["spend_control"] = map[string]any{"reached": false, "individual_limit": nil}
			m["rate_limit_reached_type"] = map[string]any{"type": "rate_limit_reached", "details": nil}
			edit(m)
		})
	}
	as := func(m map[string]any) {}
	for _, c := range []struct {
		name string
		body map[string]any
		want bool
	}{
		{"Pro at 100% on its week, with credits", read("codex_usage_pro_credits.json"), false},
		{"Team at its spend cap, with credits", read("codex_usage_team_spend_cap.json"), true},
		{"Pro at 100%, with credits, past its overage", with(read("codex_usage_pro_credits.json"), func(m map[string]any) {
			credits(m)["overage_limit_reached"] = true
		}), true},
		{"Pro at 100%, with credits, at a spend cap", with(read("codex_usage_pro_credits.json"), func(m map[string]any) {
			m["spend_control"].(map[string]any)["reached"] = true
		}), true},
		{"Pro at 100%, credits spent", with(read("codex_usage_pro_credits.json"), func(m map[string]any) {
			credits(m)["has_credits"], credits(m)["balance"] = false, "0"
		}), true},
		{"Pro at 100%, credits unlimited", with(read("codex_usage_pro_credits.json"), func(m map[string]any) {
			credits(m)["has_credits"], credits(m)["unlimited"] = false, true
		}), false},
		{"Pro at 100%, no credits said", with(read("codex_usage_pro_credits.json"), func(m map[string]any) {
			delete(m, "credits")
		}), true},
		{"allowed not said", with(read("codex_usage_pro_credits.json"), func(m map[string]any) {
			delete(m, "credits")
			delete(m["rate_limit"].(map[string]any), "allowed")
			delete(m["rate_limit"].(map[string]any), "limit_reached")
		}), false},
		{"allowed", with(read("codex_usage_pro_credits.json"), func(m map[string]any) {
			delete(m, "credits")
			m["rate_limit"].(map[string]any)["allowed"] = true
			m["rate_limit"].(map[string]any)["limit_reached"] = false
		}), false},
		{"allowed, its limit reached, no credits", with(read("codex_usage_pro_credits.json"), func(m map[string]any) {
			delete(m, "credits")
			m["rate_limit"].(map[string]any)["allowed"] = true
		}), true},
		{"allowed, its limit reached, with credits", with(read("codex_usage_pro_credits.json"), func(m map[string]any) {
			m["rate_limit"].(map[string]any)["allowed"] = true
		}), false},
		{"limit reached, allowed not said, no credits", with(read("codex_usage_pro_credits.json"), func(m map[string]any) {
			delete(m, "credits")
			delete(m["rate_limit"].(map[string]any), "allowed")
		}), true},
		// a workspace's included usage still served past its spend cap
		{"Business at its spend cap, still allowed", within("business", func(m map[string]any) {
			m["spend_control"].(map[string]any)["reached"] = true
			m["rate_limit"].(map[string]any)["allowed"] = true
			m["rate_limit"].(map[string]any)["limit_reached"] = false
		}), false},
		{"Business without credits, within its overage", within("business", as), false},
		{"K12 without credits, within its overage", within("k12", as), false},
		{"Pro without credits, its overage open", within("pro", as), true},
		// the app's reserve experiment holds these on no credits, overage
		// or not, and magpie can't see whether it is on
		{"Team without credits, within its overage", within("team", as), true},
		{"Business at its spend cap", within("business", func(m map[string]any) {
			m["spend_control"].(map[string]any)["reached"] = true
		}), true},
		{"Business at its owner's limit", within("business", func(m map[string]any) {
			m["rate_limit_reached_type"].(map[string]any)["type"] = "workspace_owner_usage_limit_reached"
		}), true},
		{"Business past its overage", within("business", func(m map[string]any) {
			credits(m)["overage_limit_reached"] = true
		}), true},
		{"Business, overage not said", within("business", func(m map[string]any) {
			delete(credits(m), "overage_limit_reached")
		}), true},
		{"Team without credits, at its owner's limit", with(read("codex_usage_team_spend_cap.json"), func(m map[string]any) {
			credits(m)["has_credits"] = false
		}), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			signIn(t) // me@example.com, acct-1, the account Codex is on
			rememberLogins(true)
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(c.body)
			}))
			defer fake.Close()
			old := CodexBase
			CodexBase = fake.URL + "/backend-api/codex"
			t.Cleanup(func() { CodexBase = old })
			loginUsageCache.Lock()
			loginUsageCache.m = nil
			loginUsageCache.Unlock()
			if got := CodexUsedUp(context.Background()); got != c.want {
				t.Fatalf("CodexUsedUp = %v, want %v", got, c.want)
			}
		})
	}
}

// A held reading kept for when /wham/usage fails stops saying held once
// the window that held it has started again, as its usage does: else a
// 503 after the reset kept Codex on magpie, out of ChatGPT, with room.
func TestCodexHeldKeptOnlyTillTheReset(t *testing.T) {
	signIn(t)
	rememberLogins(true)
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false
	lastQuotas.Unlock()
	b, err := os.ReadFile("testdata/codex_usage_pro_credits.json")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	cr := body["credits"].(map[string]any)
	cr["has_credits"], cr["balance"] = false, "0"
	body["rate_limit"].(map[string]any)["primary_window"].(map[string]any)["reset_at"] = time.Now().Add(time.Hour).Unix()
	var down atomic.Bool
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			http.Error(w, "upstream", http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(body)
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	read := func() bool {
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
		return CodexUsedUp(context.Background())
	}
	if !read() {
		t.Fatal("CodexUsedUp = false on a held reading")
	}
	down.Store(true)
	if !read() {
		t.Fatal("CodexUsedUp = false on the kept reading before its reset")
	}
	lastQuotas.Lock()
	past := time.Now().Add(-time.Minute)
	for k, e := range lastQuotas.m {
		for i := range e.Q.Windows {
			e.Q.Windows[i].ResetsAt = &past
		}
		lastQuotas.m[k] = e
	}
	lastQuotas.Unlock()
	if read() {
		t.Fatal("CodexUsedUp = true on the kept reading after its window started again")
	}
}

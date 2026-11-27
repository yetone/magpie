package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeCodexResets serves a Codex account's usage with count resets (none
// said at all when count < 0) and the credits' details as given; nil
// details answers them with a 500.
func fakeCodexResets(t *testing.T, count int, details any) *atomic.Int32 {
	t.Helper()
	var asked atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" || r.Header.Get("chatgpt-account-id") != "acct-1" {
			t.Errorf("%s without the account's sign-in", r.URL.Path)
		}
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			body := map[string]any{"plan_type": "pro", "rate_limit": map[string]any{
				"primary_window": map[string]any{"used_percent": 64, "limit_window_seconds": 18000}}}
			if count >= 0 {
				body["rate_limit_reset_credits"] = map[string]any{"available_count": count}
			}
			json.NewEncoder(w).Encode(body)
		case "/backend-api/wham/rate-limit-reset-credits":
			asked.Add(1)
			if details == nil {
				w.WriteHeader(500)
				return
			}
			json.NewEncoder(w).Encode(details)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(fake.Close)
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	return &asked
}

func TestCodexResetsCounted(t *testing.T) {
	home := signIn(t)
	dir := home + "/.codex"
	soon := "2026-10-03T14:30:00Z"
	later := "2026-11-01T00:00:00Z"
	for _, c := range []struct {
		name    string
		count   int
		details any
		want    *ResetCredits
		asked   int32
	}{
		{"none said", -1, nil, nil, 0},
		{"none held", 0, nil, nil, 0},
		{"details unreadable", 2, nil, &ResetCredits{Count: 2}, 1},
		{"never expire", 2, map[string]any{"available_count": 2, "credits": []any{
			map[string]any{"id": "a", "reset_type": "codex_rate_limits", "status": "available", "granted_at": "2026-09-01T00:00:00Z", "expires_at": nil},
			map[string]any{"id": "b", "reset_type": "codex_rate_limits", "status": "available", "granted_at": "2026-09-02T00:00:00Z"},
		}}, &ResetCredits{Count: 2, Each: []ResetCard{{ID: "a"}, {ID: "b"}}}, 1},
		{"soonest of those left", 2, map[string]any{"available_count": 2, "credits": []any{
			map[string]any{"id": "a", "status": "available", "granted_at": "2026-09-01T00:00:00Z", "expires_at": later},
			map[string]any{"id": "b", "status": "redeemed", "granted_at": "2026-09-01T00:00:00Z", "expires_at": "2026-09-20T00:00:00Z"},
			map[string]any{"id": "c", "status": "available", "granted_at": "2026-09-02T00:00:00Z", "expires_at": soon},
			map[string]any{"id": "d", "status": "available", "granted_at": "2026-09-02T00:00:00Z", "expires_at": nil},
		}}, &ResetCredits{Count: 2, Until: ptrTime(soon)}, 1},
		// #960: each one left listed, soonest first, the one that never
		// runs out last; the redeemed one isn't
		{"each of those left", 3, map[string]any{"available_count": 3, "credits": []any{
			map[string]any{"id": "a", "status": "available", "granted_at": "2026-09-01T00:00:00Z", "expires_at": later},
			map[string]any{"id": "b", "status": "redeemed", "granted_at": "2026-09-01T00:00:00Z", "expires_at": "2026-09-20T00:00:00Z"},
			map[string]any{"id": "d", "status": "available", "granted_at": "2026-09-02T00:00:00Z", "expires_at": nil},
			map[string]any{"id": "c", "status": "available", "granted_at": "2026-09-02T00:00:00Z", "expires_at": soon},
		}}, &ResetCredits{Count: 3, Until: ptrTime(soon), Each: []ResetCard{{Until: ptrTime(soon), ID: "c"}, {Until: ptrTime(later), ID: "a"}, {ID: "d"}}}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			asked := fakeCodexResets(t, c.count, c.details)
			q := codexSubscriptionUsage(context.Background(), dir, "")
			if q.Error != "" || len(q.Windows) != 1 {
				t.Fatalf("quota %+v", q)
			}
			if !sameResets(q.Resets, c.want) {
				t.Fatalf("resets %+v, want %+v", q.Resets, c.want)
			}
			if asked.Load() != c.asked {
				t.Fatalf("details asked %d times, want %d", asked.Load(), c.asked)
			}
		})
	}
}

func ptrTime(s string) *time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return &t
}

func sameResets(a, b *ResetCredits) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Count != b.Count || !sameTime(a.Until, b.Until) || len(a.Each) != len(b.Each) {
		return false
	}
	for i := range a.Each {
		if a.Each[i].Window != b.Each[i].Window || a.Each[i].ID != b.Each[i].ID || !sameTime(a.Each[i].Until, b.Each[i].Until) {
			return false
		}
	}
	return true
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func TestConsumeCodexReset(t *testing.T) {
	var got []map[string]string
	answer := map[string]any{}
	status := 200
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/backend-api/wham/rate-limit-reset-credits/consume" ||
			r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("chatgpt-account-id") != "acct-1" {
			t.Errorf("%s %s, auth %q, account %q", r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("chatgpt-account-id"))
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, body)
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(answer)
	}))
	defer fake.Close()
	base := fake.URL + "/backend-api"
	for _, c := range []struct {
		code    string
		windows int
		text    string
	}{
		{"reset", 2, "2 windows started again"},
		{"reset", 1, "1 window started again"},
		{"nothing_to_reset", 0, "nothing to reset — no window has been used, and the reset is kept"},
		{"no_credit", 0, "no reset left on the account"},
		{"already_redeemed", 0, "that reset was already used"},
	} {
		answer = map[string]any{"code": c.code, "windows_reset": c.windows}
		out, err := consumeCodexReset(context.Background(), base, "tok", "acct-1", "cr-1", newRedeemID())
		if err != nil || out.Code != c.code || out.Windows != c.windows || out.Text() != c.text {
			t.Fatalf("%s: %+v %q %v", c.code, out, out.Text(), err)
		}
	}
	// each spend its own idempotency key, a UUID, and names the credit
	if len(got) != 5 || got[0]["redeem_request_id"] == got[1]["redeem_request_id"] || len(got[0]["redeem_request_id"]) != 36 || got[0]["credit_id"] != "cr-1" {
		t.Fatalf("requests %v", got)
	}
	status = 401
	if _, err := consumeCodexReset(context.Background(), base, "tok", "acct-1", "cr-1", newRedeemID()); err == nil {
		t.Fatal("a refused spend said nothing")
	}
	status, answer = 200, map[string]any{}
	if _, err := consumeCodexReset(context.Background(), base, "tok", "acct-1", "cr-1", newRedeemID()); err == nil {
		t.Fatal("an empty answer taken for an outcome")
	}
}

// UseCodexReset spends a reset of the account asked for — the one that
// runs out first — and the next look at the usage reads it afresh.
func TestUseCodexReset(t *testing.T) {
	signIn(t)
	var consumed atomic.Int32
	var spent atomic.Value
	credits := map[string]any{"available_count": 3, "credits": []any{
		map[string]any{"id": "never", "status": "available", "expires_at": nil},
		map[string]any{"id": "late", "status": "available", "expires_at": "2026-12-01T00:00:00Z"},
		map[string]any{"id": "used", "status": "redeemed", "expires_at": "2026-10-01T00:00:00Z"},
		map[string]any{"id": "soon", "status": "available", "expires_at": "2026-10-05T00:00:00Z"},
	}}
	listed := true
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/rate-limit-reset-credits":
			if !listed {
				w.WriteHeader(500)
				return
			}
			json.NewEncoder(w).Encode(credits)
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			if r.Header.Get("chatgpt-account-id") != "acct-1" {
				t.Errorf("spent on account %q", r.Header.Get("chatgpt-account-id"))
			}
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			spent.Store(body["credit_id"])
			consumed.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"code": "reset", "windows_reset": 2})
		default:
			w.WriteHeader(404)
		}
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	subscriptionUsageCache.Lock()
	subscriptionUsageCache.at, subscriptionUsageCache.data = time.Now(), []SubscriptionQuota{{Provider: "codex"}}
	subscriptionUsageCache.Unlock()

	if _, err := UseCodexReset(context.Background(), "nobody@example.com"); err == nil || consumed.Load() != 0 {
		t.Fatalf("spent on an unknown account: %v", err)
	}
	// the menu bar hears the windows started again, and reads them at
	// once rather than at its next tick (#2357)
	var told atomic.Int32
	OnSubscriptionUsage(func() { told.Add(1) })
	t.Cleanup(func() { OnSubscriptionUsage(nil) })
	out, err := UseCodexReset(context.Background(), "me@example.com")
	if err != nil || out.Code != "reset" || out.Windows != 2 || consumed.Load() != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	if told.Load() == 0 {
		t.Fatal("a reset spent didn't tell the menu bar")
	}
	if got := spent.Load(); got != "soon" {
		t.Fatalf("spent %v, not the one that runs out first", got)
	}
	subscriptionUsageCache.Lock()
	stale := subscriptionUsageCache.data == nil && subscriptionUsageCache.at.IsZero()
	subscriptionUsageCache.Unlock()
	if !stale {
		t.Fatal("usage cached from before the reset")
	}

	// only one that never runs out left: that one
	credits = map[string]any{"available_count": 1, "credits": []any{
		map[string]any{"id": "never", "status": "available", "expires_at": nil},
	}}
	if _, err := UseCodexReset(context.Background(), "me@example.com"); err != nil || spent.Load() != "never" {
		t.Fatalf("spent %v: %v", spent.Load(), err)
	}
	// none left: nothing is asked to be spent
	credits = map[string]any{"available_count": 0, "credits": []any{}}
	if out, err := UseCodexReset(context.Background(), "me@example.com"); err != nil || out.Code != "no_credit" || consumed.Load() != 2 {
		t.Fatalf("%+v %v, %d spent", out, err, consumed.Load())
	}
	// which runs out first unknown: none is spent, rather than any
	listed = false
	if _, err := UseCodexReset(context.Background(), "me@example.com"); err == nil || consumed.Load() != 2 {
		t.Fatalf("spent blind: %v", err)
	}
}

// UseCodexResetCard spends the reset the user chose, not the one that
// runs out first (01huadalang on Discord), and only while the account still
// holds it: one used meanwhile, or never the account's, spends none, nor
// any other in its place. No credit named is the one that runs out first.
func TestUseCodexResetCard(t *testing.T) {
	signIn(t)
	var spent []string
	credits := map[string]any{"available_count": 3, "credits": []any{
		map[string]any{"id": "never", "status": "available", "expires_at": nil},
		map[string]any{"id": "late", "status": "available", "expires_at": "2026-12-01T00:00:00Z"},
		map[string]any{"id": "used", "status": "redeemed", "expires_at": "2026-10-01T00:00:00Z"},
		map[string]any{"id": "soon", "status": "available", "expires_at": "2026-10-05T00:00:00Z"},
	}}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backend-api/wham/rate-limit-reset-credits":
			json.NewEncoder(w).Encode(credits)
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			spent = append(spent, body["credit_id"])
			json.NewEncoder(w).Encode(map[string]any{"code": "reset", "windows_reset": 2})
		default:
			w.WriteHeader(404)
		}
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	for _, c := range []struct{ credit, want string }{{"late", "late"}, {"never", "never"}, {"", "soon"}} {
		out, err := UseCodexResetCard(context.Background(), "me@example.com", c.credit)
		if err != nil || out.Code != "reset" || len(spent) == 0 || spent[len(spent)-1] != c.want {
			t.Fatalf("asked for %q: spent %v, %+v %v", c.credit, spent, out, err)
		}
	}
	n := len(spent)
	for _, credit := range []string{"used", "someone-elses"} {
		if _, err := UseCodexResetCard(context.Background(), "me@example.com", credit); err == nil || len(spent) != n {
			t.Fatalf("%q isn't held, yet spent %v: %v", credit, spent[n:], err)
		}
	}
}

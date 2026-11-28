package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// siwcTwinFake is chatgpt.com's backend for one ChatGPT account signed in
// to Codex: its plan's windows at /wham/usage and the apps it let in at
// /wham/usage/chatpass/apps, each answered only for the Codex sign-in's
// own token and account id.
type siwcTwinFake struct {
	mu    sync.Mutex
	asked map[string]int
	apps  []string
	// plan is whether /wham/usage tells the plan's resets and credits too
	plan bool
}

func (f *siwcTwinFake) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[path]
}

// siwcTwinHome is the reporter's machine (tmr11235 on X): Codex signed in
// to a ChatGPT account, and the same account added to magpie through Sign
// in with ChatGPT, whose tokens say what OpenAI's do: a client id, a
// subject and an email, no ChatGPT account, user or plan. The fake's app
// list holds apps.
func siwcTwinHome(t *testing.T, apps ...string) *siwcTwinFake {
	t.Helper()
	isolate(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	exp := float64(time.Now().Add(time.Hour).Unix())
	codexAccess := fakeJWT(map[string]any{"exp": exp, "n": "codex"})
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{"sub": "auth0|u1", "email": "Tmr@Example.com", "https://api.openai.com/auth": map[string]any{
				"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-1", "chatgpt_user_id": "user-1",
				"chatgpt_subscription_active_until": siwcTwinPaid.Format(time.RFC3339)}}),
			"access_token": codexAccess, "refresh_token": "r", "account_id": "acct-1",
		},
	})
	opaque := map[string]any{"per_user_salt": "s", "encrypted_auth_metadata": "e"}
	access := fakeJWT(map[string]any{"exp": exp, "client_id": "oaiapp_mine", "aud": "https://api.openai.com/v1", "https://api.openai.com/auth": opaque})
	id := fakeJWT(map[string]any{"sub": "auth0|u1", "email": "tmr@example.com", "aud": "oaiapp_mine"})
	b, _ := json.Marshal(map[string]any{"clientId": "oaiapp_mine", "hostId": "urn:uuid:1", "sub": "auth0|u1", "email": "tmr@example.com",
		"idToken": id, "access": access, "refresh": "r", "expires": time.Now().Add(time.Hour)})
	saveLogins(t, savedLogin{Agent: ChatGPTAPIID, User: "tmr@example.com", Auth: b})

	f := &siwcTwinFake{asked: map[string]int{}, apps: apps}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.asked[r.URL.Path]++
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+codexAccess || r.Header.Get("chatgpt-account-id") != "acct-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/backend-api/wham/usage":
			u := map[string]any{"plan_type": "plus", "rate_limit": map[string]any{
				"primary_window":   map[string]any{"used_percent": 30, "limit_window_seconds": 18000, "reset_after_seconds": 3600},
				"secondary_window": map[string]any{"used_percent": 40, "limit_window_seconds": 604800, "reset_after_seconds": 86400}}}
			f.mu.Lock()
			if f.plan {
				u["credits"] = map[string]any{"has_credits": true, "unlimited": false, "balance": "12"}
				u["rate_limit_reset_credits"] = map[string]any{"available_count": 2}
			}
			f.mu.Unlock()
			json.NewEncoder(w).Encode(u)
		case "/backend-api/wham/rate-limit-reset-credits":
			json.NewEncoder(w).Encode(map[string]any{"available_count": 2, "credits": []any{
				map[string]any{"id": "rc_1", "reset_type": "full", "status": "available", "expires_at": siwcTwinPaid.Format(time.RFC3339)},
				map[string]any{"id": "rc_2", "reset_type": "full", "status": "available"}}})
		case "/backend-api/wham/usage/chatpass/apps":
			items := []any{}
			for _, a := range f.apps {
				items = append(items, map[string]any{"id": a, "name": "magpie", "allowed_usage_percent": 100,
					"windows": []any{map[string]any{"used_percent": 2, "limit_window_seconds": 604800}}})
			}
			json.NewEncoder(w).Encode(map[string]any{"items": items})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	old := CodexBase
	CodexBase = srv.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })
	return f
}

// siwcTwinPaid is when the fake Codex sign-in's plan is paid until.
var siwcTwinPaid = time.Now().Add(20 * 24 * time.Hour).UTC().Truncate(time.Second)

// tmr11235 on X, after the row showed the plan's windows: "补一下套餐重置卡
// 什么的". Read through Codex's own sign-in (the Codex provider removed),
// the ChatGPT API row shows the plan's resets, credits and paid-until as
// the Codex row would, from the same read: /wham/usage and the resets'
// list, nothing more.
func TestChatGPTAPIRowReadsThePlansResetsThroughCodexsSignIn(t *testing.T) {
	f := siwcTwinHome(t, "oaiapp_mine")
	f.plan = true
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	q := LoginUsageShown(context.Background(), ChatGPTAPIID)["tmr@example.com"]
	if q.Error != "" || len(q.Windows) != 2 {
		t.Fatalf("the ChatGPT API row reads %+v, want the plan's two windows", q)
	}
	if q.Resets == nil || q.Resets.Count != 2 || len(q.Resets.Each) != 2 || q.Resets.Until == nil || !q.Resets.Until.Equal(siwcTwinPaid) {
		t.Fatalf("the plan's resets on the ChatGPT API row: %+v, want the 2 Codex's sign-in holds, the first till %v", q.Resets, siwcTwinPaid)
	}
	if q.Balance != "12 credits" {
		t.Fatalf("the plan's credits on the ChatGPT API row: %q, want 12 credits", q.Balance)
	}
	if q.Until == nil || !q.Until.Equal(siwcTwinPaid) {
		t.Fatalf("the plan is paid until %v on the ChatGPT API row, want %v", q.Until, siwcTwinPaid)
	}
	if q.Held || q.SamePlan != "" {
		t.Fatalf("held %v, samePlan %q: no Codex row is read here, so nothing is used from it", q.Held, q.SamePlan)
	}
	for _, p := range []string{"/backend-api/wham/usage", "/backend-api/wham/rate-limit-reset-credits"} {
		if n := f.count(p); n != 1 {
			t.Fatalf("%s asked %d times, want once", p, n)
		}
	}
}

// The reporter's case: only the ChatGPT API account in magpie, the
// Codex provider removed, so magpie reads no Codex row; Codex itself
// is signed in to the same ChatGPT account. The row reads the plan's five
// hours and week through Codex's sign-in, found by the app's client id
// in that account's app list, the tokens naming no account id.
func TestChatGPTAPIRowReadsCodexsOwnSignInOfTheSameAccount(t *testing.T) {
	f := siwcTwinHome(t, "oaiapp_other", "oaiapp_mine")
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	// Logins("codex") may still hold the sign-in, as seen before the
	// removal; magpie reads no Codex row for it either way
	if !leftAlone("codex") {
		t.Fatal("the Codex provider isn't left alone after its removal")
	}
	q := LoginUsageShown(context.Background(), ChatGPTAPIID)["tmr@example.com"]
	if q.Error != "" || len(q.Windows) != 2 || q.Windows[0].Used != 30 || q.Windows[1].Used != 40 {
		t.Fatalf("the ChatGPT API row reads %+v, want the plan's 5 hours at 30%% and week at 40%%", q)
	}
	if q.Plan != "plus" || q.Provider != ChatGPTAPIID {
		t.Fatalf("the row is %q's with plan %q", q.Provider, q.Plan)
	}
	if n := f.count("/backend-api/wham/usage/chatpass/apps"); n != 1 {
		t.Fatalf("the app list was asked %d times, want once", n)
	}
	// learned: routing and the usage ledger know the pair without asking
	if same := SamePlanAccounts(); same[ChatGPTAPIID+"\x00tmr@example.com"] != "codex\x00tmr@example.com" {
		t.Fatalf("SamePlanAccounts %v", same)
	}
	loginUsageCache.Lock()
	loginUsageCache.m = nil
	loginUsageCache.Unlock()
	LoginUsageShown(context.Background(), ChatGPTAPIID)
	if n := f.count("/backend-api/wham/usage/chatpass/apps"); n != 1 {
		t.Fatalf("the app list was asked %d times after it was known, want once", n)
	}
	// routing reads the same plan's windows, the week 40% used: no more
	// room than Codex's own sign-in has, where it was "not known" before
	resetUsed := func() {
		usedCache.Lock()
		usedCache.m, usedCache.at, usedCache.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
		usedCache.stale = nil
		usedCache.Unlock()
	}
	resetUsed()
	t.Cleanup(resetUsed)
	var a Allowance
	for until := time.Now().Add(10 * time.Second); len(a) == 0 && time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		a = Allowances(ChatGPTAPIID)["tmr@example.com"]
	}
	week := slices.IndexFunc(a, func(l Limit) bool { return l.Span == 7*24*time.Hour })
	if week < 0 || a[week].Used != 40 {
		t.Fatalf("routing reads the ChatGPT API account as %+v, want the plan's week at 40%%", a)
	}
}

// Codex's account listed in magpie, as a Codex row: the ChatGPT API row
// shows that row's reading (samePlanReadings), matched through the app
// list, and the plan's windows are asked once for both rows.
func TestChatGPTAPIRowShowsTheListedCodexSignInMatchedByItsApp(t *testing.T) {
	f := siwcTwinHome(t, "oaiapp_mine")
	rememberLogins(true) // as the look a start makes would
	if ls := Logins("codex"); len(ls) != 1 || !ls[0].Active {
		t.Fatalf("Codex accounts %+v, want Codex's own", ls)
	}
	q := LoginUsageShown(context.Background(), ChatGPTAPIID)["tmr@example.com"]
	if q.Error != "" || len(q.Windows) != 2 || q.Windows[1].Used != 40 || q.SamePlan == "" {
		t.Fatalf("the ChatGPT API row reads %+v, want the Codex row's week at 40%%, samePlan named", q)
	}
	LoginUsageShown(context.Background(), "codex")
	if n := f.count("/backend-api/wham/usage"); n != 1 {
		t.Fatalf("the plan's windows were asked %d times, want once for both rows", n)
	}
}

// A Codex sign-in whose app list hasn't this account's client id, or has
// it twice, is not its twin: the row says where the usage is told, and no
// window is read through another account. The list isn't asked again
// before siwcTwinAgain.
func TestChatGPTAPIRowTakesNoCodexSignInWithoutItsApp(t *testing.T) {
	for _, apps := range [][]string{{"oaiapp_other"}, {"oaiapp_mine", "oaiapp_mine"}, nil} {
		f := siwcTwinHome(t, apps...)
		if err := Delete("codex"); err != nil {
			t.Fatal(err)
		}
		q := LoginUsageShown(context.Background(), ChatGPTAPIID)["tmr@example.com"]
		if q.Error != siwcNoUsage || len(q.Windows) != 0 || q.SamePlan != "" {
			t.Fatalf("apps %v: the row reads %+v, want siwcNoUsage", apps, q)
		}
		if n := f.count("/backend-api/wham/usage"); n != 0 {
			t.Fatalf("apps %v: the plan's windows were read %d times", apps, n)
		}
		if same := SamePlanAccounts(); len(same) != 0 {
			t.Fatalf("apps %v: SamePlanAccounts %v", apps, same)
		}
		loginUsageCache.Lock()
		loginUsageCache.m = nil
		loginUsageCache.Unlock()
		LoginUsageShown(context.Background(), ChatGPTAPIID)
		if n := f.count("/backend-api/wham/usage/chatpass/apps"); n != 1 {
			t.Fatalf("apps %v: the app list was asked %d times, want once", apps, n)
		}
	}
}

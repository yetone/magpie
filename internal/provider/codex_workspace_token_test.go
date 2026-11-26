package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// codexSwitchedTo writes auth.json in dir as Codex keeps LemonFish233's one
// ChatGPT sign-in routed to workspace (codex-rs workspace_switching.rs,
// switch_workspace): the id_token and the refresh token as signed in, on
// the Team seat, and the access token exchanged for one naming workspace
// (/accounts/switch-workspace-token), running out at exp.
func codexSwitchedTo(t *testing.T, dir, workspace, plan string, exp time.Time) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "auth.json"), map[string]any{
		"auth_mode": "chatgpt", "OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{"email": "lemon@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "ws-team", "chatgpt_plan_type": "team", "chatgpt_user_id": "user-lemon"}}),
			"access_token": fakeJWT(map[string]any{"exp": float64(exp.Unix()),
				"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": workspace, "chatgpt_plan_type": plan, "chatgpt_user_id": "user-lemon"}}),
			"refresh_token": "r-lemon", "account_id": workspace,
		},
		"last_refresh": time.Now().UTC().Format(time.RFC3339),
	})
}

// LemonFish233 on X: one email with a Team seat and a personal Plus, both
// on in magpie, Codex signed in to Team. The Plus card read Team's plan
// and week, and a request pinned to Plus came back with Team's rate
// limits: the two workspaces share one refresh token, so renewSaved took
// Plus for the sign-in Codex holds and sent Codex's access token, Team's,
// with Plus's chatgpt-account-id. ChatGPT bills the workspace the token
// names. Each workspace goes with its own access token, and one whose own
// has run out is signed in again, never sent with another's or refreshed
// under Codex.
func TestCodexWorkspaceGoesWithItsOwnAccessToken(t *testing.T) {
	for _, folder := range []string{".codex", ".codex-work"} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/expired=%v", folder, expired), func(t *testing.T) {
				workspaceOwnToken(t, folder, expired)
			})
		}
	}
}

func workspaceOwnToken(t *testing.T, folder string, expired bool) {
	home := signIn(t)
	t.Setenv("CODEX_HOME", "")
	resetLoginUsage(t)
	if folder == ".codex" {
		if err := ForgetLogin("codex", "me@example.com"); err != nil {
			t.Logf("forget: %v", err)
		}
	} // else ~/.codex stays signed in to me@example.com, Codex in ~/.codex-work to LemonFish's
	dir := filepath.Join(home, folder)
	hour := time.Now().Add(time.Hour)
	plusUntil := time.Now().Add(10 * 24 * time.Hour)
	if expired {
		plusUntil = time.Now().Add(-time.Hour)
	}
	codexSwitchedTo(t, dir, "ws-team", "team", hour)
	rememberLogins(true)
	codexSwitchedTo(t, dir, "ws-plus", "plus", plusUntil) // the Codex app's switch
	rememberLogins(true)
	codexSwitchedTo(t, dir, "ws-team", "team", hour) // and back: Codex stays on Team
	rememberLogins(true)
	codexAuth, _ := os.ReadFile(filepath.Join(dir, "auth.json"))

	seats := map[string]struct {
		plan string
		used float64
	}{"ws-team": {"team", 1}, "ws-plus": {"plus", 49}}
	var mu sync.Mutex
	asked := map[string]int{}
	var refreshed []string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			mu.Lock()
			refreshed = append(refreshed, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// as ChatGPT bills it: by the workspace the access token names
		ws := codexTokenWorkspace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if h := r.Header.Get("chatgpt-account-id"); ws != "" && h != ws {
			t.Errorf("%s pinned to %q but sent %q's access token", r.URL.Path, h, ws)
		}
		s, ok := seats[ws]
		if !ok || r.URL.Path != "/backend-api/wham/usage" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mu.Lock()
		asked[ws]++
		mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"plan_type": s.plan, "rate_limit": map[string]any{
			"secondary_window": map[string]any{"used_percent": s.used, "limit_window_seconds": 604800}}})
	}))
	defer fake.Close()
	oldTok, oldBase := codexTokenURL, CodexBase
	t.Cleanup(func() { codexTokenURL, CodexBase = oldTok, oldBase })
	codexTokenURL, CodexBase = fake.URL+"/oauth/token", fake.URL+"/backend-api/codex"
	ForgetAccounts()

	wsOf := map[string]string{}
	var plus string
	for _, l := range Logins("codex") {
		if ws, _ := workspaceOf(t, l.User); ws == "ws-team" || ws == "ws-plus" {
			wsOf[l.User] = ws
			if ws == "ws-plus" {
				plus = l.User
			}
		}
	}
	if len(wsOf) != 2 || plus == "" {
		t.Fatalf("accounts: %v, %+v", wsOf, Logins("codex"))
	}

	// a request pinned to Plus
	tok, acct, err := savedLoginToken(context.Background(), "codex", plus)
	if ws := codexTokenWorkspace(tok); !expired && (err != nil || acct != "ws-plus" || ws != "ws-plus") {
		t.Errorf("a request pinned to Plus goes for %q with %q's access token (%v)", acct, ws, err)
	}
	if expired && (err == nil || tok != "") {
		t.Errorf("Plus, its own access token run out, goes with %q's access token for %q", codexTokenWorkspace(tok), acct)
	}

	signInGone := regexp.MustCompile(`(?i)sign-in has (expired|lapsed)|sign in again`) // app.js's SIGN_IN_GONE
	for user, q := range LoginUsage(context.Background(), "codex") {
		ws, ok := wsOf[user]
		if !ok {
			continue
		}
		want := seats[ws]
		if ws == "ws-plus" && expired {
			if q.Error == "" || !signInGone.MatchString(q.Error) || len(q.Windows) != 0 {
				t.Errorf("Plus, its own access token run out, reads %+v (%q); it is to be signed in again", q.Windows, q.Error)
			}
			continue
		}
		if q.Error != "" || len(q.Windows) == 0 || q.Windows[0].Used != want.used || q.Plan != want.plan {
			t.Errorf("%s (%s) reads plan %q, %+v (%s); its workspace has %q, %v%% used", user, ws, q.Plan, q.Windows, q.Error, want.plan, want.used)
		}
	}
	mu.Lock()
	if asked["ws-team"] == 0 || !expired && asked["ws-plus"] == 0 || expired && asked["ws-plus"] != 0 {
		t.Errorf("workspaces read: %v", asked)
	}
	if len(refreshed) != 0 {
		t.Errorf("the shared sign-in was refreshed %d times by magpie", len(refreshed))
	}
	mu.Unlock()
	if b, _ := os.ReadFile(filepath.Join(dir, "auth.json")); string(b) != string(codexAuth) {
		t.Errorf("Codex's sign-in was rewritten:\n%s\nwas\n%s", b, codexAuth)
	}
	for _, l := range Logins("codex") {
		if l.User == plus && expired != (l.Lapsed != "") {
			t.Errorf("Plus's row says lapsed %q", l.Lapsed)
		}
	}

	// Codex's Team token about to run out: Plus isn't what Codex holds, so
	// Codex's sign-in is not renewed for it
	codexSwitchedTo(t, dir, "ws-team", "team", time.Now().Add(2*time.Minute))
	rememberLogins(true)
	tok, _, err = savedLoginToken(context.Background(), "codex", plus)
	if ws := codexTokenWorkspace(tok); expired == (err == nil) || !expired && ws != "ws-plus" {
		t.Errorf("Plus beside Codex's running-out Team token goes with %q's token (%v)", ws, err)
	}
	mu.Lock()
	if len(refreshed) != 0 {
		t.Errorf("Codex's sign-in was renewed %d times for Plus", len(refreshed))
	}
	mu.Unlock()
	if !expired {
		return
	}

	// Codex switched to Plus again: a fresh Plus token, saved for it, and
	// the row is in use again
	codexSwitchedTo(t, dir, "ws-plus", "plus", hour)
	rememberLogins(true)
	for _, l := range Logins("codex") {
		if l.User == plus && l.Lapsed != "" {
			t.Errorf("Plus is still lapsed with Codex on it: %q", l.Lapsed)
		}
	}
	if tok, acct, err := savedLoginToken(context.Background(), "codex", plus); err != nil || acct != "ws-plus" || codexTokenWorkspace(tok) != "ws-plus" {
		t.Errorf("Plus with Codex on it goes for %q with %q's token (%v)", acct, codexTokenWorkspace(tok), err)
	}
}

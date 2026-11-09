package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// codexAppSignIn writes auth.json as the Codex app keeps one ChatGPT
// sign-in routed to workspace: the tokens of a single sign-in, its
// refresh token the same whichever workspace is picked. Real tokens carry
// the workspace in id_token's and access_token's
// https://api.openai.com/auth claims and in tokens.account_id; reclaimed
// says the switch got tokens naming the new workspace, else only
// account_id was rewritten.
func codexAppSignIn(t *testing.T, home, workspace, plan string, reclaimed bool) {
	t.Helper()
	claimWS, claimPlan := "ws-personal", "prolite" // as signed in
	if reclaimed {
		claimWS, claimPlan = workspace, plan
	}
	auth := map[string]any{"chatgpt_account_id": claimWS, "chatgpt_plan_type": claimPlan, "chatgpt_user_id": "user-jd"}
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt", "OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"id_token": fakeJWT(map[string]any{"email": "jd@example.com", "sid": "sid-jd",
				"https://api.openai.com/auth": auth}),
			"access_token": fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "session_id": "sid-jd",
				"https://api.openai.com/auth": auth}),
			"refresh_token": "r-jd", "account_id": workspace,
		},
		"last_refresh": time.Now().UTC().Format(time.RFC3339),
	})
}

// JdAZzzzzz on X: one ChatGPT login with a personal Pro and a Team seat,
// both in magpie, the workspace switched in the Codex app. Both cards
// read the personal plan ("Prolite"), its week, its reset time and its
// two resets: the account not in use shares the sign-in Codex holds (one
// refresh token), and was read with Codex's sign-in as it is, workspace
// and all. Each card is its own workspace's.
func TestCodexAppWorkspaceSwitchReadsEachWorkspace(t *testing.T) {
	for _, reclaimed := range []bool{false, true} {
		for _, backHome := range []bool{false, true} {
			t.Run(fmt.Sprintf("reclaimed=%v/backHome=%v", reclaimed, backHome), func(t *testing.T) {
				workspaceSwitchReadsApart(t, reclaimed, backHome)
			})
		}
	}
}

func workspaceSwitchReadsApart(t *testing.T, reclaimed, backHome bool) {
	home := signIn(t)
	if err := ForgetLogin("codex", "me@example.com"); err != nil {
		t.Logf("forget: %v", err)
	}
	codexAppSignIn(t, home, "ws-personal", "prolite", reclaimed)
	rememberLogins(true)
	codexAppSignIn(t, home, "ws-team", "team", reclaimed) // the Codex app's switch
	rememberLogins(true)
	if backHome {
		codexAppSignIn(t, home, "ws-personal", "prolite", reclaimed)
		rememberLogins(true)
	}

	type seat struct {
		plan   string
		used   float64
		resets int
	}
	seats := map[string]seat{"ws-personal": {"prolite", 25, 2}, "ws-team": {"team", 60, 0}}
	// LoginUsage reads the accounts at once, so the fake is asked from two
	// handlers together: unguarded, a write could be lost (#5819, only
	// ws-personal counted) or the map broken ("concurrent map writes")
	var askedMu sync.Mutex
	asked := map[string]int{}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws := r.Header.Get("chatgpt-account-id")
		s, ok := seats[ws]
		if !ok {
			t.Errorf("%s asked for workspace %q", r.URL.Path, ws)
			w.WriteHeader(403)
			return
		}
		if r.URL.Path != "/backend-api/wham/usage" {
			w.WriteHeader(404)
			return
		}
		askedMu.Lock()
		asked[ws]++
		askedMu.Unlock()
		body := map[string]any{"plan_type": s.plan, "rate_limit": map[string]any{
			"secondary_window": map[string]any{"used_percent": s.used, "limit_window_seconds": 604800}}}
		if s.resets > 0 {
			body["rate_limit_reset_credits"] = map[string]any{"available_count": s.resets}
		}
		json.NewEncoder(w).Encode(body)
	}))
	defer fake.Close()
	old := CodexBase
	CodexBase = fake.URL + "/backend-api/codex"
	t.Cleanup(func() { CodexBase = old })

	ls := Logins("codex")
	if len(ls) != 2 {
		t.Fatalf("accounts: %+v", ls)
	}
	wsOf := map[string]string{}
	for _, l := range ls {
		wsOf[l.User], _ = workspaceOf(t, l.User)
	}
	t.Logf("accounts: %v", wsOf)

	for _, l := range ls {
		if l.Active {
			continue
		}
		_, acct, err := savedLoginToken(context.Background(), "codex", l.User)
		if err != nil || acct != wsOf[l.User] {
			t.Errorf("%s (not in use) signs its requests for %q (%v), its workspace is %s", l.User, acct, err, wsOf[l.User])
		}
	}
	for user, q := range LoginUsage(context.Background(), "codex") {
		want := seats[wsOf[user]]
		resets := 0
		if q.Resets != nil {
			resets = q.Resets.Count
		}
		if q.Error != "" || len(q.Windows) == 0 || q.Windows[0].Used != want.used || q.Plan != want.plan || resets != want.resets {
			t.Errorf("%s (%s) reads plan %q, %+v, %d resets (%s); its workspace has %q, %v%% used, %d resets",
				user, wsOf[user], q.Plan, q.Windows, resets, q.Error, want.plan, want.used, want.resets)
		}
	}
	askedMu.Lock()
	if asked["ws-team"] == 0 || asked["ws-personal"] == 0 {
		t.Errorf("workspaces read: %v", asked)
	}
	askedMu.Unlock()

	// carried to another machine, each account keeps its own workspace:
	// Codex's sign-in here goes only on the record it is
	s, err := CollectSignIns()
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range s.Logins {
		if l.Agent != "codex" {
			continue
		}
		if _, ws := codexWho(l.Auth); ws != wsOf[l.User] {
			t.Errorf("%s is carried as a sign-in to %s, its workspace is %s", l.User, ws, wsOf[l.User])
		}
	}
}

package provider

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/plugin"
)

// Signing in to an account removed from magpie shows it again before the
// sign-in reads as done, so the window, seeing it done, finds the account
// listed rather than saying it can't list it (TestClaudeTeamSeatBesidePersonal
// found it missing on macOS in #1046's CI). Each way a sign-in finishes is
// checked: magpie's own callback (Codex), an agent's login command run by
// magpie (Claude) and a plugin's.
func TestSignInDoneOnceTheAccountIsBack(t *testing.T) {
	for _, c := range []struct {
		agent string
		// setUp signs in to agent, which is then removed from magpie
		setUp func(t *testing.T)
		// signIn starts the sign-in again, and finish does what the
		// browser, or the code its page shows, does then
		signIn func(t *testing.T) SignInState
		finish func(t *testing.T, st SignInState)
	}{{
		agent: "codex",
		setUp: fakeCodexSignIn,
		signIn: func(t *testing.T) SignInState {
			st, err := StartSignIn("codex")
			if err != nil {
				t.Fatal(err)
			}
			return st
		},
		finish: func(t *testing.T, st SignInState) { finishInBrowser(t, st, "code") },
	}, {
		agent: "claude",
		setUp: func(t *testing.T) {
			home := claudeHome(t)
			noAnthropic(t)
			fakeClaudeLogin(t, fakeClaudeAccount{email: "new@example.com", name: "New", plan: "max", refresh: "sk-ant-ort01-new"}, "", true)
			claudeSignIn(t, home, time.Now().Add(time.Hour))
			writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "old@example.com"}})
		},
		signIn: func(t *testing.T) SignInState {
			st, err := StartSignIn("claude")
			if err != nil {
				t.Fatal(err)
			}
			return st
		},
		finish: func(t *testing.T, st SignInState) { finishInBrowser(t, st, "the-code") },
	}, {
		agent: "fakeco",
		setUp: func(t *testing.T) {
			bun, err := exec.LookPath("bun")
			if err != nil {
				t.Skip("no bun on PATH")
			}
			claudeHome(t)
			t.Setenv("MAGPIE_BUN", bun)
			t.Cleanup(plugin.Settle)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			abs, _ := filepath.Abs("../plugin/testdata/fake/index.js")
			if _, err := plugin.Add(ctx, abs); err != nil {
				t.Fatal(err)
			}
			if _, err := plugin.Providers(ctx); err != nil {
				t.Fatal(err)
			}
			st, err := StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": "blue"})
			if err != nil {
				t.Fatal(err)
			}
			if err := SubmitSignInCallback(st.ID, "good"); err != nil {
				t.Fatal(err)
			}
		},
		signIn: func(t *testing.T) SignInState {
			st, err := StartPluginSignIn("fakeco", 1, map[string]string{"where": "work", "team": "blue"})
			if err != nil {
				t.Fatal(err)
			}
			return st
		},
		finish: func(t *testing.T, st SignInState) {
			if err := SubmitSignInCallback(st.ID, "good"); err != nil {
				t.Fatal(err)
			}
		},
	}} {
		t.Run(c.agent, func(t *testing.T) {
			c.setUp(t)
			if err := Delete(c.agent); err != nil {
				t.Fatal(err)
			}
			if _, ok := find(All(), c.agent); ok {
				t.Fatal("removed account still listed")
			}

			// what the sign-in reads as the moment its account is shown again
			var mu sync.Mutex
			id, back := "", ""
			was := catalog.Changed
			catalog.Changed = func() {
				mu.Lock()
				defer mu.Unlock()
				if id != "" && back == "" && !hiddenAccount(c.agent) {
					st, _ := SignInStatus(id)
					back = st.State
				}
			}
			t.Cleanup(func() { catalog.Changed = was })

			st := c.signIn(t)
			mu.Lock()
			id = st.ID
			mu.Unlock()
			c.finish(t, st)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if st, err := WaitSignIn(ctx, st.ID); err != nil || st.State != "done" {
				t.Fatalf("sign-in %+v, %v", st, err)
			}
			mu.Lock()
			got := back
			mu.Unlock()
			switch got {
			case "":
				t.Fatal("signing in didn't show the removed account again")
			case "done":
				t.Fatal("the sign-in read as done before its account was shown again: the window, seeing it done, can't list it")
			}
			if _, ok := find(All(), c.agent); !ok {
				t.Fatal("the account isn't listed after its sign-in")
			}
		})
	}
}

// A sign-in canceled while its account is shown again, as the window still
// offers Cancel until it reads as done, ends done all the same: the account
// was signed in to and saved already, and a sign-in records one outcome.
func TestSignInCanceledWhileTheAccountComesBack(t *testing.T) {
	fakeCodexSignIn(t)
	if err := Delete("codex"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	id, canceled := "", false
	was := catalog.Changed
	catalog.Changed = func() {
		mu.Lock()
		defer mu.Unlock()
		if id != "" && !canceled && !hiddenAccount("codex") {
			canceled = true
			CancelSignIn(id)
		}
	}
	t.Cleanup(func() { catalog.Changed = was })

	st, err := StartSignIn("codex")
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	id = st.ID
	mu.Unlock()
	finishInBrowser(t, st, "code")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if st, err := WaitSignIn(ctx, st.ID); err != nil || st.State != "done" || st.User != "me@example.com" {
		t.Fatalf("sign-in %+v, %v", st, err)
	}
	mu.Lock()
	shown := canceled
	mu.Unlock()
	if !shown {
		t.Fatal("signing in didn't show the removed account again")
	}
	if _, ok := find(All(), "codex"); !ok {
		t.Fatal("the account isn't listed after its sign-in")
	}
}

// fakeCodexSignIn signs in to Codex (me@example.com, personal Pro,
// workspace acct-1) and points its sign-in at a token server that signs in
// to that account again, coming back to a free port.
func fakeCodexSignIn(t *testing.T) {
	t.Helper()
	signIn(t)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id_token": fakeJWT(map[string]any{"email": "me@example.com",
				"https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "pro", "chatgpt_account_id": "acct-1"}}),
			"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix())}),
			"refresh_token": "r2",
		})
	}))
	t.Cleanup(fake.Close)
	oldTok, oldAddr := codexTokenURL, codexCallbackAddr
	t.Cleanup(func() { codexTokenURL, codexCallbackAddr = oldTok, oldAddr })
	codexTokenURL = fake.URL
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	codexCallbackAddr = ln.Addr().String()
	ln.Close()
}

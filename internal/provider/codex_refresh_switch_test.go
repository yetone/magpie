package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A refresh of Codex's sign-in is a network round trip, and Codex can be
// signed in to another account before it answers: by a switch (magpie's
// own, as when the account in use is spent), or by the Codex app's own
// sign-in. The refresh then never writes the account it started from back
// over that one (darwish4021 on Discord: magpie "switched my account"),
// and a switch waits for a refresh in progress, so the account it saves
// as the one replaced holds the refresh token the refresh got, not the
// one it spent.
func TestCodexRefreshNeverUndoesASwitch(t *testing.T) {
	for _, by := range []string{"codex", "magpie"} {
		t.Run(by, func(t *testing.T) {
			home := signIn(t) // me@example.com, refresh token "r"
			rememberLogins(true)
			dir := filepath.Join(home, ".codex")
			// work@example.com signed in, its access token expired
			writeFile(t, filepath.Join(dir, "auth.json"), map[string]any{
				"auth_mode": "chatgpt",
				"tokens": map[string]any{
					"id_token":      fakeJWT(map[string]any{"email": "work@example.com", "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-work"}}),
					"access_token":  fakeJWT(map[string]any{"exp": float64(time.Now().Add(-time.Minute).Unix())}),
					"refresh_token": "r-work", "account_id": "acct-work",
				},
			})
			rememberLogins(true) // both saved, work in use

			switched := make(chan error, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]string
				json.NewDecoder(r.Body).Decode(&body)
				if body["refresh_token"] != "r-work" {
					w.WriteHeader(400)
					return
				}
				switch by {
				case "codex":
					// the Codex app signed in to me@example.com meanwhile
					codexSignIn(t, home, "me@example.com", "r")
					switched <- nil
				case "magpie":
					// magpie switched to me@example.com meanwhile; with
					// the refresh waited for, the switch is still waiting
					// when the answer goes
					go func() { _, err := switchSavedLogin("codex", "me@example.com"); switched <- err }()
					select {
					case <-switched:
						switched <- nil // done before the refresh was: the old way
					case <-time.After(time.Second):
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"access_token": fakeJWT(map[string]any{"exp": float64(time.Now().Add(time.Hour).Unix()), "n": "fresh"}), "refresh_token": "r-work-2"})
			}))
			defer srv.Close()
			old := codexTokenURL
			codexTokenURL = srv.URL
			t.Cleanup(func() { codexTokenURL = old })

			if _, _, err := codexTokenRenewing(context.Background(), dir, ""); err != nil {
				t.Fatal(err)
			}
			if err := <-switched; err != nil {
				t.Fatal(err)
			}
			var a codexAuth
			readJSON(filepath.Join(dir, "auth.json"), &a)
			if a.Tokens.RefreshToken != "r" {
				t.Fatalf("Codex is signed in to %q after the refresh, not the account switched to", a.Tokens.RefreshToken)
			}
			if by == "magpie" {
				for _, l := range readLogins() {
					if l.User == "work@example.com" && !strings.Contains(string(l.Auth), "r-work-2") {
						t.Fatalf("the account switched off was saved with the refresh token its refresh spent: %s", l.Auth)
					}
				}
			}
		})
	}
}

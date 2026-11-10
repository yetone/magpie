package provider

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// accessOnlyLogin is the saved account user as Logins has it.
func accessOnlyLogin(t *testing.T, user string) Login {
	t.Helper()
	for _, l := range Logins("codex") {
		if l.User == user {
			return l
		}
	}
	t.Fatalf("no saved account %s", user)
	return Login{}
}

// setSavedAccess gives the saved account user the access token tok.
func setSavedAccess(t *testing.T, user, tok string) {
	t.Helper()
	err := editSideLogin("codex", user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		var raw map[string]any
		if err := json.Unmarshal(ls[i].Auth, &raw); err != nil {
			return nil, err
		}
		raw["tokens"].(map[string]any)["access_token"] = tok
		ls[i].Auth, _ = json.Marshal(raw)
		return ls, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// An account kept access token only is never refreshed (EastonXvii on X):
// not by a usage read whose token the backend turns away, not by the
// keep-alive, not by a forced renewal, not by a request's renewal once
// the token has run out. It lapses instead, saying to import it again.
func TestCodexAccessOnlyNeverRefreshed(t *testing.T) {
	_, refreshes, _ := codexExpiredFakes(t, staleExpired, false)
	if err := SetLoginAccessOnly("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if l := accessOnlyLogin(t, "me@example.com"); !l.AccessOnly || l.Lapsed != "" {
		t.Fatalf("after turning it on: %+v", l)
	}

	// the keep-alive passes it over, run as the gateway runs it
	for _, r := range renewLogins(context.Background(), 0, 0) {
		if r.User == "me@example.com" {
			t.Fatalf("the keep-alive renewed it: %+v", r)
		}
	}
	// a forced renewal (a revoked sign-in's check) is refused, lapsing nothing
	if _, _, err := renewSavedLogin(context.Background(), "codex", "me@example.com", true); err == nil {
		t.Fatal("a forced renewal went through")
	}
	if l := accessOnlyLogin(t, "me@example.com"); l.Lapsed != "" {
		t.Fatalf("a forced renewal lapsed it: %q", l.Lapsed)
	}
	// a request's token is the one it was given, while it has time left
	if tok, _, err := savedLoginToken(context.Background(), "codex", "me@example.com"); err != nil || tok == "" || strings.HasPrefix(tok, "fresh-") {
		t.Fatalf("token %q, %v", tok, err)
	}

	// usage reads: the backend turns its token away, which lapses it
	// rather than refreshing it
	u := LoginUsage(context.Background(), "codex")
	if u["work@example.com"].Error != "" {
		t.Fatalf("Codex's own account: %+v", u["work@example.com"])
	}
	for _, r := range refreshes() {
		if r == "r" {
			t.Fatalf("refreshed the access-only account: %q", refreshes())
		}
	}
	l := accessOnlyLogin(t, "me@example.com")
	if l.Lapsed != accessOnlyLapse("me@example.com") {
		t.Fatalf("lapsed %q", l.Lapsed)
	}
	if _, _, err := savedLoginToken(context.Background(), "codex", "me@example.com"); !errors.As(err, new(refreshRefused)) {
		t.Fatalf("after its token was turned away: %v", err)
	}
	for _, r := range refreshes() {
		if r == "r" {
			t.Fatalf("refreshed the access-only account: %q", refreshes())
		}
	}

	// turned off again, it is renewed again: the lapse it had is cleared
	if err := SetLoginAccessOnly("codex", "me@example.com", false); err != nil {
		t.Fatal(err)
	}
	if l := accessOnlyLogin(t, "me@example.com"); l.AccessOnly || l.Lapsed != "" {
		t.Fatalf("after turning it off: %+v", l)
	}
}

// One whose access token has run out by its own exp lapses at once,
// on its row and at its next request, with no refresh.
func TestCodexAccessOnlyRunOutLapses(t *testing.T) {
	_, refreshes, _ := codexExpiredFakes(t, staleExpired, false)
	if err := SetLoginAccessOnly("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	setSavedAccess(t, "me@example.com", fakeJWT(map[string]any{"exp": float64(time.Now().Add(-time.Minute).Unix())}))
	loginsSeenAt.Store(0)
	if l := accessOnlyLogin(t, "me@example.com"); l.Lapsed == "" {
		t.Fatal("its row doesn't say its access token ran out")
	}
	if _, _, err := savedLoginToken(context.Background(), "codex", "me@example.com"); !errors.As(err, new(refreshRefused)) {
		t.Fatalf("a run-out token: %v", err)
	}
	if got := refreshes(); len(got) != 0 {
		t.Fatalf("refreshed %q", got)
	}
}

// Codex is never signed in to it, which would refresh it; nor is the
// account Codex renews itself kept access token only.
func TestCodexAccessOnlyNotSwitchedTo(t *testing.T) {
	codexExpiredFakes(t, staleExpired, false)
	if err := SetLoginAccessOnly("codex", "work@example.com", true); err == nil {
		t.Fatal("the account Codex is signed in to was kept access token only")
	}
	if err := SetLoginAccessOnly("codex", "me@example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := SwitchLogin("codex", "me@example.com"); err == nil || !strings.Contains(err.Error(), "access token only") {
		t.Fatalf("switched Codex to it: %v", err)
	}
	if switchable(accessOnlyLogin(t, "me@example.com")) {
		t.Fatal("offered as an account to switch Codex to")
	}
}

// Imported access token only, a file's account is kept as it is, with
// nothing sent to ChatGPT's token endpoint, so the sign-in it came from
// stays signed in. A run-out access token isn't taken; one with no
// refresh token is.
func TestCodexImportAccessOnly(t *testing.T) {
	_, refreshes, _ := codexExpiredFakes(t, staleExpired, false)
	file := func(email, refresh string, exp time.Time) string {
		tokens := map[string]any{
			"id_token":     fakeJWT(map[string]any{"email": email, "https://api.openai.com/auth": map[string]any{"chatgpt_plan_type": "plus", "chatgpt_account_id": "acct-" + email}}),
			"access_token": fakeJWT(map[string]any{"exp": float64(exp.Unix()), "n": email}),
			"account_id":   "acct-" + email,
		}
		if refresh != "" {
			tokens["refresh_token"] = refresh
		}
		b, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": tokens})
		return string(b)
	}
	later := time.Now().Add(48 * time.Hour)
	res, err := ImportCodexAccessOnly(context.Background(), []string{
		file("a@example.com", "r-a", later),
		file("b@example.com", "", later),
		file("c@example.com", "r-c", time.Now().Add(-time.Hour)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 || res[0].Status != "added" || res[1].Status != "added" || res[2].Status != "failed" {
		t.Fatalf("results %+v", res)
	}
	if got := refreshes(); len(got) != 0 {
		t.Fatalf("the import refreshed %q", got)
	}
	for _, u := range []string{"a@example.com", "b@example.com"} {
		if l := accessOnlyLogin(t, u); !l.AccessOnly || l.Active || l.Lapsed != "" {
			t.Fatalf("%s: %+v", u, l)
		}
	}
	// the same file again is the same account
	res, err = ImportCodexAccessOnly(context.Background(), []string{file("a@example.com", "r-a", later)})
	if err != nil || len(res) != 1 || res[0].Status != "exists" {
		t.Fatalf("again: %+v, %v", res, err)
	}
	// an ordinary import still refreshes, as it did
	if _, err := ImportLogins(context.Background(), "codex", []string{file("d@example.com", "r-d", later)}); err != nil {
		t.Fatal(err)
	}
	if got := refreshes(); len(got) != 1 || got[0] != "r-d" {
		t.Fatalf("an ordinary import refreshed %q", got)
	}
}

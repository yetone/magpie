package provider

import (
	"path/filepath"
	"testing"
	"time"
)

// signInAlertsOf is what a look at the accounts calls for now, the marks
// kept in path.
func signInAlertsOf(t *testing.T, path string, now time.Time) []SignInAlert {
	t.Helper()
	forgetAccountCaches()
	return checkSignIns(path, signInStates(), now)
}

// setSideClaude writes the saved Claude account side back as it is now.
func setSideClaude(t *testing.T, side savedLogin) {
	t.Helper()
	writeFile(t, loginsPath(), []savedLogin{side})
	forgetAccountCaches()
}

// A Claude account in use whose sign-in Claude Code couldn't use is told
// of once, with the provider a click opens, and again only after it was
// usable in between (steven_4422 on Discord: nothing said so outside the
// account's row).
func TestSignInAlertClaudeSignedOut(t *testing.T) {
	_, side := claudeAuthFixture(t, claudeAuthLapse)
	path := filepath.Join(t.TempDir(), "signin-alerts.json")
	now := time.Now()
	got := signInAlertsOf(t, path, now)
	if len(got) != 1 || got[0].Kind != "lapsed" || got[0].Provider != "claude" || got[0].User != side.User || got[0].Name == "" {
		t.Fatalf("alerts for a signed-out Claude account: %+v", got)
	}
	if got := signInAlertsOf(t, path, now.Add(5*time.Minute)); len(got) != 0 {
		t.Fatalf("told again: %+v", got)
	}
	side.Lapsed = ""
	setSideClaude(t, side)
	if got := signInAlertsOf(t, path, now.Add(10*time.Minute)); len(got) != 0 {
		t.Fatalf("a usable account told of: %+v", got)
	}
	side.Lapsed = claudeAuthLapse
	setSideClaude(t, side)
	if got := signInAlertsOf(t, path, now.Add(15*time.Minute)); len(got) != 1 || got[0].Kind != "lapsed" {
		t.Fatalf("signed out again, not told: %+v", got)
	}
}

// An account not in use is told of nothing: no request goes to it.
func TestSignInAlertSkipsAccountsNotInUse(t *testing.T) {
	_, side := claudeAuthFixture(t, claudeAuthLapse)
	side.On = false
	setSideClaude(t, side)
	if got := signInAlertsOf(t, filepath.Join(t.TempDir(), "a.json"), time.Now()); len(got) != 0 {
		t.Fatalf("an account not in use told of: %+v", got)
	}
}

// A Claude sign-in whose refresh token ends within three days is told of
// once for that end, as Claude Code's own "Your login expires in N days"
// is; one further off, or signed in anew, is not.
func TestSignInAlertClaudeEnding(t *testing.T) {
	_, side := claudeAuthFixture(t, "")
	now := time.Now()
	creds := func(ends time.Time) {
		side.Auth = mustJSONRaw(t, map[string]any{"claudeAiOauth": map[string]any{
			"accessToken": "old-access", "refreshToken": "old-refresh",
			"expiresAt": now.Add(time.Hour).UnixMilli(), "refreshTokenExpiresAt": ends.UnixMilli(), "subscriptionType": "max",
		}})
		setSideClaude(t, side)
	}
	path := filepath.Join(t.TempDir(), "signin-alerts.json")
	creds(now.Add(20 * 24 * time.Hour))
	if got := signInAlertsOf(t, path, now); len(got) != 0 {
		t.Fatalf("told of a sign-in 20 days from its end: %+v", got)
	}
	ends := now.Add(2*24*time.Hour + time.Hour)
	creds(ends)
	got := signInAlertsOf(t, path, now)
	if len(got) != 1 || got[0].Kind != "ending" || got[0].User != side.User || got[0].EndsAt == nil || !got[0].EndsAt.Equal(time.UnixMilli(ends.UnixMilli())) {
		t.Fatalf("alerts for a sign-in ending in 2 days: %+v", got)
	}
	if got := signInAlertsOf(t, path, now.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("told again of the same end: %+v", got)
	}
	creds(now.Add(30 * 24 * time.Hour)) // signed in anew
	if got := signInAlertsOf(t, path, now.Add(2*time.Hour)); len(got) != 0 {
		t.Fatalf("told of a new sign-in: %+v", got)
	}
	creds(now.Add(24 * time.Hour))
	if got := signInAlertsOf(t, path, now.Add(3*time.Hour)); len(got) != 1 || got[0].Kind != "ending" {
		t.Fatalf("the new sign-in's end not told: %+v", got)
	}
}

// Claude Code's own rule: no end said, or an access token that outlasts
// the refresh token's end by more than the warning, is no end to tell.
func TestClaudeSignInEnds(t *testing.T) {
	now := time.Now()
	blob := func(o map[string]any) []byte {
		o["accessToken"], o["refreshToken"] = "a", "r"
		return mustJSONRaw(t, map[string]any{"claudeAiOauth": o})
	}
	if at, ok := claudeSignInEnds(blob(map[string]any{"expiresAt": now.Add(time.Hour).UnixMilli(), "refreshTokenExpiresAt": now.Add(48 * time.Hour).UnixMilli()})); !ok || at.UnixMilli() != now.Add(48*time.Hour).UnixMilli() {
		t.Fatalf("end not read: %v %v", at, ok)
	}
	if _, ok := claudeSignInEnds(blob(map[string]any{"expiresAt": now.Add(time.Hour).UnixMilli()})); ok {
		t.Fatal("an end read where none is said")
	}
	if _, ok := claudeSignInEnds(blob(map[string]any{"expiresAt": now.Add(10 * 24 * time.Hour).UnixMilli(), "refreshTokenExpiresAt": now.Add(time.Hour).UnixMilli()})); ok {
		t.Fatal("an end read under an access token that outlasts it")
	}
}

// Every subscription whose row says Sign in again is told of, as the
// Providers page decides it: a Claude, Copilot or plugin account's mark,
// and a built-in's that says its sign-in is gone. A failure of the moment
// is not.
func TestSignInGoneAsThePageSaysIt(t *testing.T) {
	for _, c := range []struct {
		agent  string
		plugin bool
		lapsed string
		want   bool
	}{
		{"claude", false, claudeAuthLapse, true},
		{"copilot", false, copilotLapsedText, true},
		{"cursor", true, "Cursor refused this sign-in", true},
		{"codex", false, revokedText("a@example.com"), true},
		{"codex", false, "a@example.com's sign-in has expired — add the account again to use it", true},
		{"chatgpt-api", false, "a@example.com's ChatGPT sign-in has expired — sign in again", true},
		{"codex", false, "usage read failed: HTTP 502", false},
		{"codex", false, "", false},
		{"claude", false, "", false},
	} {
		if got := needsSignIn(c.agent, c.plugin, c.lapsed); got != c.want {
			t.Errorf("needsSignIn(%q, %v, %q) = %v", c.agent, c.plugin, c.lapsed, got)
		}
	}
}

// The marks: one alert per account and kind, cleared when the account is
// usable again; an account not listed keeps its marks a while, so a plugin
// not started yet isn't told of again when it is.
func TestDueSignIns(t *testing.T) {
	now := time.Now()
	marks := map[string]alertMark{}
	gone := []signInState{{Provider: "cursor", Name: "Cursor", User: "U@x", Gone: true}, {Provider: "codex", Name: "Codex", User: "b"}}
	if got := dueSignIns(gone, marks, now); len(got) != 1 || got[0].Provider != "cursor" {
		t.Fatalf("first look: %+v", got)
	}
	if got := dueSignIns(nil, marks, now.Add(time.Hour)); len(got) != 0 || len(marks) != 1 {
		t.Fatalf("not listed: %+v, marks %v", got, marks)
	}
	if got := dueSignIns(gone, marks, now.Add(2*time.Hour)); len(got) != 0 {
		t.Fatalf("told again after a look it wasn't listed in: %+v", got)
	}
	if dueSignIns(nil, marks, now.Add(alertKeep+3*time.Hour)); len(marks) != 0 {
		t.Fatalf("a mark kept past alertKeep: %v", marks)
	}
}

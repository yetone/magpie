package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

func claudeAuthFixture(t *testing.T, lapse string) (Provider, savedLogin) {
	t.Helper()
	testHome := claudeHome(t)
	noAnthropic(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	claudeSignIn(t, testHome, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(testHome, ".claude.json"), map[string]any{
		"oauthAccount": map[string]any{"emailAddress": "own@example.com"},
	})
	side := savedLogin{Agent: "claude", User: "side@example.com", On: true, Plan: "max", Seen: time.Now().Add(-time.Hour), Lapsed: lapse,
		Auth: mustJSONRaw(t, map[string]any{"claudeAiOauth": map[string]any{
			"accessToken": "old-access", "refreshToken": "old-refresh",
			"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max",
		}})}
	writeFile(t, loginsPath(), []savedLogin{side})
	forgetAccountCaches()
	p, ok := find(All(), "claude")
	if !ok {
		t.Fatal("own Claude account missing")
	}
	return p, side
}

// sideToken is the saved account's turn at a request: the directory Claude
// Code runs on it in, or why it can't.
func sideToken(t *testing.T, p Provider, user string) (string, error) {
	t.Helper()
	for _, q := range p.AlsoOn() {
		if q.Account.User == user {
			dir, _, err := q.Account.Token(context.Background())
			return dir, err
		}
	}
	t.Fatalf("%s is not among the accounts on", user)
	return "", nil
}

func claudeLapseOf(user string) string {
	for _, l := range Logins("claude") {
		if strings.EqualFold(l.User, user) {
			return l.Lapsed
		}
	}
	return ""
}

func TestClaudeLegacyLapseIsNotRunOrBlamedOnLogout(t *testing.T) {
	p, side := claudeAuthFixture(t, legacyClaudeLogoutLapse)
	if _, err := sideToken(t, p, side.User); err == nil || !strings.Contains(err.Error(), "sign in again") {
		t.Fatalf("lapsed login run: %v", err)
	}
	if _, err := os.Stat(claudeAccountDir(side.User)); !os.IsNotExist(err) {
		t.Fatalf("a directory was made for the lapsed login: %v", err)
	}
	if got := claudeLapseOf(side.User); got != claudeLogoutLapse {
		t.Fatalf("lapse shown as %q", got)
	}
}

// A refusal holds for the credential it was made on: Claude Code refreshing
// the account's sign-in in its directory afterwards, or a run started on
// the refused credential reporting late, leaves the account usable.
func TestClaudeRefusalHoldsForItsCredentialOnly(t *testing.T) {
	p, side := claudeAuthFixture(t, "")
	refused := ClaudeLoginVersion(side.User)
	NoteClaudeSignInFailure(side.User, refused, "Failed to authenticate: OAuth session expired and could not be refreshed")
	if _, err := sideToken(t, p, side.User); err == nil || !strings.Contains(err.Error(), "sign in again") {
		t.Fatalf("refused login run: %v", err)
	}
	if err := SwitchLogin("claude", side.User); err == nil {
		t.Fatal("Claude Code signed in to a refused login")
	}
	writeFile(t, filepath.Join(claudeAccountDir(side.User), ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "new-access", "refreshToken": "new-refresh",
		"expiresAt": time.Now().Add(8 * time.Hour).UnixMilli(), "subscriptionType": "max",
	}})
	if _, err := sideToken(t, p, side.User); err != nil {
		t.Fatalf("the sign-in Claude Code refreshed to is not used: %v", err)
	}
	NoteClaudeSignInFailure(side.User, refused, "Failed to authenticate: OAuth token revoked.")
	if _, err := sideToken(t, p, side.User); err != nil || claudeLapseOf(side.User) != "" {
		t.Fatalf("an old run's refusal holds for the new sign-in: %v", err)
	}
}

// Claude Code empties a sign-in Anthropic refused to refresh: the copy
// magpie kept of it is the refused one, never put back for Claude Code.
func TestClaudeEmptiedSignInIsNotPutBack(t *testing.T) {
	p, side := claudeAuthFixture(t, "")
	dir, err := sideToken(t, p, side.User)
	if err != nil {
		t.Fatal(err)
	}
	creds := filepath.Join(dir, ".credentials.json")
	writeFile(t, creds, map[string]any{"claudeAiOauth": map[string]any{"accessToken": "", "refreshToken": "", "expiresAt": 0}})
	if err := SwitchLogin("claude", side.User); err == nil {
		t.Fatal("Claude Code signed in to the refused copy")
	}
	if _, err := sideToken(t, p, side.User); err == nil {
		t.Fatal("ran on a sign-in Claude Code emptied")
	}
	if b, _ := os.ReadFile(creds); strings.Contains(string(b), "old-refresh") {
		t.Fatal("the refused copy was put back")
	}
	if claudeLapseOf(side.User) != claudeAuthLapse {
		t.Fatalf("lapse: %q", claudeLapseOf(side.User))
	}
}

func TestClaudeClearedKeychainDoesNotFallBackToStaleFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for the keychain")
	}
	p, side := claudeAuthFixture(t, "")
	dir, err := sideToken(t, p, side.User)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	cleared := filepath.Join(bin, "cleared.json")
	writeFile(t, cleared, map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "", "refreshToken": "", "expiresAt": 0, "subscriptionType": "max",
	}})
	testenv.Program(t, filepath.Join(bin, "security"), "#!/bin/sh\ncat '"+cleared+"'\n")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	claudeKeychain = true
	if _, err := sideToken(t, p, side.User); err == nil {
		t.Fatal("cleared keychain item passed over for the file beside it")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".credentials.json")); !strings.Contains(string(b), "old-refresh") {
		t.Fatal("the file was rewritten")
	}
}

// The account Claude Code itself is signed in to: read again with the same
// sign-in after a moment it couldn't be read, it is not taken for gone;
// refused, it stays so until Claude Code holds another credential.
func TestClaudeOwnLoginLapsesOnlyForWhatHappened(t *testing.T) {
	p, _ := claudeAuthFixture(t, "")
	cred := filepath.Join(os.Getenv("HOME"), ".claude", ".credentials.json")
	saved, err := os.ReadFile(cred)
	if err != nil {
		t.Fatal(err)
	}
	look := func() {
		forgetClaudeCredential()
		loginsMu.Lock()
		loginsSeenAt = time.Time{}
		loginsMu.Unlock()
		rememberLogins(true)
	}
	look()
	os.Remove(cred)
	look()
	if err := os.WriteFile(cred, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	look()
	if got := claudeLapseOf("own@example.com"); got != "" {
		t.Fatalf("read again unchanged, still lapsed: %q", got)
	}

	NoteClaudeSignInFailure("own@example.com", ClaudeLoginVersion("own@example.com"),
		"Failed to authenticate: OAuth session expired and could not be refreshed")
	look()
	if _, _, err := p.Account.Token(context.Background()); err == nil || claudeLapseOf("own@example.com") == "" {
		t.Fatalf("refused own login still run: %v", err)
	}
	writeFile(t, cred, map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "signed-in-again", "refreshToken": "signed-in-again-refresh",
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max",
	}})
	look()
	if _, _, err := p.Account.Token(context.Background()); err != nil || claudeLapseOf("own@example.com") != "" {
		t.Fatalf("signed in again, still refused: %v", err)
	}
}

func TestClaudeSignInRequiredDoesNotBlameTemporaryFailures(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    bool
	}{
		{"Failed to authenticate: OAuth session expired and could not be refreshed", true},
		{"Failed to authenticate: OAuth token revoked. Please log in again or contact your administrator.", true},
		{"OAuth access token has been revoked.", true},
		{"Failed to refresh OAuth token: another Claude Code process is refreshing it or exited mid-refresh", false},
		{"OAuth token refresh failed (HTTP 503)", false},
		{"Failed to authenticate: network connection timed out", false},
		{"You've hit your session limit", false},
	} {
		if got := ClaudeSignInRequired(tc.message); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.message, got, tc.want)
		}
	}
}

func TestClaudeProbeReportsTheAccountAndMarksItsFailedLogin(t *testing.T) {
	p, side := claudeAuthFixture(t, "")
	old := claudeCLIProbe
	t.Cleanup(func() { claudeCLIProbe = old })
	claudeCLIProbe = func(context.Context, string, string) error {
		return errors.New("Failed to authenticate: OAuth session expired and could not be refreshed")
	}
	var saved Provider
	for _, q := range p.AlsoOn() {
		saved = q
	}
	result := saved.testClaude(context.Background(), "claude-sonnet-5")
	if result.OK || result.Account != side.User || !strings.Contains(result.Error, "OAuth session expired") {
		t.Fatalf("failed test lost the account or original error: %+v", result)
	}
	if _, err := sideToken(t, p, side.User); err == nil {
		t.Fatal("the login that failed its test is still run")
	}
}

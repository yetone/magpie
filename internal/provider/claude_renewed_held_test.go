package provider

import (
	"testing"
	"time"
)

// claudeLook is magpie's next look at Claude Code's sign-in.
func claudeLook() {
	time.Sleep(10 * time.Millisecond)
	forgetAccountCaches()
	rememberLogins(true)
}

func claudeActive(t *testing.T) string {
	t.Helper()
	for _, l := range Logins("claude") {
		if l.Active {
			return l.User
		}
	}
	return ""
}

// claudeHolding is each saved Claude account's access token.
func claudeHolding() map[string]string {
	out := map[string]string{}
	for _, l := range readLogins() {
		if l.Agent == "claude" {
			c, _ := parseClaudeCredentials(l.Auth)
			out[claudeEmail(l.User)] = c.OAuth.AccessToken
		}
	}
	return out
}

// motui9535 on Discord: Max made first in Providers, Claude Code is
// switched to it; a Claude Code started on the Pro before writes the Pro's
// profile after; then a refresh of Usage runs /usage, which renews the
// sign-in, and Providers showed the Pro first again, its record holding
// the Max's renewed tokens. Renewed under the profile magpie read at its
// last look, the sign-in is the Max's still.
func TestClaudeMadeFirstStaysFirstWhenRenewedUnderAnothersProfile(t *testing.T) {
	prof, creds := claudeThree(t)
	if err := SwitchLogin("claude", "max@x.com"); err != nil {
		t.Fatal(err)
	}
	claudeLook()
	prof("pro@x.com", "pro@x.com's Organization")
	claudeLook()
	if got := claudeActive(t); got != "max@x.com" {
		t.Fatalf("under the Pro's stale profile, first is %q, want max@x.com", got)
	}
	creds("max@x.com-renewed", "max")
	claudeLook()
	if got := claudeActive(t); got != "max@x.com" {
		t.Errorf("after Claude Code renewed the Max's sign-in, first is %q, want max@x.com", got)
	}
	want := map[string]string{"team@x.com": "tok-team@x.com", "max@x.com": "tok-max@x.com-renewed", "pro@x.com": "tok-pro@x.com"}
	for e, tok := range claudeHolding() {
		if tok != want[e] {
			t.Errorf("%s holds %s, want %s", e, tok, want[e])
		}
	}
	if _, err := SwitchWhenSpent(t.Context(), "claude"); err != nil {
		t.Fatal(err)
	}
	if got := claudeActive(t); got != "max@x.com" {
		t.Errorf("after a look for a spent account, first is %q, want max@x.com", got)
	}
}

// A /login is not taken for a renewal: it writes its own account's
// profile, or its plan is another's.
func TestClaudeLoginAnewIsTheNewAccounts(t *testing.T) {
	t.Run("its profile", func(t *testing.T) {
		prof, creds := claudeThree(t)
		if err := SwitchLogin("claude", "max@x.com"); err != nil {
			t.Fatal(err)
		}
		claudeLook()
		prof("pro@x.com", "pro@x.com's Organization")
		creds("pro@x.com-anew", "pro")
		claudeLook()
		if got := claudeActive(t); got != "pro@x.com" {
			t.Errorf("signed in anew to the Pro, first is %q", got)
		}
		if h := claudeHolding(); h["pro@x.com"] != "tok-pro@x.com-anew" || h["max@x.com"] != "tok-max@x.com" {
			t.Errorf("records: %v", h)
		}
	})
	t.Run("its plan", func(t *testing.T) {
		prof, creds := claudeThree(t)
		if err := SwitchLogin("claude", "max@x.com"); err != nil {
			t.Fatal(err)
		}
		claudeLook()
		prof("pro@x.com", "pro@x.com's Organization")
		claudeLook()
		creds("pro@x.com-anew", "pro")
		claudeLook()
		if got := claudeActive(t); got != "pro@x.com" {
			t.Errorf("signed in anew to the Pro under its own profile, first is %q", got)
		}
		if h := claudeHolding(); h["pro@x.com"] != "tok-pro@x.com-anew" || h["max@x.com"] != "tok-max@x.com" {
			t.Errorf("records: %v", h)
		}
	})
}

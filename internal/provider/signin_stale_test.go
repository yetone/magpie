package provider

import (
	"path/filepath"
	"testing"
	"time"
)

// A logout can leave Claude Code's credentials and profile behind. Signing in
// through magpie then is the account Claude Code uses, not one kept beside a
// stale one, and it shows up as a provider (#31).
func TestClaudeSignInOverStaleCredentials(t *testing.T) {
	shellFakes(t)
	home := claudeHome(t)
	noAnthropic(t)
	cred := claudeSignIn(t, home, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{
		"emailAddress": "old@example.com", "organizationUuid": "o-old"}})
	// Claude Code is signed out until the new account's credentials are there
	fakeClaudeLogin(t, fakeClaudeAccount{email: "new@example.com", org: "new@example.com's Organization", orgUUID: "o-new", plan: "max", refresh: "sk-ant-ort01-new"},
		"if grep -q sk-ant-ort01-new "+cred+"; then\n"+
			`echo '{"loggedIn": true, "email": "new@example.com", "subscriptionType": "max"}'`+"\nelse\n"+
			`echo '{"loggedIn": false, "authMethod": "none"}'`+"\nfi\nexit 0", true)
	forgetClaudeStatus()
	if _, ok := find(All(), "claude"); ok {
		t.Fatal("signed-out account listed")
	}

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	finishInBrowser(t, st, "the-code")
	st = waitDone(t, st.ID)
	if st.State != "done" || st.User != "new@example.com" || !st.Using {
		t.Fatalf("state %+v", st)
	}
	var c map[string]map[string]any
	readJSON(cred, &c)
	if c["claudeAiOauth"]["refreshToken"] != "sk-ant-ort01-new" {
		t.Fatal("the sign-in is not Claude Code's")
	}
	if p, ok := find(All(), "claude"); !ok || p.Account.User != "new@example.com" {
		t.Fatalf("account after sign-in: %v %+v", ok, p.Account)
	}
}

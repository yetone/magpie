package provider

// A spare handed to Claude Code comes back from the switch with the
// sign-in it was last refreshed to (#6391, Crosery): the account's config
// directory, made again behind the hand-over (#4750), keeps the sign-in
// from before it, and Claude Code refreshes its own copy meanwhile. The
// directory's older sign-in must not be taken over the saved one, or the
// spent copy replaces the fresh one in logins.json and the next refresh in
// the directory empties it, the account signed out.

import (
	"strings"
	"testing"
	"time"
)

func TestClaudeDirOlderSignInKeepsTheSavedOne(t *testing.T) {
	claudeThree(t)
	if err := SwitchLogin("claude", "team@x.com · Acme"); err != nil {
		t.Fatal(err)
	}
	var maxUser string
	for _, l := range readLogins() {
		if strings.HasPrefix(l.User, "max@") {
			maxUser = l.User
		}
	}
	if maxUser == "" {
		t.Fatal("no saved max account")
	}
	// max's directory, as a run queued behind the hand-over makes it
	// (#4750): it holds the sign-in the account was saved with
	if _, err := claudeSavedDir(maxUser); err != nil {
		t.Fatal(err)
	}
	dir := claudeAccountDir(maxUser)
	if c, ok := readClaudeDir(dir); !ok || c.OAuth.AccessToken != "tok-max@x.com" {
		t.Fatalf("the directory does not hold the saved sign-in: ok=%v, %s", ok, c.OAuth.AccessToken)
	}
	// the hand-over makes the account Claude Code's own, Claude Code
	// refreshes its copy over the hour it serves, and the switch back
	// saves that as the account's: logins.json holds the fresh sign-in,
	// the directory the one from before the hand-over
	loginsMu.Lock()
	ls := readLogins()
	for i := range ls {
		if strings.HasPrefix(ls[i].User, "max@") {
			c, ok := parseClaudeCredentials(ls[i].Auth)
			if !ok {
				t.Fatal("max's saved sign-in doesn't parse")
			}
			c.OAuth.AccessToken = "tok-max-fresh"
			c.OAuth.RefreshToken = "r-max-fresh"
			c.OAuth.ExpiresAt = time.Now().Add(2 * time.Hour).UnixMilli()
			b, err := c.marshal()
			if err != nil {
				t.Fatal(err)
			}
			ls[i].Auth = b
		}
	}
	if err := writeLogins(ls); err != nil {
		t.Fatal(err)
	}
	loginsMu.Unlock()
	forgetAccountCaches()

	// the next run on the account: its directory is made again from the
	// saved sign-in, not the directory's older one taken over it
	if _, err := claudeSavedDir(maxUser); err != nil {
		t.Fatal(err)
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if strings.HasPrefix(l.User, "max@") {
			c, _ := parseClaudeCredentials(l.Auth)
			if c.OAuth.AccessToken != "tok-max-fresh" {
				t.Errorf("logins.json took the directory's %s over the saved %s", c.OAuth.AccessToken, "tok-max-fresh")
			}
		}
	}
	if c, ok := readClaudeDir(dir); !ok || c.OAuth.AccessToken != "tok-max-fresh" {
		t.Errorf("the directory runs on %s, not the saved sign-in", c.OAuth.AccessToken)
	}
}

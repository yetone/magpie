package provider

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// claudeOnSeat signs Claude Code in to me@x.com's subscription of org,
// as /login leaves it: the sign-in and the profile naming its org.
func claudeOnSeat(t *testing.T, home, cred, org, orgID, plan string) {
	t.Helper()
	writeFile(t, filepath.Join(home, ".claude.json"), map[string]any{"oauthAccount": map[string]any{"emailAddress": "me@x.com",
		"organizationName": org, "organizationUuid": orgID}})
	writeFile(t, cred, map[string]any{"claudeAiOauth": map[string]any{"accessToken": "tok-" + orgID, "refreshToken": "r-" + orgID,
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": plan}})
	forgetAccountCaches()
}

// A Team seat and a personal Max of one email are two accounts: Claude
// Code moved from one to the other is moved off it, either way (netfishx
// on X: several Claude accounts' usage and plans mixed up).
func TestClaudeSeatAndPersonalOfOneEmailAreTwo(t *testing.T) {
	home := claudeHome(t)
	cred := claudeSignIn(t, home, time.Now().Add(time.Hour))
	claudeOnSeat(t, home, cred, "Acme", "org-acme", "team")
	if u, _ := claudeCodeOn(); u != "me@x.com · Acme" {
		t.Fatalf("on %q", u)
	}
	if ClaudeCodeMovedOff("me@x.com · Acme") || ClaudeCodeMovedOff("ME@x.com · Acme ") {
		t.Error("Claude Code on the Acme seat taken as moved off it")
	}
	if !ClaudeCodeMovedOff("me@x.com") {
		t.Error("Claude Code on the Acme seat taken as on the personal me@x.com")
	}
	claudeOnSeat(t, home, cred, "me@x.com's Organization", "org-me", "max")
	if u, _ := claudeCodeOn(); u != "me@x.com" {
		t.Fatalf("on %q", u)
	}
	if !ClaudeCodeMovedOff("me@x.com · Acme") {
		t.Error("Claude Code on the personal me@x.com taken as still on the Acme seat")
	}
	if ClaudeCodeMovedOff("me@x.com") {
		t.Error("Claude Code on the personal me@x.com taken as moved off it")
	}
}

// The seat's reading isn't overwritten by /usage run once Claude Code is
// on the personal account of the same email.
func TestClaudeSeatKeepsItsOwnUsage(t *testing.T) {
	home := claudeHome(t)
	cred := claudeSignIn(t, home, time.Now().Add(time.Hour))
	claudeOnSeat(t, home, cred, "Acme", "org-acme", "team")
	var out atomic.Value
	out.Store("Current session: 5% used · resets " + sessionReset() + "\n")
	fakeClaudeUsage(t, &out, nil)
	AskClaudeUsage()
	if ws, err := claudeWindows(context.Background(), "me@x.com · Acme", true); err != nil || len(ws) == 0 || ws[0].Used != 5 {
		t.Fatalf("seat: %v %+v", err, ws)
	}
	claudeOnSeat(t, home, cred, "me@x.com's Organization", "org-me", "max")
	out.Store("Current session: 100% used · resets " + sessionReset() + "\n")
	claudeUsage.Lock()
	for k, e := range claudeUsage.m {
		e.tried = e.tried.Add(-claudeAskFloor)
		claudeUsage.m[k] = e
	}
	claudeUsage.Unlock()
	AskClaudeUsage()
	// asked as the active account by a caller that looked before the switch
	if ws, _ := claudeWindows(context.Background(), "me@x.com · Acme", true); len(ws) == 0 || ws[0].Used != 5 {
		t.Fatalf("the seat shows the personal account's reading: %+v", ws)
	}
	if ws, err := claudeWindows(context.Background(), "me@x.com", true); err != nil || len(ws) == 0 || ws[0].Used != 100 {
		t.Fatalf("personal: %v %+v", err, ws)
	}
}

package provider

// Sign-in alerts (steven_4422 on Discord): a notification when an account
// in use has to be signed in again, and, for Claude, before its sign-in
// ends. Until now a sign-in its vendor refused or revoked showed only on
// the account's row (Sign-in required, Sign in again) and the row's
// Signed out pill, seen only by opening magpie. The test of an account that
// has to be signed in again is the one the Providers page uses
// (renderAccounts in app.js), so a notification names exactly the rows
// that say so. Each is said once, until the account is usable again; what
// was said is kept in a file, so a restart doesn't say it again.
//
// Claude Code keeps when a sign-in's refresh token ends
// (claudeAiOauth.refreshTokenExpiresAt, from the token reply's
// refresh_token_expires_in, else 30 days from the sign-in) and says "Your
// login expires in N days · run /login to renew" from three days before;
// past it, it takes the sign-in for none. A refresh doesn't move it unless
// Anthropic sends a new one. magpie tells the same, from the same field.

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SignInAlert is a sign-in to tell the user of. Kind is "lapsed" for an
// account that has to be signed in again, "ending" for one whose sign-in
// ends at EndsAt.
type SignInAlert struct {
	Provider string // the provider's id: the row a click opens
	Name     string
	User     string
	Kind     string
	EndsAt   *time.Time
}

// needsSignIn says an account of agent marked lapsed has to be signed in
// again, as the Providers page decides it (renderAccounts' unusable and
// signedOut): any mark on a Claude, Copilot or plugin account, and on
// another built-in one a mark that says so (signInGone, app.js's
// SIGN_IN_GONE). A plugin's usage read that says so marks the account
// too (pluginLoginQuota), so its mark covers the page's other case.
func needsSignIn(agent string, isPlugin bool, lapsed string) bool {
	if lapsed == "" {
		return false
	}
	return agent == "claude" || agent == "copilot" || isPlugin || signInGone.MatchString(lapsed)
}

// claudeEndsWithin is how long before a Claude sign-in ends it is told of:
// Claude Code's own warning's three days.
const claudeEndsWithin = 3 * 24 * time.Hour

// claudeSignInEnds is when the Claude sign-in auth ends, as Claude Code
// reads it: its refresh token's end, unless its access token outlasts that
// by more than the warning (a sign-in whose end is not the refresh
// token's). ok is false when it says none.
func claudeSignInEnds(auth []byte) (at time.Time, ok bool) {
	c, parsed := parseClaudeCredentials(auth)
	if !parsed || c.OAuth.RefreshToken == "" || c.OAuth.RefreshExpiresAt <= 0 {
		return time.Time{}, false
	}
	if c.OAuth.ExpiresAt > c.OAuth.RefreshExpiresAt+claudeEndsWithin.Milliseconds() {
		return time.Time{}, false
	}
	return time.UnixMilli(c.OAuth.RefreshExpiresAt), true
}

// signInState is one account as the alerts see it.
type signInState struct {
	Provider, Name, User string
	Gone                 bool
	EndsAt               *time.Time
}

// signInStates is every account in use of every subscription switched on:
// whether it has to be signed in again, and when a Claude one's sign-in
// ends.
func signInStates() []signInState {
	ends := map[string]time.Time{}
	var out []signInState
	for _, p := range Accounts() {
		a := p.Account
		if a == nil || p.Off {
			continue
		}
		agent, isPlugin := a.Agent, false
		if _, ok := PluginOf(p.ID); ok && p.IsPlugin() {
			agent, isPlugin = p.ID, true // its accounts go by the provider's id
		}
		ls := Logins(agent)
		if agent == "claude" && len(ls) > 0 {
			for _, l := range sharedLogins() {
				if l.Agent == "claude" {
					if at, ok := claudeSignInEnds(l.Auth); ok {
						ends[strings.ToLower(l.User)] = at
					}
				}
			}
		}
		for _, l := range ls {
			if !l.On && !l.Active {
				continue // not in use: nothing is lost while it is signed out
			}
			s := signInState{Provider: p.ID, Name: p.Name, User: l.User, Gone: needsSignIn(a.Agent, isPlugin, l.Lapsed)}
			if at, ok := ends[strings.ToLower(l.User)]; ok && agent == "claude" && !s.Gone {
				s.EndsAt = &at
			}
			out = append(out, s)
		}
	}
	return out
}

// dueSignIns is what the accounts ss call for, the marks in marks brought
// up to date: an account that has to be signed in again, once until it is
// usable again; a sign-in ending within claudeEndsWithin, once for that
// end. An account not listed now (its plugin not started yet) keeps its
// marks a while, so it isn't told of again when it is.
func dueSignIns(ss []signInState, marks map[string]alertMark, now time.Time) []SignInAlert {
	var out []SignInAlert
	seen := map[string]bool{}
	for _, s := range ss {
		pre := s.Provider + "|" + strings.ToLower(s.User) + "|"
		gone, ends := pre+"gone", pre+"ends"
		seen[gone], seen[ends] = true, true
		if !s.Gone {
			delete(marks, gone)
		} else if _, ok := marks[gone]; !ok {
			marks[gone] = alertMark{At: now}
			out = append(out, SignInAlert{Provider: s.Provider, Name: s.Name, User: s.User, Kind: "lapsed"})
		}
		if s.EndsAt == nil || !s.EndsAt.After(now) || s.EndsAt.Sub(now) > claudeEndsWithin {
			if s.EndsAt == nil || s.EndsAt.Sub(now) > claudeEndsWithin {
				delete(marks, ends) // signed in anew, or not ending yet
			}
			continue
		}
		if m, ok := marks[ends]; ok && sameRun(m.Until, s.EndsAt) {
			continue
		}
		at := *s.EndsAt
		marks[ends] = alertMark{At: now, Until: &at}
		out = append(out, SignInAlert{Provider: s.Provider, Name: s.Name, User: s.User, Kind: "ending", EndsAt: &at})
	}
	for k, m := range marks {
		if !seen[k] && now.Sub(m.At) > alertKeep {
			delete(marks, k)
		}
	}
	return out
}

func signInAlertPath() string { return filepath.Join(filepath.Dir(Path()), "signin-alerts.json") }

// signInAlertMu keeps the file to one writer in this magpie.
var signInAlertMu sync.Mutex

// checkSignIns says what the accounts ss call for, keeping the marks in
// path.
func checkSignIns(path string, ss []signInState, now time.Time) []SignInAlert {
	signInAlertMu.Lock()
	defer signInAlertMu.Unlock()
	marks := map[string]alertMark{}
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &marks) != nil {
			// unknown, not empty: a mark lost would say every lapse again
			return nil
		}
	} else if !os.IsNotExist(err) {
		return nil
	}
	before, _ := json.Marshal(marks)
	out := dueSignIns(ss, marks, now)
	if after, _ := json.Marshal(marks); string(after) != string(before) {
		os.MkdirAll(filepath.Dir(path), 0o755)
		writePrivate(path, append(after, '\n'))
	}
	return out
}

// signInAlertEvery is how often the accounts are looked at: a mark is set
// as a request is refused, so this reads magpie's own files, asking no
// vendor.
const signInAlertEvery = 2 * time.Minute

// WatchSignIns hands send each sign-in alert due, a minute after it starts
// and every signInAlertEvery after, until ctx ends.
func WatchSignIns(ctx context.Context, send func(SignInAlert)) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, a := range checkSignIns(signInAlertPath(), signInStates(), time.Now()) {
			log.Printf("sign-in alert: %s %s %s", a.Kind, a.Name, a.User)
			send(a)
		}
		t.Reset(signInAlertEvery)
	}
}

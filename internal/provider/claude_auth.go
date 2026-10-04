package provider

// A Claude sign-in Anthropic refused. Claude Code refreshes the sign-ins it
// runs on itself; when Anthropic refuses a refresh (invalid_grant), Claude
// Code empties the tokens where it keeps them (Claude Code 2.1.x), and each
// run on that sign-in fails with "OAuth session expired and could not be
// refreshed" until the account is signed in again. Waiting doesn't mend it,
// so magpie doesn't rest the account and try it again: it keeps the refusal
// on the saved login, for the credential it was made on (Refused), and
// passes over the login while it still has that credential. A credential
// changed since — Claude Code refreshed it after all, or the account was
// signed in again — is not the one refused.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// claudeAuthLapse is why a saved Claude account whose credential Anthropic
// refused can't be used.
const claudeAuthLapse = "Claude Code could not authenticate this account; sign in again in magpie"

// ClaudeSignInRequired says message is Claude Code's, or magpie's, word that
// a Claude sign-in is gone and has to be made again: not a failure of the
// moment, as a network error or another Claude Code holding the refresh is
// ("Failed to refresh OAuth token: another Claude Code process is
// refreshing it").
func ClaudeSignInRequired(message string) bool {
	for _, text := range []string{
		"OAuth session expired and could not be refreshed", // Claude Code: its refresh refused
		"OAuth token revoked",                              // Claude Code: Anthropic revoked it
		"OAuth access token has been revoked",              // Anthropic's own 401
		claudeAuthLapse, claudeLogoutLapse, claudeGoneLapse,
	} {
		if strings.Contains(message, text) {
			return true
		}
	}
	return false
}

// version tells a credential from the one it is refreshed to: a refresh
// replaces both tokens.
func (c claudeCredentials) version() string {
	sum := sha256.Sum256([]byte(c.OAuth.AccessToken + "\x00" + c.OAuth.RefreshToken))
	return hex.EncodeToString(sum[:])
}

// cleared says c is a sign-in Claude Code emptied, as it does one whose
// refresh Anthropic refused: it reads an empty refresh token as no sign-in.
func (c claudeCredentials) cleared() bool {
	o, _ := c.raw["claudeAiOauth"].(map[string]any)
	rt, ok := o["refreshToken"].(string)
	return ok && rt == "" && c.OAuth.AccessToken == ""
}

func claudeLoginVersion(l savedLogin) string {
	if c, ok := parseClaudeCredentials(l.Auth); ok {
		return c.version()
	}
	return ""
}

// ClaudeLoginVersion is the credential the saved Claude account user has
// now, which a run started on it goes by (NoteClaudeSignInFailure).
func ClaudeLoginVersion(user string) string {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if l.Agent == "claude" && strings.EqualFold(l.User, user) {
			return claudeLoginVersion(l)
		}
	}
	return ""
}

// refuseClaudeLogin keeps on l that Anthropic refused its credential
// version, and says whether that changed l.
func refuseClaudeLogin(l *savedLogin, version string) bool {
	if version == "" || l.Refused == version && l.Lapsed == claudeAuthLapse {
		return false
	}
	l.Lapsed, l.Refused = claudeAuthLapse, version
	return true
}

// NoteClaudeSignInFailure keeps a run's refusal on the saved Claude account
// user, when the run started on the credential the account still has
// (version): one started on an older credential says nothing of this one.
// The run's own error stays in the request's trace.
func NoteClaudeSignInFailure(user, version, message string) {
	if !ClaudeSignInRequired(message) {
		return
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	for i := range ls {
		if ls[i].Agent == "claude" && strings.EqualFold(ls[i].User, user) && claudeLoginVersion(ls[i]) == version {
			if refuseClaudeLogin(&ls[i], version) {
				_ = writeLogins(ls)
			}
			return
		}
	}
}

// claudeOwnRefused is why the account Claude Code itself is signed in to,
// user, can't be used: Anthropic refused the credential Claude Code holds
// for it now; "" when it didn't.
func claudeOwnRefused(user string) string {
	c, _, ok := claudeCredential()
	if !ok {
		return ""
	}
	v := c.version()
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if l.Agent == "claude" && strings.EqualFold(l.User, user) && l.Refused == v && l.Lapsed != "" {
			return l.Lapsed
		}
	}
	return ""
}

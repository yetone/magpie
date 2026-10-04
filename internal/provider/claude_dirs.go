package provider

// A saved Claude account in use beside the one Claude Code is signed in to
// runs Claude Code in a config directory of its own (CLAUDE_CONFIG_DIR): its
// sign-in is put there once, from logins.json, and from then on Claude Code
// keeps it, refreshing it as it does its own. magpie never refreshes a
// Claude sign-in nor asks Anthropic anything with one; it only reads back
// what Claude Code left, so logins.json follows.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/proc"
	"golang.org/x/text/unicode/norm"
)

// claudeDirsRoot holds the saved Claude accounts' config directories, and
// the sign-ins being made.
func claudeDirsRoot() string { return filepath.Join(filepath.Dir(Path()), "claude-accounts") }

// claudeAccountDir is the config directory a saved Claude account runs
// Claude Code in.
func claudeAccountDir(user string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(user))))
	return filepath.Join(claudeDirsRoot(), hex.EncodeToString(sum[:])[:16])
}

// claudeDirService is the keychain item Claude Code keeps its sign-in in
// when CLAUDE_CONFIG_DIR is dir: its own name, told apart by the directory.
func claudeDirService(dir string) string {
	sum := sha256.Sum256([]byte(norm.NFC.String(dir)))
	return "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
}

// readClaudeDir is the sign-in Claude Code keeps in config directory dir,
// where it looks first: the keychain on a Mac, then the file.
func readClaudeDir(dir string) (claudeCredentials, bool) {
	if claudeKeychain {
		out, err := proc.Command("security", "find-generic-password", "-s", claudeDirService(dir), "-a", claudeKeychainAccount(), "-w").Output()
		if err == nil {
			b, _ := keychainText(bytes.TrimSpace(out))
			if c, ok := parseClaudeCredentials(b); c.raw != nil {
				// the item there is the sign-in, emptied or not: Claude
				// Code reads no file beside it
				return c, ok
			}
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		return claudeCredentials{}, false
	}
	return parseClaudeCredentials(b)
}

// clearClaudeDir takes the sign-in out of config directory dir, so the one
// it was moved to is its only holder.
func clearClaudeDir(dir string) {
	if claudeKeychain {
		_ = proc.Command("security", "delete-generic-password", "-s", claudeDirService(dir), "-a", claudeKeychainAccount()).Run()
	}
	_ = os.Remove(filepath.Join(dir, ".credentials.json"))
}

// forgetClaudeDir removes a saved account's config directory altogether.
func forgetClaudeDir(user string) {
	dir := claudeAccountDir(user)
	clearClaudeDir(dir)
	_ = os.RemoveAll(dir)
}

// claudeSavedDir is the config directory to run Claude Code in for the
// saved account user: its sign-in is put there from logins.json when there
// is none yet, and what Claude Code has made of it since is read back. An
// account that can't be used (claudeSignedOut) gets none.
func claudeSavedDir(user string) (string, error) {
	savedTokenMu.Lock()
	defer savedTokenMu.Unlock()
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	i := -1
	for j := range ls {
		if ls[j].Agent == "claude" && strings.EqualFold(ls[j].User, user) {
			i = j
		}
	}
	if i < 0 {
		return "", fmt.Errorf("no saved claude account %q", user)
	}
	dir := claudeAccountDir(user)
	has, changed, err := syncClaudeDir(&ls[i])
	if err != nil {
		return "", err
	}
	if changed {
		if err := writeLogins(ls); err != nil {
			return "", err
		}
	}
	if why := claudeSignedOut(ls[i]); why != "" {
		return "", errors.New(why)
	}
	if has {
		return dir, nil
	}
	c, _ := parseClaudeCredentials(ls[i].Auth)
	b, err := c.marshal()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := writePrivate(filepath.Join(dir, ".credentials.json"), append(b, '\n')); err != nil {
		return "", err
	}
	// who it is, as Claude Code keeps it beside a sign-in it made
	profile := filepath.Join(dir, ".claude.json")
	if _, err := os.Stat(profile); os.IsNotExist(err) && len(ls[i].Profile) > 0 {
		var acct map[string]any
		if json.Unmarshal(ls[i].Profile, &acct) == nil {
			pb, _ := json.MarshalIndent(map[string]any{"oauthAccount": acct, "hasCompletedOnboarding": true}, "", "  ")
			_ = writePrivate(profile, append(pb, '\n'))
		}
	}
	return dir, nil
}

// syncClaudeDir brings the saved Claude login l up to what Claude Code
// keeps in the account's config directory: the sign-in it refreshed to
// there, or, when it emptied that sign-in, Anthropic's refusal of the one
// l has. has says the directory holds a sign-in to run on; changed, that l
// changed.
func syncClaudeDir(l *savedLogin) (has, changed bool, err error) {
	c, ok := readClaudeDir(claudeAccountDir(l.User))
	switch {
	case ok:
		changed, err = takeClaudeDir(l, c)
		return true, changed, err
	case c.cleared():
		return false, refuseClaudeLogin(l, claudeLoginVersion(*l)), nil
	}
	return false, false, nil
}

// takeClaudeDir puts the sign-in Claude Code keeps in an account's config
// directory into its saved login, and says whether that changed it.
func takeClaudeDir(l *savedLogin, c claudeCredentials) (bool, error) {
	if old, ok := parseClaudeCredentials(l.Auth); ok && old.OAuth.AccessToken == c.OAuth.AccessToken &&
		old.OAuth.RefreshToken == c.OAuth.RefreshToken {
		return false, nil
	}
	b, err := c.marshal()
	if err != nil {
		return false, err
	}
	l.Auth = b
	if c.OAuth.SubscriptionType != "" {
		l.Plan = c.OAuth.SubscriptionType
	}
	l.Renewed = time.Now().UTC().Truncate(time.Second)
	l.Lapsed, l.Refused = "", ""
	return true, nil
}

// claudeStandIn is the saved account served in the place of Claude Code's
// own while Claude Code is signed out — the user logged out of it, as it
// tells them to when magpie's token is set beside a claude.ai sign-in.
// magpie keeps the other accounts' sign-ins itself, each its own, so a
// logout signs none of them out of magpie: the stand-in runs Claude Code
// in a config directory of its own, as the others on do. Never the one
// Claude Code was signed in to, whose sign-in went with it
// (claudeLoggedOut, or still Held before rememberLogins next looks: it is
// asked only while Claude Code is signed out); of the others, one that can
// be used, on before off, then the one seen last; one that can't — its
// sign-in gone, or refused — only when no other can; "" when there is none.
func claudeStandIn(ls []savedLogin) string {
	user, best := "", -1
	var seen time.Time
	for _, l := range ls {
		if l.Agent != "claude" || l.Held || l.Lapsed != "" && l.Refused == "" {
			continue
		}
		rank := 0
		if claudeSignedOut(l) == "" {
			rank = 2
			if l.On {
				rank++
			}
		}
		if rank > best || rank == best && l.Seen.After(seen) {
			user, seen, best = l.User, l.Seen, rank
		}
	}
	return user
}

// claudeLogoutLapse is why the account Claude Code was signed in to can't
// be used once Claude Code let go of it: logged out, which revokes the
// sign-in it held, or emptied that sign-in once Anthropic refused it.
const claudeLogoutLapse = "Claude Code is no longer signed in; sign in again in magpie"

// legacyClaudeLogoutLapse is claudeLogoutLapse as magpie wrote it before,
// which took every such lapse for a /logout.
const legacyClaudeLogoutLapse = "Claude Code's /logout signed it out (it revokes the sign-in it holds); sign in again"

// claudeGoneLapse is why a saved Claude account with no sign-in kept can't
// be used.
const claudeGoneLapse = "its sign-in is gone; sign in again"

// claudeLoggedOut marks the account Claude Code held, now that it holds no
// sign-in, as lapsed: magpie's copy of that account is the same sign-in,
// gone too — revoked by a /logout (POST <token URL>/revoke, Claude Code
// 2.1.x's performLogout), or refused by Anthropic, as the sign-in Claude
// Code emptied was. Which it was can't be told from here. Signed in to it
// again, Claude Code holds it once more (rememberLogins). The accounts
// magpie keeps in config directories of their own are sign-ins of their own
// and stay. It says whether ls changed.
func claudeLoggedOut(ls []savedLogin) bool {
	changed := false
	for i := range ls {
		if ls[i].Agent != "claude" || !ls[i].Held {
			continue
		}
		ls[i].Held, changed = false, true
		if claudeSignedOut(ls[i]) == "" {
			ls[i].Lapsed, ls[i].Refused = claudeLogoutLapse, ""
		}
	}
	return changed
}

// claudeSignedOut is why a saved Claude account can't be used, "" when it
// can: its saved sign-in is gone, Claude Code let go of it
// (claudeLoggedOut), or Anthropic refused the credential it has now
// (claude_auth.go) — a refusal of an earlier one says nothing of it.
func claudeSignedOut(l savedLogin) string {
	if l.Lapsed != "" && (l.Refused == "" || l.Refused == claudeLoginVersion(l)) {
		if l.Lapsed == legacyClaudeLogoutLapse {
			return claudeLogoutLapse
		}
		return l.Lapsed
	}
	if _, ok := parseClaudeCredentials(l.Auth); ok {
		return ""
	}
	return claudeGoneLapse
}

// claudeStandInAccount is the Claude Code provider while Claude Code is
// signed out, on claudeStandIn's account; false with no account saved.
func claudeStandInAccount() (Provider, bool) {
	loginsMu.Lock()
	ls := readLogins()
	loginsMu.Unlock()
	user := claudeStandIn(ls)
	if user == "" {
		return Provider{}, false
	}
	acct := &Account{Agent: "claude", User: user, standIn: true}
	for _, l := range ls {
		if l.Agent == "claude" && strings.EqualFold(l.User, user) {
			acct.Plan = l.Plan
		}
	}
	acct.token = func(context.Context) (string, error) { return claudeSavedDir(user) }
	return claudeProvider(acct), true
}

// AgentsOwn says the account is the one the agent itself is signed in to,
// run in the agent's own home with what it keeps there, not with a sign-in
// magpie hands it. Which account that is moves with a switch (SwitchLogin):
// a Claude Code started on it before goes on as whatever Claude Code is
// signed in to by then, as it reads its keychain again every half minute.
func (a *Account) AgentsOwn() bool { return a != nil && a.token == nil }

// ClaudeCodeMovedOff says Claude Code itself is signed in to another
// account than user now: what a Claude Code run in its own home says of
// the account it is on is no longer user's. false when it can't be told.
func ClaudeCodeMovedOff(user string) bool {
	on := claudeCodeOn()
	return on != "" && !sameClaudeUser(on, user)
}

// claudeCodeOn is the account Claude Code itself is signed in to now, as
// magpie names it; "" when signed out or unknown.
func claudeCodeOn() string {
	l, ok := liveLogin("claude")
	if !ok {
		return ""
	}
	return l.User
}

// sameClaudeUser: a and b name one account, the one as magpie names a Team
// or Enterprise seat ("me@x.com · Org") and the other as its email alone,
// as `claude auth status` gives it.
func sameClaudeUser(a, b string) bool {
	a, b = strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if a == b {
		return true
	}
	ea, _, _ := strings.Cut(a, " · ")
	eb, _, _ := strings.Cut(b, " · ")
	return (ea == b || eb == a) && ea != ""
}

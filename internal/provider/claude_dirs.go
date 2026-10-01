package provider

// A saved Claude account in use beside the one Claude Code is signed in to
// runs Claude Code in a config directory of its own (CLAUDE_CONFIG_DIR): its
// sign-in is put there once, from logins.json, and from then on Claude Code
// keeps it, refreshing it as it does its own. magpie never refreshes a
// Claude sign-in nor asks Anthropic anything with one; it only reads back
// what Claude Code left, so logins.json follows.

import (
	"bytes"
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
			if c, ok := parseClaudeCredentials(b); ok {
				return c, true
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
// is none yet, and what Claude Code has made of it since is read back.
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
	if c, ok := readClaudeDir(dir); ok {
		if changed, err := takeClaudeDir(&ls[i], c); err != nil || !changed {
			return dir, err
		}
		return dir, writeLogins(ls)
	}
	c, ok := parseClaudeCredentials(ls[i].Auth)
	if !ok {
		return "", errors.New("the saved Claude sign-in of " + user + " is unreadable")
	}
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
	l.Lapsed = ""
	return true, nil
}

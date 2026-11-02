package provider

// A Claude account added by a setup-token: the long-lived token `claude
// setup-token` prints, which Claude Code takes in CLAUDE_CODE_OAUTH_TOKEN.
// magpie keeps it in logins.json as it keeps a sign-in, and hands it to the
// real Claude Code, in that variable, for runs in the account's own config
// directory only (ClaudeDirToken); it never asks Anthropic anything with it.
//
// What such a token can't do: it has the inference scope only, so Claude
// Code tells no email, plan or organization for it (`auth status` says
// "oauth_token" and no more), and its /usage shows the API cost view, not
// the subscription's windows. The account is named by the token's own
// short hash, and its allowance is unknown (errClaudeSetupToken) until a
// reply's rate-limit headers tell it. Claude Code itself can't be signed in
// to it: there is nothing to put in its keychain or .credentials.json.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/yetone/magpie/internal/settings"
)

// claudeTokenAuth is a setup-token as savedLogin.Auth keeps it.
type claudeTokenAuth struct {
	SetupToken string `json:"setupToken"`
}

// claudeSetupToken is the setup-token a saved Claude login's Auth holds, ""
// for a sign-in.
func claudeSetupToken(auth []byte) string {
	if len(auth) == 0 {
		return ""
	}
	var a claudeTokenAuth
	if json.Unmarshal(auth, &a) != nil {
		return ""
	}
	return a.SetupToken
}

// claudeTokenVersion is the credential version of a setup-token
// (claudeLoginVersion): its hash, never the token.
func claudeTokenVersion(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "setup-token:" + hex.EncodeToString(sum[:])[:16]
}

// claudeTokenName is the name a setup-token account gets: Claude tells no
// email for it, so a short hash of the token tells two apart without
// showing any of it.
func claudeTokenName(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "Claude setup-token " + hex.EncodeToString(sum[:])[:6]
}

// errClaudeSetupToken is a setup-token account's allowance: unknown, never
// zero. Its words must not read as a refusal (claudeUsageDenied).
var errClaudeSetupToken = errors.New("unknown: a setup-token can't read the allowance (Claude Code's /usage needs a full sign-in); it shows once a reply tells it")

// errClaudeTokenSwitch is the answer to signing Claude Code in to a
// setup-token account.
const errClaudeTokenSwitch = "%s was added by a setup-token: magpie's requests use it, but Claude Code itself can't be signed in to it"

// checkClaudeSetupToken is token trimmed, or why it isn't a setup-token.
func checkClaudeSetupToken(token string) (string, error) {
	token = strings.TrimSpace(token)
	token = strings.TrimPrefix(token, "CLAUDE_CODE_OAUTH_TOKEN=")
	token = strings.Trim(token, `"'`)
	switch {
	case token == "":
		return "", errors.New("paste the token `claude setup-token` printed")
	case strings.HasPrefix(token, "sk-ant-api"):
		return "", errors.New("this is an Anthropic API key, not a setup-token: add it as an API key provider instead")
	case !strings.HasPrefix(token, "sk-ant-oat"):
		return "", errors.New("this isn't a setup-token: `claude setup-token` prints one that starts with sk-ant-oat")
	case len(token) > 512 || strings.IndexFunc(token, func(r rune) bool { return unicode.IsSpace(r) || r > unicode.MaxASCII }) >= 0:
		return "", errors.New("this isn't a setup-token: it has spaces or other characters a token doesn't")
	}
	return token, nil
}

// AddClaudeSetupToken adds a Claude account from a setup-token, served by
// Claude Code with the token in CLAUDE_CODE_OAUTH_TOKEN. Claude Code's own
// sign-in is left as it is. It answers the account's name; a token already
// saved answers the account that has it.
func AddClaudeSetupToken(token string) (string, error) {
	if settings.AccountDiscoveryOff() {
		return "", errAccountDiscoveryOff
	}
	token, err := checkClaudeSetupToken(token)
	if err != nil {
		return "", err
	}
	loginsMu.Lock()
	for _, l := range readLogins() {
		if l.Agent == "claude" && claudeSetupToken(l.Auth) == token {
			loginsMu.Unlock()
			return l.User, nil
		}
	}
	loginsMu.Unlock()
	auth, err := json.Marshal(claudeTokenAuth{SetupToken: token})
	if err != nil {
		return "", err
	}
	l := savedLogin{Agent: "claude", User: claudeTokenName(token), Auth: auth}
	if _, err := addLogin(l); err != nil {
		return "", err
	}
	return l.User, nil
}

// ClaudeDirToken is the setup-token of the saved Claude account whose
// config directory is dir (claudeAccountDir), "" when it has a sign-in of
// its own or is no saved account's. A run there is given it, and no other.
func ClaudeDirToken(dir string) string {
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(dir)
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if l.Agent != "claude" {
			continue
		}
		if tok := claudeSetupToken(l.Auth); tok != "" && filepath.Clean(claudeAccountDir(l.User)) == dir {
			return tok
		}
	}
	return ""
}

// claudeTokenAccount says the saved Claude account user was added by a
// setup-token.
func claudeTokenAccount(user string) bool {
	loginsMu.Lock()
	defer loginsMu.Unlock()
	for _, l := range readLogins() {
		if l.Agent == "claude" && strings.EqualFold(l.User, user) {
			return claudeSetupToken(l.Auth) != ""
		}
	}
	return false
}

// claudeTokenDir makes the config directory a setup-token account runs
// in: no sign-in in it (on a refused token Claude Code would otherwise
// take one it found there; addLogin cleared the directory's keychain item
// with it, forgetClaudeDir), only the onboarding Claude Code asks for
// before its first run.
func claudeTokenDir(user string) (string, error) {
	dir := claudeAccountDir(user)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Remove(filepath.Join(dir, ".credentials.json")); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("a sign-in left in %s: %w", dir, err)
	}
	claudeOnboarded(dir, nil)
	return dir, nil
}

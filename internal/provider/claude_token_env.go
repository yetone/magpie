package provider

// Claude Code itself on an account added by a setup-token (liukaybee on X).
// There is no sign-in to put in Claude Code's keychain or
// .credentials.json for such an account, but Claude Code takes the token
// in CLAUDE_CODE_OAUTH_TOKEN, and reads that from the env of its user
// settings.json too, ahead of its stored sign-in (its docs' authentication
// order; tried with Claude Code 2.1.316: a token there is used over a
// valid stored sign-in, and with it taken out the stored sign-in is used
// again). So a switch to such an account writes the token there, and a
// switch to a signed-in account takes it out again.
//
// Claude Code's stored sign-in is left as it is meanwhile: it is still
// the account magpie's own runs of Claude Code are on, since those read
// no settings of the user's (--setting-sources ""), and the account
// Logins calls Active. A setup-token in use is said beside it (Login.Uses).
//
// A token the user set there is theirs: magpie writes none over it and
// never takes it out. magpie keeps the hash of the token it wrote
// (claude-token-env.json), and the value counts as its own only while it
// still has that hash. Claude Code reads its env as it starts, so it is
// the sessions started after a switch that are on the new account.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/edit"
)

const claudeTokenEnvKey = "CLAUDE_CODE_OAUTH_TOKEN"

var claudeTokenEnvMu sync.Mutex

func claudeTokenRecordPath() string {
	return filepath.Join(filepath.Dir(Path()), "claude-token-env.json")
}

// claudeTokenRecord is what magpie last wrote to Claude Code's env: the
// token's hash, never the token.
type claudeTokenRecord struct {
	Hash string `json:"hash"`
}

func claudeTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// readClaudeTokenRecord is the hash magpie wrote, "" for none. One that
// can't be read is an error, never "nothing written": that would make
// magpie's own token look like the user's for good.
func readClaudeTokenRecord() (string, error) {
	b, err := os.ReadFile(claudeTokenRecordPath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var r claudeTokenRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return "", fmt.Errorf("%s: %w", claudeTokenRecordPath(), err)
	}
	return r.Hash, nil
}

func writeClaudeTokenRecord(hash string) error {
	if hash == "" {
		if err := os.Remove(claudeTokenRecordPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, err := json.MarshalIndent(claudeTokenRecord{Hash: hash}, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(claudeTokenRecordPath(), append(b, '\n'))
}

// claudeEnvToken is CLAUDE_CODE_OAUTH_TOKEN in the env of Claude Code's
// settings.json, and whether it is set there. A file that can't be read
// is an error: what it holds is unknown.
func claudeEnvToken() (string, bool, error) {
	path := claudeSettingsFile()
	raw, err := edit.Read(path)
	if err != nil {
		return "", false, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false, nil
	}
	stripped := jsonc.ToJSON(raw)
	if !json.Valid(stripped) {
		return "", false, fmt.Errorf("%s can't be read", path)
	}
	v := gjson.GetBytes(stripped, "env."+claudeTokenEnvKey)
	return v.String(), v.Exists(), nil
}

// claudeTokenInUse is the setup-token magpie has Claude Code use, ""
// when there is none of magpie's in its settings.json (none, the user's
// own, or one that can't be read).
func claudeTokenInUse() string {
	claudeTokenEnvMu.Lock()
	defer claudeTokenEnvMu.Unlock()
	return claudeTokenOursLocked()
}

func claudeTokenOursLocked() string {
	hash, err := readClaudeTokenRecord()
	if err != nil || hash == "" {
		return ""
	}
	v, ok, err := claudeEnvToken()
	if err != nil || !ok || v == "" || claudeTokenHash(v) != hash {
		return ""
	}
	return v
}

// ClaudeTokenUser is the saved setup-token account Claude Code itself
// uses now (magpie's token in its settings.json), "" for none.
func ClaudeTokenUser() string {
	tok := claudeTokenInUse()
	if tok == "" {
		return ""
	}
	loginsMu.Lock()
	defer loginsMu.Unlock()
	return claudeTokenUserIn(readLogins(), tok)
}

func claudeTokenUserIn(ls []savedLogin, tok string) string {
	if tok == "" {
		return ""
	}
	for _, l := range ls {
		if l.Agent == "claude" && claudeSetupToken(l.Auth) == tok {
			return l.User
		}
	}
	return ""
}

// errClaudeOwnToken: the user set CLAUDE_CODE_OAUTH_TOKEN in Claude
// Code's settings.json themselves.
const errClaudeOwnToken = "Claude Code's %s sets CLAUDE_CODE_OAUTH_TOKEN of your own, which Claude Code uses over any account magpie switches it to; magpie leaves it as it is. Take it out there to switch Claude Code here."

// setClaudeEnvToken has Claude Code use token (setClaudeEnvToken("")
// takes magpie's out again), leaving a value the user set alone: putting
// one in over it is an error, taking magpie's out leaves it.
func setClaudeEnvToken(token string) error {
	claudeTokenEnvMu.Lock()
	defer claudeTokenEnvMu.Unlock()
	return setClaudeEnvTokenLocked(token)
}

func setClaudeEnvTokenLocked(token string) error {
	hash, err := readClaudeTokenRecord()
	if err != nil {
		return err
	}
	if token == "" && hash == "" {
		return nil // nothing of magpie's there, nor to be
	}
	path := claudeSettingsFile()
	cur, has, err := claudeEnvToken()
	if err != nil {
		if token == "" {
			// what it holds is unknown: magpie's token may be there yet,
			// and the record is kept to take it out once it can be read
			return err
		}
		return fmt.Errorf("%w; Claude Code is left on the account it is on", err)
	}
	ours := has && hash != "" && claudeTokenHash(cur) == hash
	if token == "" {
		if ours {
			if err := edit.DelJSON(path, "env."+claudeTokenEnvKey); err != nil {
				return err
			}
		}
		return writeClaudeTokenRecord("")
	}
	if has && !ours && cur != "" {
		return fmt.Errorf(errClaudeOwnToken, path)
	}
	// what is about to be written is kept first: a crash between the two
	// then leaves a record of a value that isn't there, never a value of
	// magpie's that looks like the user's
	if err := writeClaudeTokenRecord(claudeTokenHash(token)); err != nil {
		return err
	}
	if has && cur == token {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return edit.SetJSON(path, edit.KV{Path: "env." + claudeTokenEnvKey, Value: token})
}

// claudeTokenGone takes magpie's token for the removed account l out of
// Claude Code's settings.json, so Claude Code goes back to its own
// sign-in; another account's, or the user's own, stays.
func claudeTokenGone(l savedLogin) error {
	tok := claudeSetupToken(l.Auth)
	if tok == "" || l.Agent != "claude" {
		return nil
	}
	claudeTokenEnvMu.Lock()
	defer claudeTokenEnvMu.Unlock()
	if claudeTokenOursLocked() != tok {
		return nil
	}
	return setClaudeEnvTokenLocked("")
}

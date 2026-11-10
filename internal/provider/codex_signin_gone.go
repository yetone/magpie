package provider

// A ChatGPT sign-in OpenAI has refused to refresh for good (jjt on
// Discord, #4308: revoked, or failed for good). Codex keeps such a
// sign-in's auth.json as it was and remembers the refusal in memory alone
// (codex-rs login/src/auth/manager.rs permanent_refresh_failure), so
// magpie read the file as a sign-in and wired Codex beside it, on its
// built-in provider. Every turn there, magpie's models too, then failed at
// Codex's workspace routing discovery (401), and Codex's picker had none of
// magpie's models. magpie refreshes the sign-in itself (codexTokenRenewing,
// on a usage read), so it hears OpenAI's refusal too: kept here for the
// refresh token refused, which the agent package reads as no sign-in
// (CodexSignInGone). A sign-in made again has another refresh token, which
// nothing here names: it counts again with no step of the user's.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lastgood"
)

// codexGoneMu guards the record's file.
var codexGoneMu sync.Mutex

// codexGonePath is the record: a Codex home's folder to the hash of the
// refresh token OpenAI refused for good there.
func codexGonePath() string { return filepath.Join(filepath.Dir(Path()), "codex-signin-gone.json") }

// codexGoneHash names a refresh token in the record without keeping it.
func codexGoneHash(refresh string) string {
	h := sha256.Sum256([]byte(refresh))
	return hex.EncodeToString(h[:])
}

// readCodexGone reads the record. None yet is empty; a file that can't be
// read or doesn't parse is an error: what it says is unknown.
func readCodexGone() (map[string]string, error) {
	m := map[string]string{}
	b, err := os.ReadFile(codexGonePath())
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// codexRefreshCode is the code a token endpoint's refusal carries, read as
// Codex reads it (codex-rs login/src/oauth/error.rs TokenErrorDetail::parse):
// "error" as a string, then error.code, then a top-level code.
func codexRefreshCode(b []byte) string {
	var v map[string]json.RawMessage
	if json.Unmarshal(b, &v) != nil {
		return ""
	}
	text := func(r json.RawMessage) string {
		var s string
		if json.Unmarshal(r, &s) == nil && strings.TrimSpace(s) != "" {
			return s
		}
		return ""
	}
	if s := text(v["error"]); s != "" {
		return s
	}
	var e map[string]json.RawMessage
	if json.Unmarshal(v["error"], &e) == nil {
		if s := text(e["code"]); s != "" {
			return s
		}
	}
	return text(v["code"])
}

// codexRefreshGone says a refresh refusal is for good, by Codex's own rule
// (codex-rs login/src/auth/manager.rs request_chatgpt_token_refresh):
// refresh_token_expired, refresh_token_reused or refresh_token_invalidated,
// a 400 invalid_grant, or a 401. A 401 counts only with the endpoint's
// error code in it: one with no code (a proxy's page) says nothing of the
// sign-in. A 5xx, a 403, a 429 or any other 400 is a hiccup, as Codex
// takes it.
func codexRefreshGone(status int, body []byte) bool {
	code := strings.ToLower(codexRefreshCode(body))
	switch {
	case code == "refresh_token_expired", code == "refresh_token_reused", code == "refresh_token_invalidated":
		return true
	case status == http.StatusBadRequest && code == "invalid_grant":
		return true
	}
	return status == http.StatusUnauthorized && code != ""
}

// codexRefreshGoneError is a refresh OpenAI refused for good
// (codexRefreshGone); the refusal it wraps is told as before.
type codexRefreshGoneError struct{ error }

func (e codexRefreshGoneError) Unwrap() error { return e.error }

// noteCodexSignInGone keeps refresh, the refresh token OpenAI just refused
// for good, as dead for the Codex home dir: only while the store still
// holds it. One Codex refreshed meanwhile (its own refresh spent the token
// magpie sent: refresh_token_reused), or a sign-in made since, is another
// token and isn't marked.
func noteCodexSignInGone(dir, refresh string) {
	if refresh == "" {
		return
	}
	b, _, err := readCodexLogin(dir, true)
	var a codexAuth
	if err != nil || json.Unmarshal(b, &a) != nil || a.Tokens.RefreshToken != refresh {
		return
	}
	setCodexGone(dir, codexGoneHash(refresh))
}

// forgetCodexSignInGone takes dir out of the record, its sign-in refreshed.
func forgetCodexSignInGone(dir string) { setCodexGone(dir, "") }

// setCodexGone records hash for dir ("" forgets it), the agents' files told
// when it changed.
func setCodexGone(dir, hash string) {
	codexGoneMu.Lock()
	defer codexGoneMu.Unlock()
	k := filepath.Clean(dir)
	m, err := readCodexGone()
	if err != nil {
		if hash == "" {
			return
		}
		m = map[string]string{}
	}
	if m[k] == hash {
		return
	}
	if hash == "" {
		delete(m, k)
	} else {
		m[k] = hash
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err == nil {
		// a file that doesn't parse is kept aside before it is written over
		err = lastgood.KeepUnparsed(codexGonePath(), lastgood.Into[map[string]string])
	}
	if err == nil {
		err = writePrivate(codexGonePath(), append(out, '\n'))
	}
	if err != nil {
		log.Printf("codex: keeping that the sign-in in %s is gone: %v", dir, err)
		return
	}
	if hash != "" {
		log.Printf("codex: OpenAI refused the sign-in in %s for good; magpie takes Codex as signed out until it is signed in again", dir)
	}
	if k == filepath.Clean(codexHome()) {
		codexWasGone.Store(hash != "")
	}
	catalog.Touched()
}

// CodexSignInGone says whether auth, the sign-in read from the Codex home
// dir, is one OpenAI refused to refresh for good (noteCodexSignInGone).
// known false: the record couldn't be read, which says nothing either way.
func CodexSignInGone(dir string, auth []byte) (gone, known bool) {
	m, err := readCodexGone()
	if err != nil {
		var a codexAuth
		if json.Unmarshal(auth, &a) != nil || a.Tokens.RefreshToken == "" {
			return false, true
		}
		return false, false
	}
	// no refusal on record for dir, the usual case: nothing to compare,
	// so the sign-in isn't parsed and hashed on each poll
	want := m[filepath.Clean(dir)]
	if want == "" {
		return false, true
	}
	var a codexAuth
	if json.Unmarshal(auth, &a) != nil || a.Tokens.RefreshToken == "" {
		return false, true
	}
	return want == codexGoneHash(a.Tokens.RefreshToken), true
}

// codexWasGone is whether Codex's own sign-in was gone at the last look.
var codexWasGone atomic.Bool

// noteCodexGoneNow tells the agents' files when Codex's own sign-in stops
// being gone: signed in again, in Codex or anywhere else that writes its
// auth.json, which magpie hears of from no refresh of its own. Run from
// KeepOnAnAccountWithRoom's loop. A read that says nothing changes nothing.
func noteCodexGoneNow() {
	dir := codexHome()
	b, ok, known := CodexSignInKnown(dir)
	if !known {
		return
	}
	gone := false
	if ok {
		if gone, known = CodexSignInGone(dir, b); !known {
			return
		}
	}
	if codexWasGone.Swap(gone) != gone {
		catalog.Touched()
	}
}

// NoteCodexSignInGoneForTest records refresh as refused for good in dir,
// as a refresh OpenAI refused would, and says where the record is.
func NoteCodexSignInGoneForTest(dir, refresh string) (record string) {
	setCodexGone(dir, codexGoneHash(refresh))
	return codexGonePath()
}

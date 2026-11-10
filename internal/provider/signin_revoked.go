package provider

// A subscription account whose sign-in its vendor revoked. ChatGPT answers
// a request on a revoked token 401 "token_revoked" (ysicing, #1793), and
// waiting doesn't mend it: the account was signed out elsewhere, its
// password changed, its session ended. magpie used to send every request
// to it all the same, each refused, the account never said to be signed
// out. Now the account is marked lapsed, for the credential refused: the
// gateway passes over it while it still has that credential, without
// asking the vendor, and its row says to sign in again. A credential
// changed since (refreshed, or the account signed in again) is not the
// one refused, and a request it serves clears the mark.
//
// Claude's sign-ins keep their own mark (claude_auth.go), as do Copilot's
// (copilot_lapsed.go); a plugin's account is its plugin's to mark.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
)

// revokedPrefix starts a savedLogin's Refused when it holds the mark of a
// credential its vendor revoked.
const revokedPrefix = "revoked:"

// revokedWords: a 401 that says the sign-in itself is gone, not expired
// for a refresh to mend (token_expired) nor the request's fault. ChatGPT's
// token_revoked ("Encountered invalidated oauth token for user") and the
// refresh codes OpenAI's auth answers a dead sign-in with; and magpie's own
// words for one (revokedText), which the gateway may hold in place of the
// reply's body.
var revokedWords = lazyre.New(`(?i)\btoken_revoked\b|\btoken_invalidated\b|\brefresh_token_(reused|invalidated|expired)\b|\binvalid_grant\b|invalidated oauth token|(token|sign-in) (has been|was) revoked`)

// SignInRevoked says a reply is the vendor's word that the sign-in it was
// sent with is revoked.
func SignInRevoked(status int, body []byte) bool {
	return status == http.StatusUnauthorized && revokedWords.Match(body)
}

// RevokedAccount says the account's revoked sign-in is marked here: one
// of the built-in subscriptions that keep no mark of their own.
func RevokedAccount(a *Account) bool {
	return a != nil && a.plugin == nil && a.User != "" && a.Agent != "claude" && a.Agent != "copilot"
}

// credentialMark tells the credential a request went with from another
// without keeping it.
func credentialMark(h http.Header) string {
	for _, k := range []string{"Authorization", "X-Api-Key", "Cookie"} {
		if v := h.Get(k); v != "" {
			sum := sha256.Sum256([]byte(k + "\x00" + v))
			return hex.EncodeToString(sum[:12])
		}
	}
	return ""
}

// revoked is the credentials known revoked, by mark, each with why; and
// the mark of each account marked so, by agent and user, which a served
// request clears. Loaded from logins.json the first time either is asked.
var revoked = struct {
	sync.Mutex
	loaded   bool
	marks    map[string]string
	accounts map[string]string
}{marks: map[string]string{}, accounts: map[string]string{}}

func revokedKey(agent, user string) string { return agent + "\x00" + strings.ToLower(user) }

// revokedLoad reads the marks kept in logins.json; revoked is held.
func revokedLoad() {
	if revoked.loaded {
		return
	}
	revoked.loaded = true
	loginsMu.Lock()
	ls := readLogins()
	loginsMu.Unlock()
	for _, l := range ls {
		if m, ok := strings.CutPrefix(l.Refused, revokedPrefix); ok && l.Lapsed != "" && m != "" {
			revoked.marks[m] = l.Lapsed
			revoked.accounts[revokedKey(l.Agent, l.User)] = m
		}
	}
}

// resetRevoked forgets what is known, to be read from logins.json again.
func resetRevoked() {
	revoked.Lock()
	defer revoked.Unlock()
	revoked.loaded = false
	revoked.marks, revoked.accounts = map[string]string{}, map[string]string{}
}

// revokedText is what a revoked account's row says.
func revokedText(user string) string {
	return user + "'s sign-in was revoked; sign in again"
}

// RevokedSignIn is why the request req, signed for account a, isn't sent:
// its vendor revoked the credential it carries. ok is false otherwise.
func RevokedSignIn(a *Account, req *http.Request) (why string, ok bool) {
	if !RevokedAccount(a) {
		return "", false
	}
	m := credentialMark(req.Header)
	if m == "" {
		return "", false
	}
	revoked.Lock()
	defer revoked.Unlock()
	revokedLoad()
	why, ok = revoked.marks[m]
	return why, ok
}

// SignInMarked says account a is marked revoked: a request is asked of
// another account first where the gateway picks one alone (compaction).
func SignInMarked(a *Account) bool {
	if !RevokedAccount(a) {
		return false
	}
	revoked.Lock()
	defer revoked.Unlock()
	revokedLoad()
	_, ok := revoked.accounts[revokedKey(a.Agent, a.User)]
	return ok
}

// NoteRevokedSignIn keeps that the vendor revoked the credential req went
// to account a with. A saved ChatGPT account is refreshed first, as Codex
// does on a 401: a refresh that goes through is a new sign-in, and the
// account isn't marked.
func NoteRevokedSignIn(ctx context.Context, a *Account, req *http.Request) {
	if !RevokedAccount(a) {
		return
	}
	m := credentialMark(req.Header)
	if m == "" {
		return
	}
	if a.Agent == "codex" && a.token != nil {
		// refreshed, the next request goes with the new token; refused
		// or unanswered, the token sent is revoked all the same
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		_, _, err := renewSavedLogin(rctx, a.Agent, a.User, true)
		cancel()
		if err == nil {
			return
		}
	}
	markRevoked(a.Agent, a.User, m)
}

// markRevoked marks the account of agent and user revoked for the
// credential whose mark is m: the gateway passes over it while it has
// that credential, and its row says to sign in again.
func markRevoked(agent, user, m string) {
	why := revokedText(user)
	revoked.Lock()
	revokedLoad()
	k := revokedKey(agent, user)
	if was, ok := revoked.accounts[k]; ok && was != m {
		delete(revoked.marks, was) // logins.json keeps one mark an account
	}
	revoked.marks[m] = why
	revoked.accounts[k] = m
	revoked.Unlock()
	_ = editSideLogin(agent, user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		ls[i].Lapsed, ls[i].Refused = why, revokedPrefix+m
		return ls, nil
	})
}

// bearerMark is credentialMark of a request sent with access token tok,
// as the gateway and a usage read send a ChatGPT account's.
func bearerMark(tok string) string {
	if tok == "" {
		return ""
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok)
	return credentialMark(h)
}

// ServedSignIn notes a request account a's vendor served: a mark of a
// revoked sign-in it has is gone, as the account was signed in again or
// its sign-in refreshed.
func ServedSignIn(a *Account) {
	if !RevokedAccount(a) {
		return
	}
	k := revokedKey(a.Agent, a.User)
	revoked.Lock()
	revokedLoad()
	m, marked := revoked.accounts[k]
	delete(revoked.accounts, k)
	delete(revoked.marks, m) // as after a restart, which reads it gone
	revoked.Unlock()
	if !marked {
		return
	}
	_ = editSideLogin(a.Agent, a.User, func(ls []savedLogin, i int) ([]savedLogin, error) {
		if !strings.HasPrefix(ls[i].Refused, revokedPrefix) {
			return nil, errUnchanged
		}
		ls[i].Lapsed, ls[i].Refused = "", ""
		return ls, nil
	})
}

// sameAuth says two credential blobs are the same sign-in, however each
// was laid out (logins.json indents what auth.json may not).
func sameAuth(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

// revokedLapse is a saved account's lapse when it is a revoked sign-in's.
func revokedLapse(l savedLogin) string {
	if strings.HasPrefix(l.Refused, revokedPrefix) {
		return l.Lapsed
	}
	return ""
}

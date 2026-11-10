package provider

// A ChatGPT sign-in the backend calls expired while its access token has
// time left by its own exp (willz on Discord, #2973: two saved Team
// accounts, /wham/usage answering 401 "Provided authentication token is
// expired. Please try refreshing it." until each was signed in again).
// magpie refreshes a token only as its exp nears, so it kept sending the
// one turned away. Such an answer is mended by one refresh: forced here,
// and the read made once more with the new token, never more.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
)

// expiredWords: a 401 that says the access token has expired and asks
// for a refresh. Not refresh_token_expired ("_" is a word character, so
// \b doesn't split it there), which says the sign-in itself is gone
// (revokedWords).
var expiredWords = lazyre.New(`(?i)\btoken_expired\b|authentication token (is|has) expired|please try refreshing it`)

// SignInExpired says a reply is the vendor's word that the access token
// it was sent with has expired, for a refresh to mend.
func SignInExpired(status int, body []byte) bool {
	return status == http.StatusUnauthorized && expiredWords.Match(body)
}

// codexRenew refreshes a ChatGPT sign-in in place of stale, the access
// token the backend called expired (codexTokenRenewing,
// renewSavedLoginFrom).
type codexRenew func(ctx context.Context, stale string) (tok, accountID string, err error)

// codexExpired says err is the backend calling the token expired.
func codexExpired(err error) bool {
	var se *accountStatusError
	return errors.As(err, &se) && SignInExpired(se.status, se.body)
}

// errCodexStillExpired is a usage read turned away as expired again with
// the token refreshed just for it: nothing magpie holds will do.
var errCodexStillExpired = errors.New("ChatGPT turned the refreshed sign-in away too; sign in to this account again")

// codexRevoked says err is the backend calling the sign-in itself gone
// (SignInRevoked: token_revoked, an invalidated oauth token).
func codexRevoked(err error) bool {
	var se *accountStatusError
	return errors.As(err, &se) && SignInRevoked(se.status, se.body)
}

// codexSignInGone is a usage read turned away for the sign-in itself with
// a refresh tried for it (charliepu on Discord: an account showing no
// allowance until it was signed in again): the account is to be marked
// signed out for tok, the access token turned away, and signed in again
// in magpie. A refresh refused is told by refreshRefused instead.
type codexSignInGone struct {
	tok string
	err error
}

func (e *codexSignInGone) Error() string { return e.err.Error() }
func (e *codexSignInGone) Unwrap() error { return e.err }

// codexWindowsRenewing is codexWindows, read once more with the sign-in
// renewed when the backend calls the token expired or the sign-in
// revoked, as Codex refreshes on a 401. A refresh refused says to sign in
// again (codexRefresh, savedRefreshFailed); a refreshed token turned away
// the same is a codexSignInGone. Any other 401, a 403, a 5xx or a network
// error is returned as it is: what it says of the sign-in is unknown.
func codexWindowsRenewing(ctx context.Context, tok, accountID string, renew codexRenew) (plan string, out []QuotaWindow, resets *ResetCredits, credits string, held bool, err error) {
	plan, out, resets, credits, held, err = codexWindows(ctx, tok, accountID)
	if renew == nil || !codexExpired(err) && !codexRevoked(err) {
		return
	}
	fresh, id, rerr := renew(ctx, tok)
	if rerr != nil {
		return plan, out, resets, credits, held, rerr
	}
	if id != "" {
		accountID = id
	}
	plan, out, resets, credits, held, err = codexWindows(ctx, fresh, accountID)
	switch {
	case codexExpired(err):
		err = &codexSignInGone{tok: fresh, err: errCodexStillExpired}
	case codexRevoked(err):
		err = &codexSignInGone{tok: fresh, err: err}
	}
	return
}

// codexGoneFor is the access token a ChatGPT account's usage read says is
// signed out for good: one a refreshed read was turned away for, or, for
// a refresh refused, the token held (heldTok, read again from auth when
// the refresh came before any read). "" when err says nothing of the
// sign-in: a failed read is unknown, not signed out.
func codexGoneFor(err error, heldTok string, auth func() []byte) string {
	var gone *codexSignInGone
	if errors.As(err, &gone) {
		return gone.tok
	}
	var refused refreshRefused
	if !errors.As(err, &refused) {
		return ""
	}
	if heldTok != "" {
		return heldTok
	}
	var a codexAuth
	if b := auth(); b != nil && json.Unmarshal(b, &a) == nil {
		return a.Tokens.AccessToken
	}
	return ""
}

// NoteExpiredSignIn renews the ChatGPT sign-in of a, whose request req the
// backend answered 401 token expired while the token had time left by
// its exp, so the next request goes with a new one. The request itself
// isn't sent again here; the routing loop passes over it as an auth
// failure. A refresh refused marks a saved account lapsed
// (savedRefreshFailed).
func NoteExpiredSignIn(ctx context.Context, a *Account, req *http.Request) {
	if a == nil || a.renew == nil || a.plugin != nil || req == nil {
		return
	}
	stale, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok || stale == "" {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	if _, _, err := a.renew(rctx, stale); err != nil {
		log.Printf("codex: %s's token was called expired and couldn't be renewed: %v", a.User, err)
	}
}

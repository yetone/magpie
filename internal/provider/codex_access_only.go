package provider

// A ChatGPT account kept "access token only" (EastonXvii on X): magpie
// sends its requests with the access token it was given and never
// refreshes the sign-in. A refresh rotates the refresh token, and the
// sign-in it came from (Codex on another computer, the tool the file was
// exported from) is then signed out. Kept so, that one stays signed in,
// and magpie uses the account until its access token runs out (about ten
// days for a Codex sign-in): from then on it is lapsed, passed over by the
// gateway, and its row says to import it again or sign in to it again.
//
// Every path that renews a saved ChatGPT sign-in goes through renewSaved
// (requests, usage reads and their idle look, the warm-up, a 401's
// recovery, a revoked sign-in's check), which never refreshes such an
// account; the keep-alive passes it over. Codex itself is never signed in
// to it (switchSavedLogin), as Codex would refresh it as it runs.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// errCodexAccessOnlySwitch: Codex is never signed in to an account kept
// access token only, which it would refresh.
const errCodexAccessOnlySwitch = "%s is kept access token only: magpie's requests use it, but Codex itself isn't signed in to it, as Codex would renew its sign-in"

// accessOnlyLapse is what an account kept access token only says once
// its access token has run out, or ChatGPT turned it away.
func accessOnlyLapse(user string) string {
	return user + "'s access token has expired, and it is kept access token only, so magpie doesn't renew it — import it again, or sign in to it again"
}

// accessOnlyExpired says the access token of the Codex sign-in auth has
// run out by now. One whose expiry can't be read hasn't.
func accessOnlyExpired(auth json.RawMessage, now time.Time) bool {
	var a codexAuth
	return json.Unmarshal(auth, &a) == nil && accessTokenRunOut(a.Tokens.AccessToken, now)
}

// accessTokenRunOut says the JWT access token tok has run out by now.
func accessTokenRunOut(tok string, now time.Time) bool {
	exp, _ := jwtClaims(tok)["exp"].(float64)
	return exp != 0 && !now.Before(time.Unix(int64(exp), 0))
}

// accessOnlyToken is renewSaved for a saved ChatGPT account kept access
// token only: its access token while it has time left by its own exp
// (not refreshed five minutes early: it can't be), and never a refresh.
// One run out, or turned away by the backend (stale), lapses the account,
// and one lapsed so stays lapsed until it is imported again.
// A refresh forced with the token still good (a revoked sign-in's check)
// is an error that lapses nothing.
func accessOnlyToken(user, lapsed string, a codexAuth, accountID string, force bool, stale string) (string, string, error) {
	tok := a.Tokens.AccessToken
	if stale != "" && tok != stale {
		return tok, accountID, nil // imported again since
	}
	if lapsed == accessOnlyLapse(user) {
		// turned away already: imported again, it is a new entry
		return "", "", refreshRefused(lapsed)
	}
	if stale == "" && !accessTokenRunOut(tok, time.Now()) {
		if force {
			return "", "", fmt.Errorf("%s is kept access token only: magpie doesn't renew its sign-in", user)
		}
		return tok, accountID, nil
	}
	msg := accessOnlyLapse(user)
	_ = editSideLogin("codex", user, func(ls []savedLogin, i int) ([]savedLogin, error) {
		ls[i].Lapsed = msg
		return ls, nil
	})
	return "", "", refreshRefused(msg)
}

// SetLoginAccessOnly keeps a saved ChatGPT account access token only, or
// lets magpie renew its sign-in again. The account Codex is signed in to,
// here or in another of its folders, can't be kept so: Codex renews it
// itself as it runs.
func SetLoginAccessOnly(agent, user string, on bool) error {
	if discoveryOff() {
		return errAccountDiscoveryOff
	}
	if agent != "codex" {
		return fmt.Errorf("only ChatGPT accounts can be kept access token only")
	}
	// not while the account is being refreshed: that refresh would finish
	// after the account was said to be left alone
	savedTokenMu.Lock()
	defer savedTokenMu.Unlock()
	loginsMu.Lock()
	defer loginsMu.Unlock()
	ls := readLogins()
	i := pickSaved(ls, func(l savedLogin) bool { return l.Agent == agent && strings.EqualFold(l.User, user) })
	if i < 0 {
		return fmt.Errorf("no saved %s account %q", agent, user)
	}
	l := ls[i]
	if on {
		if live, ok := liveLogin(agent); ok && (sameLogin(live, l) || sameRefresh(live.Auth, l.Auth)) {
			return fmt.Errorf("Codex is signed in to %s, and renews its sign-in itself as it runs: switch Codex to another account first", l.User)
		}
		if home := codexHeldIn(l); home != "" {
			return fmt.Errorf("%s is signed in to Codex in %s, which renews its sign-in itself as it runs", l.User, home)
		}
	}
	if l.AccessOnly == on {
		return nil
	}
	ls[i].AccessOnly = on
	if !on && ls[i].Lapsed == accessOnlyLapse(l.User) {
		// renewed again from now on: the next request refreshes it, or
		// says the refresh was refused
		ls[i].Lapsed = ""
	}
	if err := writeLogins(ls); err != nil {
		return err
	}
	loginsSeenAt.Store(0)
	forgetAccountCaches()
	return nil
}

// errAccessOnlyHeld: an account Codex is signed in to on this computer
// isn't imported access token only; Codex renews it.
var errAccessOnlyHeld = errors.New("Codex on this computer is signed in to this account and renews its sign-in itself; import it without \"access token only\", or sign Codex out of it first")

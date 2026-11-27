package provider

// A Copilot account whose GitHub token GitHub refuses is signed out: GitHub
// revokes a token (signed out on github.com, the OAuth app's access taken
// back, a token unused for a year), and every request then failed with
// "Copilot is signed out …; sign in to Copilot again" while the account
// still read as signed in, to be removed and added anew (echo_ts on
// Discord). Such an account is now marked lapsed on its own row, where it
// is signed in again in place, its settings kept.
//
// Only GitHub's refusal of the token marks it — 401 on the trade for a
// session token or on the CLI's /copilot_internal/user, 403 on the trade
// — never a 5xx or a network error; and an editor's token refused while
// the Copilot CLI's sign-in of the same account stands in for it is no
// lapse (#1238). The mark is kept with a digest of the token refused, so
// a new sign-in (magpie's, the editor's or the CLI's) is never called
// signed out for the old one, and a request served on the account clears it.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
)

// copilotLapsedText is what a lapsed Copilot account's row says.
const copilotLapsedText = "GitHub refused this Copilot sign-in; sign in again"

// copilotTokenMark tells a GitHub token from another without keeping it.
func copilotTokenMark(token string) string {
	sum := sha256.Sum256([]byte("copilot\x00" + token))
	return hex.EncodeToString(sum[:12])
}

// copilotLapsedOn says a saved Copilot account is signed out with token:
// it was marked so while it had this very token.
func copilotLapsedOn(l savedLogin, token string) bool {
	return l.Lapsed != "" && token != "" && slices.Contains(strings.Split(l.Refused, ","), copilotTokenMark(token))
}

// copilotLapsedSeen is the Copilot accounts known marked lapsed, by
// accountKey: a request served on one clears its mark without reading
// logins.json at every request.
var (
	copilotLapsedMu   sync.Mutex
	copilotLapsedSeen = map[string]bool{}
)

func copilotLapsedNote(user string, lapsed bool) {
	copilotLapsedMu.Lock()
	defer copilotLapsedMu.Unlock()
	if lapsed {
		copilotLapsedSeen[accountKey(user)] = true
	} else {
		delete(copilotLapsedSeen, accountKey(user))
	}
}

var errUnchanged = errors.New("unchanged")

// copilotLapsedBy says GitHub refused app's sign-in, with nothing standing
// in for it: its token refused, and an editor's token's stand-in, the
// Copilot CLI's sign-in of the same account, refused too or not there.
func copilotLapsedBy(app copilotApp) bool {
	if !copilotRefusedToken(app.Token) {
		return false
	}
	if app.cli || app.src == "" {
		return true
	}
	if cli, ok := copilotStandIn(app); ok && !copilotRefusedToken(cli.Token) {
		return false
	}
	return true
}

// copilotLapse marks the account of app signed out, with the token GitHub
// refused (and, for an editor's, its stand-in's too, which copilotLogins
// may list it with).
func copilotLapse(app copilotApp) {
	if app.User == "" {
		return
	}
	marks := []string{copilotTokenMark(app.Token)}
	if app.src != "" && !app.cli {
		if cli, ok := copilotStandIn(app); ok {
			marks = append(marks, copilotTokenMark(cli.Token))
		}
	}
	err := editSideLogin("copilot", app.User, func(ls []savedLogin, i int) ([]savedLogin, error) {
		was := strings.Split(ls[i].Refused, ",")
		if ls[i].Lapsed == copilotLapsedText && !slices.ContainsFunc(marks, func(m string) bool { return !slices.Contains(was, m) }) {
			return nil, errUnchanged
		}
		if ls[i].Lapsed != copilotLapsedText {
			was = nil
		}
		for _, m := range marks {
			if !slices.Contains(was, m) {
				was = append(was, m)
			}
		}
		ls[i].Lapsed, ls[i].Refused = copilotLapsedText, strings.Join(slices.DeleteFunc(was, func(m string) bool { return m == "" }), ",")
		return ls, nil
	})
	if err == nil || errors.Is(err, errUnchanged) {
		copilotLapsedNote(app.User, true)
	}
}

// copilotUnlapse clears the mark of the account of app: GitHub served it.
func copilotUnlapse(app copilotApp) {
	copilotLapsedMu.Lock()
	seen := copilotLapsedSeen[accountKey(app.User)]
	copilotLapsedMu.Unlock()
	if !seen || app.User == "" {
		return
	}
	err := editSideLogin("copilot", app.User, func(ls []savedLogin, i int) ([]savedLogin, error) {
		if ls[i].Lapsed == "" && ls[i].Refused == "" {
			return nil, errUnchanged
		}
		ls[i].Lapsed, ls[i].Refused = "", ""
		return ls, nil
	})
	if err == nil || errors.Is(err, errUnchanged) {
		copilotLapsedNote(app.User, false)
	}
}

// copilotSessionDone notes what GitHub said of app's sign-in at a trade:
// served, its mark goes; refused with nothing in its place, it is marked.
func copilotSessionDone(app copilotApp, err error) {
	if err == nil || copilotToSSaid(err) {
		// served, or turned away under GitHub's terms, which is no
		// refusal of the sign-in: a mark an earlier such refusal left,
		// read as one, goes (copilot_tos.go)
		copilotUnlapse(app)
		return
	}
	if copilotLapsedBy(app) {
		copilotLapse(app)
	}
}

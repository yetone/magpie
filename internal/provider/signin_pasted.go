package provider

// A browser sign-in's end pasted where accounts are imported. magpie in
// Docker or on a server sends the browser back to a localhost address that
// won't load there, and the sign-in takes that address in its own Callback
// URL field. LuvLetter (Discord, 10-27) pasted it into the Import box beside
// it instead, which read the address as a refresh token, sent it to ChatGPT
// and said the token had been used since. An import now knows such an
// address, or the code#state Claude's page shows, for what it is: it
// finishes the sign-in it came from, as the Callback URL field would, and
// is never read as an account or sent to a vendor as a token.

import (
	"errors"
	"net/url"
	"strings"

	"github.com/yetone/magpie/internal/lazyre"
)

// errSignInAddressOver is a sign-in's end pasted after its sign-in is over,
// or for none magpie started.
var errSignInAddressOver = errors.New("this is the address a sign-in's browser ended on, not an account to import: it finishes only the sign-in that sent the browser there, which isn't waiting any more. Sign in again, and when the page won't load, paste its address into the sign-in's Callback URL field")

// claudePageCode is the code#state Claude's page shows for Claude Code's
// sign-in, typed back into `claude auth login`.
var claudePageCode = lazyre.New(`^[A-Za-z0-9_.~-]+#([A-Za-z0-9_.~-]+)$`)

// signInEndState is the OAuth state a pasted text carries when the text is
// a browser sign-in's end: an http(s) address with a code (or an error) and
// a state in its query, or Claude's code#state. ok is false for anything
// else, an account file or a refresh token among them.
func signInEndState(text string) (state string, ok bool) {
	s := strings.TrimSpace(text)
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return "", false
	}
	if m := claudePageCode.FindStringSubmatch(s); m != nil {
		return m[1], true
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	q := u.Query()
	if q.Get("code") == "" && q.Get("error") == "" {
		return "", false
	}
	return q.Get("state"), true
}

// looksLikeAddress is a line of an import that is a web address: never a
// refresh token, so never sent to a vendor as one.
func looksLikeAddress(line string) bool {
	return strings.Contains(line, "://")
}

// signInByState finds the sign-in whose vendor page carries state: the
// one a pasted end of a sign-in belongs to.
func signInByState(state string) *signInFlow {
	if state == "" {
		return nil
	}
	signIns.Lock()
	defer signIns.Unlock()
	for _, s := range signIns.m {
		s.mu.Lock()
		own, link := s.state, s.st.URL
		s.mu.Unlock()
		if own == state {
			return s
		}
		if u, err := url.Parse(link); err == nil && link != "" && u.Query().Get("state") == state {
			return s
		}
	}
	return nil
}

// PastedSignIns takes out of an import's texts each one that is a browser
// sign-in's end and finishes the sign-in it belongs to with it, as its
// Callback URL field does. done says what became of each, in the texts'
// order; rest are the texts left to import as accounts.
func PastedSignIns(texts []string) (done []ImportedAccount, rest []string) {
	for _, text := range texts {
		state, ok := signInEndState(text)
		if !ok {
			rest = append(rest, text)
			continue
		}
		done = append(done, finishPastedSignIn(state, strings.TrimSpace(text)))
	}
	return done, rest
}

func finishPastedSignIn(state, text string) ImportedAccount {
	s := signInByState(state)
	if s == nil {
		return ImportedAccount{Status: "failed", Error: errSignInAddressOver.Error()}
	}
	st := s.status()
	if st.State != "waiting" {
		return ImportedAccount{Status: "failed", Error: errSignInAddressOver.Error()}
	}
	if err := SubmitSignInCallback(st.ID, text); err != nil {
		return ImportedAccount{Status: "failed", Error: err.Error()}
	}
	st = s.status()
	out := ImportedAccount{User: st.User, Plan: st.Plan, Status: "added"}
	if st.Again {
		out.Status = "updated"
	}
	return out
}

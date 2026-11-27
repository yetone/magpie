package provider

// GitHub's 403 on the trade for a session token, "Resource not accessible
// by integration. For more on scraping GitHub and how it may affect your
// rights, please review our Terms of Service (…)" (yidasanqian on X), is
// not the token refused: a revoked token is 401 "Bad credentials". The
// tail is GitHub's words for its abuse and rate limits (the same tail as
// its "API rate limit exceeded … 403"), and GitHub Security suspends
// Copilot for scripted or excessive use (copilot-api#84). Read as a
// refused sign-in, the account was marked signed out, which signing in
// again doesn't mend, and every request traded again: the gateway asked
// the same one again as a busy 502, 15 trades and 37s a request, each one
// more to GitHub from an account it had just turned away for its use.
//
// Such a refusal holds the token now: GitHub is not asked again with it
// until Retry-After, a rate-limit reset, or an hour; the account isn't
// marked signed out (an earlier mark goes); and the error ends in
// CopilotToSHint, which the gateway reads as the account restricted
// (failRestricted): it rests, and is not asked again in place.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
)

// CopilotToSHint is what magpie adds to GitHub's refusal of a Copilot
// account under its terms. The gateway reads it as the account restricted
// (failRestricted), and the Routing page says it apart.
const CopilotToSHint = "GitHub turned this account's Copilot requests away under its Terms of Service (its limits on automated and heavy use), not for its sign-in, so signing in again doesn't help; the account rests, and other accounts and providers are used. If it lasts, look for mail from GitHub about the account, or ask GitHub Support"

// copilotToSWords are GitHub's words for a refusal under its terms or its
// rate limits: the Terms of Service tail of its abuse and rate-limit
// answers, and "rate limit" itself (primary and secondary).
var copilotToSWords = lazyre.New(`(?i)for more on scraping github|github terms of service|review our terms of service|rate limit`)

// copilotToSRefused says GitHub's answer to a Copilot sign-in's call
// (the trade, or /copilot_internal/user) turned the account away under
// its terms or its rate limits, not its token.
func copilotToSRefused(status int, body []byte) bool {
	if status != http.StatusForbidden && status != http.StatusTooManyRequests {
		return false
	}
	return copilotToSWords.Match(body)
}

// copilotToSHold is how long a token refused so is held when GitHub says
// nothing of when: the hour a refused token is held for (copilotRefusals).
const copilotToSHold = time.Hour

// copilotToSHolds are the GitHub tokens refused under GitHub's terms, by
// token: until when, and the error each call made with one meanwhile
// gives, without asking GitHub.
var copilotToSHolds = struct {
	sync.Mutex
	m map[string]copilotHold
}{m: map[string]copilotHold{}}

type copilotHold struct {
	until time.Time
	said  string
}

// copilotHeld is the error for a call with token while GitHub's refusal
// of it under its terms holds.
func copilotHeld(token string) error {
	copilotToSHolds.Lock()
	defer copilotToSHolds.Unlock()
	h, ok := copilotToSHolds.m[token]
	if !ok {
		return nil
	}
	if time.Now().After(h.until) {
		delete(copilotToSHolds.m, token)
		return nil
	}
	return errors.New(h.said)
}

// copilotToSHeld holds token after GitHub refused it under its terms, for
// as long as its Retry-After or rate-limit reset says, within a minute
// and a day, else copilotToSHold; and is the error that says so.
func copilotToSHeld(token string, h http.Header, status string, body []byte) error {
	d := copilotToSHold
	now := time.Now()
	if v := h.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			d = time.Duration(n) * time.Second
		} else if t, err := http.ParseTime(v); err == nil {
			d = t.Sub(now)
		}
	} else if h.Get("X-Ratelimit-Remaining") == "0" {
		if n, err := strconv.ParseInt(h.Get("X-Ratelimit-Reset"), 10, 64); err == nil {
			d = time.Unix(n, 0).Sub(now)
		}
	}
	d = min(max(d, time.Minute), 24*time.Hour)
	said := "GitHub refused this Copilot account (" + APIError(body, status) + ") — " + CopilotToSHint
	copilotToSHolds.Lock()
	copilotToSHolds.m[token] = copilotHold{until: now.Add(d), said: said}
	copilotToSHolds.Unlock()
	return errors.New(said)
}

// copilotToSSaid says err is GitHub's refusal under its terms, as
// copilotToSHeld words it: no refusal of the sign-in.
func copilotToSSaid(err error) bool {
	return err != nil && strings.HasSuffix(err.Error(), CopilotToSHint)
}

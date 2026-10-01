package provider

// Keeping saved accounts signed in. A ChatGPT account saved in magpie but
// not the one Codex is signed in to is refreshed only when it is used or
// its allowance looked at — and one waiting its turn (smart
// routing leaves the accounts whose week resets later for days) could sit
// unused past its refresh token's life, and be found signed out just when
// it's needed. So whichever magpie runs the gateway renews each such
// sign-in about once a day. The account the agent is signed in to is the
// agent's own to refresh, and is left to it. A Claude account is never
// refreshed by magpie: Claude Code refreshes it, whenever it runs on it
// (claude_dirs.go).

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"time"
)

// keepAliveEvery is how long a saved sign-in goes before it is renewed.
const keepAliveEvery = 24 * time.Hour

// Renewal is what came of renewing one saved account.
type Renewal struct {
	Agent string `json:"agent"`
	User  string `json:"user"`
	// Renewed is false when it wasn't due; Err is why it failed, and Lapsed
	// whether the sign-in is gone (the vendor refused it) or may yet renew.
	Renewed bool   `json:"renewed"`
	Err     string `json:"error,omitempty"`
	Lapsed  bool   `json:"lapsed,omitempty"`
}

// RenewLogins renews the saved ChatGPT sign-ins not in the
// agent's hands that have gone every or longer without it; every 0 renews
// all of them.
func RenewLogins(ctx context.Context, every time.Duration) []Renewal {
	rememberLogins(false)
	loginsMu.Lock()
	active := map[string]string{}
	for _, a := range loginAgents {
		if l, ok := liveLogin(a); ok {
			active[a] = l.User
		}
	}
	ls := readLogins()
	loginsMu.Unlock()
	out := []Renewal{}
	for _, l := range ls {
		if l.Agent != "codex" || strings.EqualFold(active[l.Agent], l.User) || accountRemoved(l.Agent) {
			continue
		}
		r := Renewal{Agent: l.Agent, User: l.User}
		if every > 0 && !renewalDue(l, every, time.Now()) {
			out = append(out, r)
			continue
		}
		if _, _, err := renewSavedLogin(ctx, l.Agent, l.User, true); err != nil {
			r.Err = err.Error()
			var refused refreshRefused
			r.Lapsed = errors.As(err, &refused)
		} else {
			r.Renewed = true
		}
		out = append(out, r)
	}
	return out
}

// renewalDue says whether a saved sign-in has gone every without being
// renewed — since magpie last renewed it, or since it was last seen in the
// agent's hands, which kept it fresh — or its refresh token is near its end.
func renewalDue(l savedLogin, every time.Duration, now time.Time) bool {
	last := l.Renewed
	if l.Seen.After(last) {
		last = l.Seen
	}
	var a struct {
		LastRefresh time.Time `json:"last_refresh"`
	}
	if json.Unmarshal(l.Auth, &a) == nil && a.LastRefresh.After(last) {
		last = a.LastRefresh
	}
	return now.Sub(last) >= every
}

// KeepLoginsAlive renews the saved sign-ins that are due, a minute after it
// starts and every hour after that — a computer that slept through a day
// catches up within the hour of waking — until ctx ends.
func KeepLoginsAlive(ctx context.Context) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		for _, r := range RenewLogins(c, keepAliveEvery) {
			if r.Err != "" {
				log.Printf("keeping %s account %s signed in: %s", r.Agent, r.User, r.Err)
			}
		}
		cancel()
		t.Reset(time.Hour)
	}
}

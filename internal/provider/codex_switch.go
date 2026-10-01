package provider

// Signing Codex or Claude Code in to the next of its accounts when the one
// it is on has used its allowance up. Codex's requests on its own models go
// through magpie while more of its accounts are on there, and move on to
// the next account when one is out — but the Codex app, once it knows the
// account it is signed in to is out, may not send at all, so no request
// reaches magpie to move on. Claude Code on its own models never comes
// through magpie: it asks Anthropic as the account it is signed in to, and
// that account being out ends its turns with "You've hit your session
// limit" while the gateway has long moved on to another (#208). So
// whichever magpie runs the gateway signs the agent in to the next account
// that is on and has room, as switching it on the Accounts page would: a
// session started after that is on it from the first turn.
//
// It moves at the share Smart routing counts an account spent at, so the
// account the agent is signed in to and the one the gateway goes to agree
// on which is out (#209): the sign-in follows Smart; Smart doesn't follow
// the sign-in.
//
// The account it moved from is the one the user made first, and it stays
// that: once it is no longer low (backShare) the agent is signed back in
// to it, as Smart gives it requests again then (#408). A switch the user
// makes meanwhile ends that.

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// loginSwitchEvery is how often the account each agent is on is looked at.
const loginSwitchEvery = 5 * time.Minute

// SpentShare is the share of an allowance past which an account is all
// but used up: Smart routing keeps it for when no other can take a
// request, and the agent signed in to it is signed in to another.
const SpentShare = 98

// backShare is the share below which the account magpie moved the agent
// off is signed back in to: no longer low, as Smart counts it (the
// gateway's lowShare), so it doesn't go back and forth at the edge.
const backShare = 90

// switchedAgents are the agents whose account magpie moves on.
var switchedAgents = []string{"codex", "claude"}

// usedUp reports whether an account's allowance is used up for now: a
// window that stops the account, for every model, at 100%.
func usedUp(q SubscriptionQuota) bool {
	return usedPast(q, 100)
}

// spent reports whether an account's allowance is all but used up, as
// Smart routing counts it: a window for every model at SpentShare.
func spent(q SubscriptionQuota) bool {
	return usedPast(q, SpentShare)
}

func usedPast(q SubscriptionQuota, share float64) bool {
	for _, w := range q.Windows {
		if !w.Aside && w.Model == "" && w.Used >= share {
			return true
		}
	}
	return false
}

// NextLogin is the account agent should be signed in to instead of the
// one it is on: the account magpie moved it off, once that has room again
// (back); else, when the one it is on is spent, the first of its other
// accounts that are on in magpie, in the order they were saved, whose
// allowance is known and not spent. ok is false when the agent should stay.
func NextLogin(ctx context.Context, agent string) (from, to string, back, ok bool) {
	if accountRemoved(agent) {
		return "", "", false, false // magpie has no say in its sign-in
	}
	var spares []Login
	var first *Login
	ls := Logins(agent)
	for i, l := range ls {
		switch {
		case l.Active:
			from = l.User
		case l.Returns:
			first = &ls[i]
		}
		if !l.Active && l.On && l.Lapsed == "" {
			spares = append(spares, l)
		}
	}
	if from == "" || len(spares) == 0 {
		return "", "", false, false
	}
	u := LoginUsage(ctx, agent)
	if first != nil && first.On && first.Lapsed == "" {
		if q, known := u[first.User]; known && q.Error == "" && !usedPast(q, backShare) {
			return from, first.User, true, true
		}
	}
	if q, known := u[from]; !known || q.Error != "" || !spent(q) {
		return "", "", false, false
	}
	for _, l := range spares {
		if q, known := u[l.User]; known && q.Error == "" && !spent(q) {
			return from, l.User, false, true
		}
	}
	return "", "", false, false
}

// SwitchWhenSpent signs agent in to the next of its accounts when the one
// it is on is spent, or back to the one it moved off once that has room
// (NextLogin), and answers the account it signed it in to, "" when it
// stayed.
func SwitchWhenSpent(ctx context.Context, agent string) (string, error) {
	from, to, back, ok := NextLogin(ctx, agent)
	if !ok {
		return "", nil
	}
	// the first moved off, not one moved to on the way
	r, _ := loginReturnOf(agent, from)
	if err := switchLogin(agent, to); err != nil {
		return "", err
	}
	if back {
		setLoginReturn(agent, loginReturn{})
		log.Printf("%s: %s has room again; signed it back in to it", agent, to)
	} else {
		if r.Back == "" {
			r.Back = from
		}
		r.To = to
		setLoginReturn(agent, r)
		log.Printf("%s: %s has used %d%% or more of its allowance; signed it in to %s", agent, from, SpentShare, to)
	}
	// the models the agent is offered are the new account's plan's
	catalog.Touched()
	return to, nil
}

// loginReturn is the account magpie signed an agent out of when it was
// spent (Back), and the one it signed it in to (To): while the agent is
// still on To, Back is signed in again once it has room.
type loginReturn struct {
	Back string `json:"back"`
	To   string `json:"to"`
}

var loginReturnsMu sync.Mutex

func loginReturnsPath() string { return filepath.Join(filepath.Dir(Path()), "login-returns.json") }

func readLoginReturns() map[string]loginReturn {
	m := map[string]loginReturn{}
	if b, err := os.ReadFile(loginReturnsPath()); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// loginReturnOf is the account to sign agent back in to, when it is still
// on the one magpie moved it to (active): one signed in otherwise since,
// by the user or the agent itself, is the user's choice.
func loginReturnOf(agent, active string) (loginReturn, bool) {
	loginReturnsMu.Lock()
	defer loginReturnsMu.Unlock()
	r, ok := readLoginReturns()[agent]
	if !ok || r.Back == "" || !strings.EqualFold(r.To, active) || strings.EqualFold(r.Back, active) {
		return loginReturn{}, false
	}
	return r, true
}

// setLoginReturn keeps r for agent; an empty one forgets it.
func setLoginReturn(agent string, r loginReturn) {
	loginReturnsMu.Lock()
	defer loginReturnsMu.Unlock()
	m := readLoginReturns()
	if _, had := m[agent]; !had && r.Back == "" {
		return
	}
	if r.Back == "" {
		delete(m, agent)
	} else {
		m[agent] = r
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err == nil {
		err = writePrivate(loginReturnsPath(), append(b, '\n'))
	}
	if err != nil {
		log.Printf("%s: keeping the account to go back to: %v", agent, err)
	}
}

// KeepOnAnAccountWithRoom runs SwitchWhenSpent for Codex and Claude Code a
// minute after it starts and every loginSwitchEvery after that, until ctx
// ends.
func KeepOnAnAccountWithRoom(ctx context.Context) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, agent := range switchedAgents {
			c, cancel := context.WithTimeout(ctx, time.Minute)
			if _, err := SwitchWhenSpent(c, agent); err != nil {
				log.Printf("%s: switching to an account with room: %v", agent, err)
			}
			cancel()
		}
		t.Reset(loginSwitchEvery)
	}
}

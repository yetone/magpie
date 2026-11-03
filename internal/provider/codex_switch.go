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
// the sign-in. In order, the gateway goes to the first until it is used
// up or refused, so the sign-in moves only once it is used up (#530).
//
// The account it moved from is the one the user made first, and where the
// sign-in follows the gateway it stays that: once it is no longer low
// (backShare) the agent is signed back in to it, as Smart gives it
// requests again then (#408). Where the sign-in alone spends the accounts
// (returnsToFirst), the agent stays on the one it moved to till that is
// spent too. A switch the user makes meanwhile ends that. With KeepLogin on the subscription, the agent
// stays signed in to the first, whatever it has left (#524); with
// KeepLoginAs, to that account, wherever it stands in the order. Not while
// magpie only switches the sign-in (SwitchOnly): moving it is then all
// magpie is asked to do, and kept on one account nothing would ever move
// (#1907, keepsLogin).

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/lastgood"
)

// loginSwitchEvery is how often the account each agent is on is looked at.
const loginSwitchEvery = 5 * time.Minute

// codexNearOutEvery is how often the account Codex is signed in to is read
// while it is near out (codexNearOut) or out: the Codex app greys its Send
// from its own reading, and magpie takes the turns over only once its
// reading says the account is out too, so five minutes apart left Send
// grey for up to five (Ethereal499 on X). Read again as soon after the
// reset, magpie gives the app its ChatGPT side back as soon.
const codexNearOutEvery = time.Minute

// codexNearShare is the share of a window past which the account counts
// near out.
const codexNearShare = 90

// codexLookEvery is when KeepOnAnAccountWithRoom looks again, after
// reading u: codexNearOutEvery while the account Codex is signed in to is
// near out or out, else loginSwitchEvery.
func codexLookEvery(u map[string]SubscriptionQuota) time.Duration {
	for _, l := range Logins("codex") {
		q, ok := u[l.User]
		if !l.Active || !ok || q.Error != "" {
			continue
		}
		if q.Held {
			return codexNearOutEvery
		}
		for _, w := range q.Windows {
			if !w.Unlimited && !w.Text && w.Used >= codexNearShare {
				return codexNearOutEvery
			}
		}
	}
	return loginSwitchEvery
}

// CodexOutUntil is when the ChatGPT account Codex is signed in to has room
// again, as last read: the latest reset of the windows used up. Zero when
// it isn't out or the reading doesn't say.
func CodexOutUntil() time.Time { return codexOutUntil(knownLoginUsage("codex")) }

// codexOutUntil is CodexOutUntil over the readings u.
func codexOutUntil(u map[string]SubscriptionQuota) time.Time {
	var until time.Time
	for _, l := range Logins("codex") {
		q, ok := u[l.User]
		if !l.Active || !ok || q.Error != "" || !q.Held {
			continue
		}
		for _, w := range q.Windows {
			if w.Unlimited || w.Text || w.Used < 100 || w.ResetsAt == nil {
				continue
			}
			if w.ResetsAt.After(until) {
				until = *w.ResetsAt
			}
		}
	}
	return until
}

// SpentShare is the share of an allowance past which an account is all
// but used up: Smart routing keeps it for when no other can take a
// request, and the agent signed in to it is signed in to another.
const SpentShare = 98

// SpentShareOf is the share past which routing counts an account spent:
// SpentShare, but In order sends requests to the first until a window of it
// is used up or the vendor refuses it, so there an account at 98% is still
// tried in its turn (#530); Until used up stays on one until then too.
func SpentShareOf(routing string) float64 {
	if routing == Ordered || routing == Drain {
		return 100
	}
	return SpentShare
}

// loginSwitching is how magpie moves agent's sign-in, as its subscription
// says: the share the account it is on is moved off at, and whether it is
// kept on the first instead (keepsLogin).
func loginSwitching(agent string) (share float64, keep bool) {
	for _, p := range storedFile().Providers {
		if p.ID == agent {
			return SpentShareOf(p.Routing), p.keepsLogin()
		}
	}
	return SpentShare, false
}

// returnsToFirst says whether magpie signs agent back in to the account
// it moved it off once that has room again (#408), rather than leaving it
// on the one it moved it to till that is spent in turn. The sign-in goes
// back where it follows the gateway: Codex's requests go through magpie,
// whose Smart and In order give the first account requests again once it
// has room (#209), and In order on any subscription is the user's word
// that the first comes first. Where the sign-in is what spends the
// accounts — Claude Code asks Anthropic itself, and Codex whose sign-in
// alone magpie switches (SwitchOnly) — going back at the first's reset
// left the account moved to part used and started the first's next
// window early, which is not "use one up, then the next" (hxy920830 on
// X): there it stays, unless the user said the first comes first.
func returnsToFirst(agent string) bool {
	for _, p := range storedFile().Providers {
		if p.ID == agent {
			// kept on the first (KeepLogin), set aside under SwitchOnly,
			// is still the user's word that the first comes first
			return p.Routing == Ordered || p.KeepLogin || agent == "codex" && !p.SwitchOnly
		}
	}
	return agent == "codex"
}

// backShare is the share below which the account magpie moved the agent
// off is signed back in to: no longer low, as Smart counts it (the
// gateway's lowShare), so it doesn't go back and forth at the edge.
const backShare = 90

// switchedAgents are the agents whose account magpie moves on.
var switchedAgents = []string{"codex", "claude"}

// usedUp reports whether an account's allowance is used up at now: a
// window that stops the account, for every model, at 100%.
func usedUp(q SubscriptionQuota, now time.Time) bool {
	return usedPast(q, 100, now)
}

// UsedUp is usedUp for the CLI's magpie quota wait, so what it waits out
// is what stops an account here.
func UsedUp(q SubscriptionQuota, now time.Time) bool { return usedUp(q, now) }

// BackAt is when a used-up account has room again: when the last of the
// windows that stop it (usedUp's) starts again, a window's seconds to go
// counted from now. Zero when one of them doesn't say.
func BackAt(q SubscriptionQuota, now time.Time) time.Time {
	var back time.Time
	for _, w := range q.Windows {
		if w.Aside || w.Model != "" || w.Used < 100 || resetPassed(w, now) {
			continue
		}
		var at time.Time
		switch {
		case w.ResetsAt != nil:
			at = *w.ResetsAt
		case w.ResetSecs > 0:
			at = now.Add(time.Duration(w.ResetSecs) * time.Second)
		default:
			return time.Time{}
		}
		if at.After(back) {
			back = at
		}
	}
	return back
}

// resetPassed says whether w has started again by now: its reset time has
// passed, whatever share it was read at. One that gives no reset time
// can't say.
func resetPassed(w QuotaWindow, now time.Time) bool {
	return w.ResetsAt != nil && !now.Before(*w.ResetsAt)
}

// usedPast reports whether one of q's windows that stop the account for
// every model is at share or past at now. A window whose reset has passed
// has started again, whatever share it was read at: a reading kept from
// before, or one the vendor gave late (Claude Code's /usage tells a reset
// to the minute), says nothing of now.
func usedPast(q SubscriptionQuota, share float64, now time.Time) bool {
	for _, w := range q.Windows {
		if !w.Aside && w.Model == "" && w.Used >= share && !resetPassed(w, now) {
			return true
		}
	}
	return false
}

// NextLogin is the account agent should be signed in to instead of the
// one it is on: the account magpie moved it off, once that has room again
// (back); else, when the one it is on is spent, the first of its other
// accounts that are on in magpie, in the order they were saved, whose
// allowance is known and not spent, or, when every one is past its line
// too, the one with the most room left (roomLeft). A saved Claude account
// not read yet is read then (readSpare). ok is false when the agent should
// stay.
func NextLogin(ctx context.Context, agent string) (from, to string, back, ok bool) {
	if leftAlone(agent) {
		return "", "", false, false // magpie has no say in its sign-in, or was told to leave it be
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
		// never one Codex keeps signed in in another folder (switchSavedLogin)
		// nor one added by a setup-token, which Claude Code can't be
		// signed in to
		if !l.Active && l.On && l.Lapsed == "" && l.home == "" && !l.Token {
			spares = append(spares, l)
		}
	}
	// kept on an account of the user's choosing, the agent goes back to
	// it whenever it is on another, and never moves off it (#524)
	if as := keptAs(agent); as != "" {
		i := slices.IndexFunc(ls, func(l Login) bool { return strings.EqualFold(l.User, as) })
		if from == "" || strings.EqualFold(from, as) || i < 0 || ls[i].Lapsed != "" {
			return "", "", false, false
		}
		return from, ls[i].User, true, true
	}
	if from == "" || len(spares) == 0 {
		return "", "", false, false
	}
	// kept on the first, the agent goes back to it at once, however little
	// that has left (#524)
	share, keep := loginSwitching(agent)
	if keep && first != nil && first.On && first.Lapsed == "" {
		return from, first.User, true, true
	}
	// the account it is on, and the one it goes back to, are read; the
	// spares only once the one it is on is spent (below): every account
	// read at once on each look was a burst no official client makes
	var firstUser string
	if first != nil && returnsToFirst(agent) {
		firstUser = first.User
	}
	u := someLoginUsage(ctx, agent, from, firstUser)
	now := time.Now()
	// an account at its usage cap is spent as one at the share below
	// (account_caps.go): it is moved off, and none is moved to or back to
	// till the window it filled renews
	capped := func(user string, q SubscriptionQuota) bool {
		return capReached(q, AccountCapsOf(agent, user), now)
	}
	// an account the Codex app holds (codexHeld: not allowed, no credits
	// to go on with) is spent whatever its windows read: the app sends
	// nothing for it, and a workspace seat at its owner's limit or a spend
	// cap is held with every window under the share
	if first != nil && first.On && first.Lapsed == "" && returnsToFirst(agent) {
		if q, known := u[first.User]; known && q.Error == "" && !q.Held && !usedPast(q, backShare, now) && !capped(first.User, q) {
			return from, first.User, true, true
		}
	}
	// kept on the first, it stays there however little that has left; and
	// it moves on at the share its routing counts the account spent at.
	// A window whose reset has passed counts as started again (usedPast),
	// on every account, however its reading came
	q, known := u[from]
	if keep || !known || q.Error != "" || !q.Held && !usedPast(q, share, now) && !capped(from, q) {
		return "", "", false, false
	}
	if agent == "codex" && !q.Held && !capped(from, q) && spendsFirst(from, q) {
		// set to spend its credits before another account, and holding
		// some: Codex stays on it (jojonas_xyz on X)
		return "", "", false, false
	}
	users := make([]string, 0, len(spares))
	for _, l := range spares {
		users = append(users, l.User)
	}
	for user, q := range someLoginUsage(ctx, agent, users...) {
		u[user] = q
	}
	// every spare past its line too: the one with the most room left, when
	// it has more than the one it is on by roomMargin, so that the agent
	// isn't moved to and fro between two all but even; else it stays
	best, most := "", math.Inf(-1)
	if !q.Held {
		most = roomLeft(q, share, AccountCapsOf(agent, from), now) + roomMargin
	}
	for _, l := range spares {
		q, known := u[l.User]
		if agent == "claude" && (!known || unreadSpare(q)) {
			// a saved Claude account magpie has no reading of: read now,
			// by Claude Code's /usage in its own directory, one account at
			// a time and only till one has room
			q, known = readSpare(ctx, l)
		}
		if !known || q.Error != "" || q.Held {
			continue
		}
		if !usedPast(q, share, now) && !capped(l.User, q) {
			return from, l.User, false, true
		}
		if r := roomLeft(q, share, AccountCapsOf(agent, l.User), now); r > most {
			best, most = l.User, r
		}
	}
	if best != "" {
		return from, best, false, true
	}
	return "", "", false, false
}

// spendsFirst says whether the Codex account user, read as q, is set to
// spend its credits before another account (CodexCreditsFirst) and holds
// some, as its windows say (QuotaWindow.Credits).
func spendsFirst(user string, q SubscriptionQuota) bool {
	return CodexCreditsFirst(user) && slices.ContainsFunc(q.Windows, func(w QuotaWindow) bool { return w.Credits })
}

// roomMargin is how much more room (roomLeft) an account past its line
// needs than the one the agent is on, past its line too, to be moved to.
const roomMargin = 1

// roomLeft is how far the fullest of q's windows that stop the account for
// every model is below the line it is moved off at: its own usage cap
// (account_caps.go) when that is lower, else share. Below zero when it is
// past the line; a window renewed since it was read, or none, leaves the
// whole line.
func roomLeft(q SubscriptionQuota, share float64, caps WindowCaps, now time.Time) float64 {
	room := share
	for _, w := range q.Windows {
		if w.Aside || w.Model != "" || w.Unlimited || resetPassed(w, now) {
			continue
		}
		line := share
		if c, _ := caps.Of(w.Name); c > 0 && float64(c) < line {
			line = float64(c)
		}
		room = min(room, line-w.Used)
	}
	return room
}

// NextInOrder is the account `magpie accounts switch <agent> next` signs
// agent in to: of its other accounts the agent can be signed in to, taken
// in their order from the one after the account it is on, the first whose
// allowance is known and has room; when none has, the one with the most
// room left (roomLeft), and over says so. A saved Claude account not read
// yet is read then, as NextLogin's spares are.
//
// An account off in magpie is taken too, as switching to it by name or
// from the accounts list is: on/off is whether the gateway's requests go
// to it, not whether the agent may be signed in to it. An account the
// agent signed itself in to (Claude Code's /login, codex login) starts
// off, and next passed over it (StringKe, #4532). The automatic switch
// (NextLogin) still takes only accounts that are on.
func NextInOrder(ctx context.Context, agent string) (to string, over bool, err error) {
	if !slices.Contains(loginAgents, agent) {
		return "", false, fmt.Errorf("next switches Claude Code's and Codex's accounts; name the %s account to switch to", agent)
	}
	ls := Logins(agent)
	at := slices.IndexFunc(ls, func(l Login) bool { return l.Active })
	var order []Login
	for i := range ls {
		l := ls[(at+1+i)%len(ls)]
		if !l.Active && l.Lapsed == "" && switchable(l) {
			order = append(order, l)
		}
	}
	if len(order) == 0 {
		return "", false, fmt.Errorf("%s has no other account in magpie to switch to", agent)
	}
	users := make([]string, 0, len(order))
	for _, l := range order {
		users = append(users, l.User)
	}
	share, _ := loginSwitching(agent)
	u := someLoginUsage(ctx, agent, users...)
	now := time.Now()
	best, most := "", math.Inf(-1)
	for _, l := range order {
		q, known := u[l.User]
		if agent == "claude" && (!known || unreadSpare(q)) {
			q, known = readSpare(ctx, l)
		}
		if !known || q.Error != "" || q.Held {
			continue
		}
		caps := AccountCapsOf(agent, l.User)
		if !usedPast(q, share, now) && !capReached(q, caps, now) {
			return l.User, false, nil
		}
		if r := roomLeft(q, share, caps, now); r > most {
			best, most = l.User, r
		}
	}
	if best == "" {
		return "", false, fmt.Errorf("magpie has no reading of %s's other accounts' usage: name the one to switch to (magpie accounts switch %s <email>)", agent, agent)
	}
	return best, true, nil
}

// unreadSpare says q, a saved Claude account's reading, has nothing to go
// by: never read (a /usage not run), or a read that failed for a reason
// not the account's own. Its /usage is then run (readSpare).
func unreadSpare(q SubscriptionQuota) bool {
	return q.Error != "" && (readDeferred(q.Error) || !claudeUsageDenied.MatchString(q.Error))
}

// readSpare is the reading of the saved Claude account l, its /usage run
// now when claudeWindows allows (spareRead: once, then again only after a
// wait), rather than the minute's cached "not read yet".
func readSpare(ctx context.Context, l Login) (SubscriptionQuota, bool) {
	forgetLoginReading(l)
	q, known := someLoginUsage(readingSpare(ctx), l.Agent, l.User)[l.User]
	return q, known
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
	if err := switchLoginBy(agent, to, true); err != nil {
		return "", err
	}
	if back {
		setLoginReturn(agent, loginReturn{})
		if keptAs(agent) != "" {
			log.Printf("%s: kept signed in to %s; signed it back in to it from %s", agent, to, from)
		} else {
			log.Printf("%s: %s has room again; signed it back in to it", agent, to)
		}
	} else {
		if r.Back == "" {
			r.Back = from
		}
		r.To = to
		setLoginReturn(agent, r)
		share, _ := loginSwitching(agent)
		log.Printf("%s: %s has used %g%% or more of its allowance; signed it in to %s", agent, from, share, to)
		if agent == "codex" {
			codexAppSwitched(true, true)
		}
	}
	if agent == "codex" {
		if was := CodexAppStale(); was != "" {
			// a switch off a spent account restarts it either way: left on
			// that account, the app greys Send for every model
			if codexRestartsApp() || !back {
				log.Printf("codex: the Codex app is still signed in to %s, and shows its limits, until its app-server is restarted once no Codex turn runs", was)
			} else {
				log.Printf("codex: the Codex app is still signed in to %s, and shows its limits, until it is restarted: magpie restarts it for a switch back only with \"Restart the Codex app to follow\" on", was)
			}
		}
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
		// readLoginReturns reads a file that doesn't parse as none: it is
		// kept aside before it is written over
		err = lastgood.KeepUnparsed(loginReturnsPath(), lastgood.Into[map[string]loginReturn])
	}
	if err == nil {
		err = writePrivate(loginReturnsPath(), append(b, '\n'))
	}
	if err != nil {
		log.Printf("%s: keeping the account to go back to: %v", agent, err)
	}
}

// codexWasUsedUp is whether the account Codex is signed in to was out
// (CodexUsedUp) at the last look.
var codexWasUsedUp atomic.Bool

// noteCodexUsedUp tells the agents' files when the account Codex is signed
// in to runs out (CodexUsedUp: of its allowance and its credits), or has
// room again: the Codex app sends nothing at all for an account that is
// out, a magpie model's turn included, so the agent package then makes
// magpie Codex's provider, and puts it back beside the sign-in after
// (#540). Only a change is told.
func noteCodexUsedUp(now bool) {
	if codexWasUsedUp.Swap(now) != now {
		catalog.Touched()
	}
}

// KeepOnAnAccountWithRoom runs SwitchWhenSpent for Codex and Claude Code a
// minute after it starts and every loginSwitchEvery after that (Codex's
// codexLookEvery), until ctx ends, and keeps Claude Code's proxy that of
// its account (SyncClaudeProxyEnv). Between those looks Claude's sign-in is
// looked at every claudeLookEvery when claudeLookDue: Claude Code asks
// Anthropic itself, and an account burnt fast was out minutes before the
// next look (#4443).
func KeepOnAnAccountWithRoom(ctx context.Context) {
	t := time.NewTimer(firstSignInLook)
	defer t.Stop()
	live := false
	defer func() {
		if live {
			codexLiveChecks.Add(-1)
		}
	}()
	var full time.Time // when the next full look is due; zero: now
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// a reply's word that the account is out is read at once only
		// while this loop, which reads the vendor itself, has started
		// reading and still runs: a test's short-lived gateway never does
		if !live {
			live = true
			codexLiveChecks.Add(1)
		}
		claudeDue := claudeLookDue()
		if time.Now().Before(full) {
			if claudeDue {
				lookAtSignIn(ctx, "claude")
			}
			signInsLooked(false)
			t.Reset(min(time.Until(full), claudeLookEvery))
			continue
		}
		for _, agent := range switchedAgents {
			lookAtSignIn(ctx, agent)
		}
		// a sign-in made in Claude Code itself, or a proxy picked since,
		// takes its account's proxy too (#1807)
		syncClaudeProxyEnvNow()
		c, cancel := context.WithTimeout(ctx, time.Minute)
		noteCodexUsedUp(CodexUsedUp(c))
		cancel()
		// Codex's own sign-in refused for good, or signed in again since
		noteCodexGoneNow()
		every := codexLookEvery(knownLoginUsage("codex"))
		full = time.Now().Add(every)
		signInsLooked(true)
		t.Reset(min(every, claudeLookEvery))
	}
}

// firstSignInLook is when KeepOnAnAccountWithRoom first looks. A var so
// tests can say.
var firstSignInLook = time.Minute

// signInsLooked is told after each of KeepOnAnAccountWithRoom's looks,
// whether it was a full one. A var so tests can wait on it.
var signInsLooked = func(full bool) {}

// lookAtSignIn is one look of KeepOnAnAccountWithRoom at agent's sign-in.
func lookAtSignIn(ctx context.Context, agent string) {
	c, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if _, err := SwitchWhenSpent(c, agent); err != nil {
		log.Printf("%s: switching to an account with room: %v", agent, err)
	}
}

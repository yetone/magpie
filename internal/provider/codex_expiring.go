package provider

// A Codex rate-limit reset about to run out unused is spent by itself
// (#624), for the accounts the user lets spend their resets, if the
// account's windows have been used at all — a reset that would start
// nothing again is left to run out, nothing lost. Spending one starts the
// windows again, and what they have left then is lost, while the fresh
// windows are the same whenever it is spent: so it is spent as late as is
// safe, resetLead before it runs out: half an hour unless the user set
// another (settings' CodexResetLead). Earlier loses what the windows have
// left then (#718: spent three hours early, an account held up for the
// five hours but freed again in two lost the 68% of its week it could
// have used before the reset ran out).
// Only when the account is held up now and won't be free again before
// then is it spent at once: nothing more can be used by waiting, and
// spending it frees the account. It is the reset that runs out first that
// is spent (UseCodexReset), so one that lasts longer is kept. This is
// apart from the week's one when the week is used up (AutoUseCodexReset):
// a reset that would be lost anyway takes none of the week's.

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// resetLead is how long before a reset runs out it is spent: the user's
// CodexResetLead, half an hour by default — as late as is safe, with room
// for a few looks (resetExpiryClose) missed or failed. A value that can't
// be read is the default, never none. Both the spend (expiringResetSpent)
// and routing (Allowance.Restarts) read it here, so they can't drift
// apart (#717, #718). Shared: routing reads it on every request.
func resetLead() time.Duration {
	return settings.Shared().ResetLead()
}

// resetExpiryNear is how long before a reset runs out its account is
// looked at every resetExpiryClose, from then until it is spent: the lead
// and half an hour more, an hour at least, so several looks fall inside
// the lead however long it is.
func resetExpiryNear(lead time.Duration) time.Duration {
	return max(time.Hour, lead+30*time.Minute)
}

var (
	// resetExpiryClose is how often an account is looked at near a reset
	// running out, after one is spent, and after a failed read or spend
	// then; and how often the accounts are gone over (each is read only
	// when its next look is due)
	resetExpiryClose = 5 * time.Minute
	// resetExpiryWatch is how often an account holding a reset that runs
	// out, not yet near, is looked at: it may come to be held up past it
	// (spent at once then), or be used
	resetExpiryWatch = 30 * time.Minute
	// resetExpiryFar is the longest an account goes unread: a reset
	// granted meanwhile is seen by then
	resetExpiryFar = 6 * time.Hour
	// resetExpiryRetry is how soon a read or spend that failed within the
	// lead is tried again once the reset runs out within resetExpiryLast:
	// a five-minute lead has room for several tries, not one
	resetExpiryRetry = time.Minute
	// resetExpiryLast is how close to running out a failure is tried again
	// every resetExpiryRetry, not resetExpiryClose: a long lead failing all
	// along isn't read every minute for hours
	resetExpiryLast = 15 * time.Minute
)

// errResetsUntilUnread: the account holds resets, but when they run out
// couldn't be read
var errResetsUntilUnread = errors.New("couldn't read when its resets run out")

// expiringResetDetailsWait is how long the background look waits for when
// the resets run out (withResetDetailsWait)
var expiringResetDetailsWait = 30 * time.Second

var expiring = expiringResets{next: map[string]time.Time{}, until: map[string]time.Time{}}

type expiringResets struct {
	sync.Mutex
	next  map[string]time.Time // by account, when to read it again
	until map[string]time.Time // by account, when its soonest reset runs out, as last read
	// pass is the accounts check was asked about since the loop last
	// woke (wake): the ones whose next look it waits for
	pass map[string]bool
}

// check spends one of user's resets, if its windows have been used, when
// the soonest of them runs out within resetLead or the account is
// held up past then (expiringResetSpent); look reads its windows and
// resets, spend spends the one that runs out first. Code is "" when none
// was tried. now is wall-clock time: a Mac asleep doesn't count it on the
// monotonic clock, and the reset runs out by the wall clock.
func (e *expiringResets) check(user string, now time.Time, look func() ([]QuotaWindow, *ResetCredits, error), spend func() (ResetOutcome, error)) (ResetOutcome, error) {
	key := strings.ToLower(user)
	now = now.Round(0)
	e.Lock()
	defer e.Unlock()
	if e.until == nil {
		e.until = map[string]time.Time{}
	}
	if e.pass == nil {
		e.pass = map[string]bool{}
	}
	e.pass[key] = true
	if now.Before(e.next[key]) {
		return ResetOutcome{}, nil
	}
	windows, resets, err := look()
	if err == nil && resets != nil && resets.Count > 0 && resets.untilUnread {
		// held, but when they run out wasn't read: unknown, not never —
		// taken for "none runs out", the account went unread for
		// resetExpiryFar and a reset running out within it ran out unspent
		err = errResetsUntilUnread
	}
	if err != nil {
		// tried again by the reset last read, soon when it is near
		e.next[key] = now.Add(resetExpiryWatch)
		if u, ok := e.until[key]; ok {
			e.next[key] = retryResetLook(u, now)
		}
		return ResetOutcome{}, err
	}
	if resets == nil || resets.Count <= 0 || resets.Until == nil {
		// none held, or none runs out
		delete(e.until, key)
		e.next[key] = now.Add(resetExpiryFar)
		return ResetOutcome{}, nil
	}
	until := resets.Until.Round(0)
	e.until[key] = until
	if !until.After(now) {
		// gone already: another may run out soon after it
		e.next[key] = now.Add(resetExpiryClose)
		return ResetOutcome{}, nil
	}
	q := SubscriptionQuota{Windows: windows}
	at := expiringResetSpent(until, now, usedUp(q, now), BackAt(q, now))
	if at.After(now) || !windowsUsed(windows) {
		// not yet, or nothing to start again yet: the account may be used,
		// or held up, before it runs out
		e.next[key] = nextResetLook(until, now)
		return ResetOutcome{}, nil
	}
	out, err := spend()
	if err != nil {
		e.next[key] = retryResetLook(until, now)
		return out, err
	}
	if out.Code != "reset" {
		e.next[key] = nextResetLook(until, now)
		return out, err
	}
	// another may run out soon after it
	e.next[key] = now.Add(resetExpiryClose)
	return out, nil
}

// nextResetLook is when an account holding a reset that runs out at until
// is looked at next: every resetExpiryClose within resetExpiryNear of
// that, every resetExpiryWatch before then but never past its start — so
// the reset is looked at several times within resetLead, a look missed
// or failed there still spends it in time. Never past when the lead
// starts either: the look lands on it, not up to resetExpiryClose after,
// which a lead as short as five minutes couldn't spare.
func nextResetLook(until, now time.Time) time.Time {
	lead := resetLead()
	if near := until.Add(-resetExpiryNear(lead)); near.After(now) {
		return minTime(now.Add(resetExpiryWatch), near)
	}
	next := now.Add(resetExpiryClose)
	if at := until.Add(-lead); at.After(now) {
		return minTime(next, at)
	}
	return next
}

// retryResetLook is when a read or spend that failed is tried again: every
// resetExpiryRetry once the reset runs out within resetExpiryLast and its
// lead has started, so a short lead gets several tries; nextResetLook
// otherwise.
func retryResetLook(until, now time.Time) time.Time {
	if !until.Add(-resetLead()).After(now) && until.Sub(now) <= resetExpiryLast {
		return now.Add(resetExpiryRetry)
	}
	return nextResetLook(until, now)
}

// wake is how long the loop (KeepResetsFromRunningOut) sleeps after a pass
// ending at now: until the soonest look due of the accounts the pass went
// over, resetExpiryClose at most, a second at least. A look due at a
// lead's start is made then, not at the next five-minute pass, and a retry
// due while the pass still ran (a try hung to its bound) is made at once.
// An account the pass skipped (auto-use turned off) waits for nothing.
func (e *expiringResets) wake(now time.Time) time.Duration {
	now = now.Round(0)
	e.Lock()
	defer e.Unlock()
	d := resetExpiryClose
	for key := range e.pass {
		d = min(d, e.next[key].Sub(now))
	}
	clear(e.pass)
	return max(d, time.Second)
}

// expiringResetSpent is when a reset that runs out at until is spent by
// itself (check, when the account's windows have been used by then):
// resetLead before it runs out, or now once that has passed; now
// too when the account is stopped (a window that stops it used up) and
// back, when it is free again (zero: not known), isn't before then — what
// the windows have left can't be used before the reset is spent anyway,
// and spending it frees the account. Zero when it has run out already.
// Routing goes by the same time (Allowance.Restarts; #717, #718), so the
// two can't drift apart.
func expiringResetSpent(until, now time.Time, stopped bool, back time.Time) time.Time {
	if !until.After(now) {
		return time.Time{}
	}
	at := until.Add(-resetLead())
	if !at.After(now) || stopped && (back.IsZero() || !back.Before(at)) {
		return now
	}
	return at
}

// resetRunsOut is when the reset an account will spend by itself before
// it runs out does (check), for routing, which takes it for when its
// windows start again by expiringResetSpent (Allowance.Restarts): what
// they have left is lost then, as at their own reset (#717, #718). Zero unless agent's
// account user is let spend the one about to run out (autoResetsExpiring), holds one that runs
// out, and has used its windows — as check, which spends none on windows
// with nothing to start again. Only holding a reset, auto-use off, is
// nothing: nobody spends it.
func resetRunsOut(agent, user string, windows []QuotaWindow, resets *ResetCredits) time.Time {
	if resets == nil || resets.Count <= 0 || resets.Until == nil || !windowsUsed(windows) || !autoResetsExpiring(agent, user) {
		return time.Time{}
	}
	return *resets.Until
}

// windowsUsed says whether any of windows, the on-demand ones aside, has
// been used: a reset starts those again.
func windowsUsed(windows []QuotaWindow) bool {
	for _, w := range windows {
		if !w.Aside && w.Used > 0 {
			return true
		}
	}
	return false
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// SpendExpiringCodexResets goes over the Codex accounts the user lets
// spend their resets and spends one about to run out unused (see check).
func SpendExpiringCodexResets(ctx context.Context) {
	if leftAlone("codex") {
		return // switched off or removed: its allowance isn't read nor a reset spent (#2226)
	}
	for _, user := range settings.Load().CodexAutoReset {
		if ctx.Err() != nil {
			return
		}
		if _, expiring := CodexAutoResetWhen(user); !expiring {
			continue // kept to its week used up (#2078)
		}
		ctx := withResetDetailsWait(ctx, expiringResetDetailsWait)
		var who string
		out, err := expiring.check(user, time.Now(), func() ([]QuotaWindow, *ResetCredits, error) {
			var windows []QuotaWindow
			var resets *ResetCredits
			var err error
			who, windows, resets, err = codexUserWindows(ctx, user)
			return windows, resets, err
		}, func() (ResetOutcome, error) {
			// one spend at a time, with the week's used-up one too
			autoReset.Lock()
			defer autoReset.Unlock()
			// read again under the lock: the week's used-up one may have
			// been spent since the look, and a held-up account freed by it
			// isn't one to spend another on
			if !stillExpiring(ctx, who) {
				return ResetOutcome{}, nil
			}
			return useCodexReset(ctx, who, ResetUsedExpiring)
		})
		switch {
		case err != nil:
			log.Printf("codex reset about to run out: %s: %v", user, err)
		case out.Code != "":
			log.Printf("codex reset about to run out: %s used it: %s", user, out.Text())
		}
	}
}

// stillExpiring reads who's windows and resets again and says whether
// check would still spend one now; a var so tests can stand in for it.
var stillExpiring = func(ctx context.Context, who string) bool {
	_, windows, resets, err := codexUserWindows(ctx, who)
	return err == nil && spendExpiringNow(windows, resets, time.Now())
}

// spendExpiringNow: the soonest of resets is due to be spent by now
// (expiringResetSpent) and windows have been used.
func spendExpiringNow(windows []QuotaWindow, resets *ResetCredits, now time.Time) bool {
	if resets == nil || resets.Count <= 0 || resets.Until == nil || !windowsUsed(windows) {
		return false
	}
	q := SubscriptionQuota{Windows: windows}
	at := expiringResetSpent(resets.Until.Round(0), now, usedUp(q, now), BackAt(q, now))
	return !at.IsZero() && !at.After(now)
}

// KeepResetsFromRunningOut spends the Codex resets about to run out unused
// (SpendExpiringCodexResets), three minutes after it starts and then as
// soon as a look is due (wake: a lead's start, a retry), every
// resetExpiryClose at most, until ctx ends.
func KeepResetsFromRunningOut(ctx context.Context) {
	t := time.NewTimer(3 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		SpendExpiringCodexResets(c)
		cancel()
		t.Reset(expiring.wake(time.Now()))
	}
}

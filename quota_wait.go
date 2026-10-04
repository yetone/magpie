package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// How often magpie quota wait reads the allowance again: shortly after the
// soonest reset it knows, but never sooner than a minute (the readings are
// kept that long anyway, and the vendors' usage endpoints are rate limited)
// nor later than ten, as a reset can come early (a Codex reset spent, a plan
// topped up) or not be told at all.
const (
	quotaWaitMin    = time.Minute
	quotaWaitMax    = 10 * time.Minute
	quotaWaitMargin = 30 * time.Second
)

// exitError is an error that ends magpie with its own exit code rather
// than 1; with no err, nothing is printed.
type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e exitError) Unwrap() error { return e.err }

// errWaitTimeout is quota wait's --timeout running out, exit code 1.
var errWaitTimeout = errors.New("timed out")

// quotaTarget is what magpie quota wait waits on: every account of a
// provider (user ""), or one account.
type quotaTarget struct {
	provider string
	user     string
	label    string
}

func (t quotaTarget) has(q provider.SubscriptionQuota) bool {
	return strings.EqualFold(q.Provider, t.provider) && (t.user == "" || strings.EqualFold(q.User, t.user))
}

// resolveQuotaTarget finds what arg names among the readings, as magpie
// quota matches a provider (its id or name), or one account by its user,
// or <provider>/<user> where two providers have the same user. find says
// whether arg is a provider at all, for an error that tells the two apart.
func resolveQuotaTarget(arg string, qs []provider.SubscriptionQuota, find func(string) bool) (quotaTarget, error) {
	byProvider := func(name string) (string, bool) {
		for _, q := range qs {
			if strings.EqualFold(q.Provider, name) || strings.EqualFold(q.Name, name) {
				return q.Provider, true
			}
		}
		return "", false
	}
	if id, ok := byProvider(arg); ok {
		return quotaTarget{provider: id, label: id}, nil
	}
	if p, u, ok := strings.Cut(arg, "/"); ok {
		if id, ok := byProvider(p); ok {
			t := quotaTarget{provider: id, user: u, label: id + " · " + u}
			if slices.ContainsFunc(qs, t.has) {
				return t, nil
			}
			return quotaTarget{}, fmt.Errorf("%s has no account %s · magpie quota %s lists its accounts", id, u, id)
		}
	}
	var hits []quotaTarget
	for _, q := range qs {
		if q.User != "" && strings.EqualFold(q.User, arg) && !slices.ContainsFunc(hits, func(t quotaTarget) bool { return t.provider == q.Provider }) {
			hits = append(hits, quotaTarget{provider: q.Provider, user: q.User, label: q.Provider + " · " + q.User})
		}
	}
	switch {
	case len(hits) == 1:
		return hits[0], nil
	case len(hits) > 1:
		var names []string
		for _, t := range hits {
			names = append(names, t.provider+"/"+t.user)
		}
		return quotaTarget{}, fmt.Errorf("%s is an account of more than one subscription: name one of %s", arg, strings.Join(names, ", "))
	}
	if find != nil && find(arg) {
		return quotaTarget{}, fmt.Errorf("%s has no subscription or plan whose allowance magpie reads: sign in to it first, or magpie quota lists what there is", arg)
	}
	return quotaTarget{}, fmt.Errorf("no subscription, plan or account %s · magpie quota lists them", arg)
}

// quotaWait is magpie quota wait's loop, with what it reads, the clock and
// how it sleeps given, so tests run it on a fake clock.
type quotaWait struct {
	// read is every account's allowance now; readable ones say Error "".
	read func(ctx context.Context) []provider.SubscriptionQuota
	// skip, when set, leaves out an account a provider's wait shouldn't
	// count: one switched off or signed out in magpie, whose room the agent
	// would never be moved to.
	skip  func(q provider.SubscriptionQuota) bool
	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error
	say   func(string)
}

// readable is a reading made now that says the account's windows: not an
// error, nor the last one kept in its place (AsOf), which may be hours old.
func readable(q provider.SubscriptionQuota) bool {
	return q.Error == "" && q.AsOf == nil && len(q.Windows) > 0
}

// run waits until an account of t is not used up, and returns it. The
// error is errWaitTimeout once timeout (when over 0) has passed, or ctx's
// once it is cancelled.
func (w quotaWait) run(ctx context.Context, t quotaTarget, timeout time.Duration) (provider.SubscriptionQuota, error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = w.now().Add(timeout)
	}
	backoff := quotaWaitMin
	for {
		qs := w.read(ctx)
		if err := ctx.Err(); err != nil {
			return provider.SubscriptionQuota{}, err
		}
		now := w.now()
		known, unread, why := 0, 0, ""
		var back time.Time
		backWho := ""
		for _, q := range qs {
			if !t.has(q) || (t.user == "" && w.skip != nil && w.skip(q)) {
				continue
			}
			if !readable(q) {
				unread++
				why = cmp.Or(why, q.Error)
				continue
			}
			if !provider.UsedUp(q) {
				return q, nil
			}
			known++
			// the soonest any account is back; one that doesn't say when
			// leaves it to the next look
			if at := provider.BackAt(q, now); !at.IsZero() && (back.IsZero() || at.Before(back)) {
				back, backWho = at, q.User
			}
		}
		var d time.Duration
		var line string
		switch {
		case known == 0:
			// nothing read: try again, less often each time, as a vendor
			// that is down or refusing isn't helped by asking every minute
			d, backoff = backoff, min(backoff*2, quotaWaitMax)
			line = fmt.Sprintf("%s: its allowance couldn't be read", t.label)
			if why != "" {
				line += " (" + why + ")"
			}
			line += fmt.Sprintf(" · trying again in %s", roundWait(d))
		case back.IsZero():
			backoff = quotaWaitMin
			d = quotaWaitMax
			line = fmt.Sprintf("%s is used up, and when it starts again isn't told · checking again in %s", t.label, roundWait(d))
		default:
			backoff = quotaWaitMin
			d = min(max(back.Sub(now)+quotaWaitMargin, quotaWaitMin), quotaWaitMax)
			who := t.label
			if t.user == "" && backWho != "" && known > 1 {
				who += " (" + backWho + " first)"
			} else if t.user == "" && known > 1 {
				who += " (every account)"
			}
			line = fmt.Sprintf("%s is used up until %s · checking again at %s", who, provider.ResetClock(back, now), now.Add(d).Local().Format("15:04"))
		}
		if known > 0 && unread > 0 {
			line += fmt.Sprintf(" · %d not read", unread)
		}
		if !deadline.IsZero() {
			if !now.Before(deadline) {
				return provider.SubscriptionQuota{}, errWaitTimeout
			}
			d = min(d, deadline.Sub(now))
		}
		if w.say != nil {
			w.say(line)
		}
		if err := w.sleep(ctx, d); err != nil {
			return provider.SubscriptionQuota{}, err
		}
	}
}

// roundWait is a wait in words, to the minute: "2m0s" reads as "2m".
func roundWait(d time.Duration) string {
	s := d.Round(time.Minute).String()
	s = strings.TrimSuffix(s, "0s")
	return strings.TrimSuffix(s, "h0m")
}

// sleepCtx sleeps d, or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// quotaWaitCmd: magpie quota wait <provider|account> [--timeout <d>]
// [--quiet] — blocks until the subscription (any of its accounts) or the
// account has allowance again, for a script to go on with the work it
// stopped (#720): until codex exec …; do magpie quota wait codex; done.
// Exits 0 then, 1 on --timeout, 2 on a name it doesn't know, 130 on Ctrl+C.
func quotaWaitCmd(args []string) error {
	var arg string
	var timeout time.Duration
	quiet := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "help" || a == "-h" || a == "--help":
			fmt.Println(quotaUsage)
			return nil
		case a == "--quiet" || a == "-q":
			quiet = true
		case a == "--timeout" || strings.HasPrefix(a, "--timeout="):
			v, ok := strings.CutPrefix(a, "--timeout=")
			if !ok {
				if i+1 >= len(args) {
					return exitError{2, fmt.Errorf("--timeout needs a duration, such as 30m or 6h")}
				}
				i++
				v = args[i]
			}
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return exitError{2, fmt.Errorf("--timeout is a duration such as 30m or 6h, not %q", v)}
			}
			timeout = d
		case strings.HasPrefix(a, "-"):
			return exitError{2, fmt.Errorf("unknown flag %s · %s", a, "magpie quota wait <provider|account> [--timeout <duration>] [--quiet]")}
		default:
			if arg != "" {
				return exitError{2, fmt.Errorf("one subscription or account at a time: %s or %s", arg, a)}
			}
			arg = a
		}
	}
	if arg == "" {
		return exitError{2, fmt.Errorf("wait for what? magpie quota wait <provider|account>, such as codex")}
	}
	ctx, stop := interruptContext()
	defer stop()
	claude := strings.HasPrefix(strings.ToLower(arg), "claude")
	read := func(ctx context.Context) []provider.SubscriptionQuota {
		// a new reading each time: the cached one is what the last look saw
		if claude {
			provider.AskClaudeUsage()
		} else {
			provider.AskUsage()
		}
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return provider.Allotted(rctx)
	}
	qs := read(ctx)
	if ctx.Err() != nil {
		return exitError{130, nil}
	}
	t, err := resolveQuotaTarget(arg, qs, func(s string) bool { _, err := provider.Find(s); return err == nil })
	if err != nil {
		return exitError{2, err}
	}
	claude = strings.EqualFold(t.provider, "claude")
	skip := func(q provider.SubscriptionQuota) bool {
		for _, l := range provider.Logins(q.Provider) {
			if q.User != "" && strings.EqualFold(l.User, q.User) {
				return !l.On || l.Lapsed != ""
			}
		}
		return false
	}
	if t.user == "" && !slices.ContainsFunc(qs, func(q provider.SubscriptionQuota) bool { return t.has(q) && !skip(q) }) {
		return exitError{2, fmt.Errorf("none of %s's accounts is on in magpie · turn one on, or name the account", t.provider)}
	}
	first := true
	w := quotaWait{
		read: func(ctx context.Context) []provider.SubscriptionQuota {
			if first { // what resolving read is new enough
				first = false
				return qs
			}
			return read(ctx)
		},
		skip:  skip,
		now:   time.Now,
		sleep: sleepCtx,
	}
	if !quiet {
		w.say = func(s string) { fmt.Fprintln(os.Stderr, muted.Render("…"), s) }
	}
	q, err := w.run(ctx, t, timeout)
	switch {
	case errors.Is(err, errWaitTimeout):
		return fmt.Errorf("%s still has no allowance after %s", t.label, timeout)
	case err != nil:
		return exitError{130, nil}
	}
	if !quiet {
		who := q.Provider
		if q.User != "" {
			who += " · " + q.User
		}
		fmt.Println(green.Render("✓"), who, "has allowance again")
	}
	return nil
}

package provider

// Warm up now (a Windows user on Discord): one account's warm-up sent at
// once from Settings › Warm-up and check-in, whatever the warm-up settings
// say. It is the request the scheduled warm-up sends — warmCodexLogin for
// a ChatGPT account, Claude Code's own run (WarmClaudeVia) for a Claude
// one — to that account alone, and what it says back is the result. The
// scheduled warm-up's file is left to the scheduled one: its next look
// reads the window as started, from its own read.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// warmNowWait is how long Warm up now waits on its request, as long as
// the scheduled warm-up gives one look.
const warmNowWait = 2 * time.Minute

// claudeWarmSend is a Claude warm-up's send, Claude Code asked with
// configDir the config directory of a saved account, "" for the one it is
// signed in to. The gateway, which runs Claude Code, sets it (WarmClaudeVia).
var claudeWarmSend func(ctx context.Context, configDir string) error

// WarmClaudeVia sets how a Claude account's warm-up runs Claude Code.
func WarmClaudeVia(f func(ctx context.Context, configDir string) error) { claudeWarmSend = f }

// WarmNow sends agent's ("codex" or "claude") account user the warm-up's
// one tiny request now, and says why it failed. Once it went, what the
// account has left is read again, as after a scheduled warm-up.
func WarmNow(ctx context.Context, agent, user string) error {
	var name string
	switch agent {
	case "codex":
		name = "ChatGPT"
	case "claude":
		name = "Claude"
	default:
		return fmt.Errorf("%q has no warm-up", agent)
	}
	ls := Logins(agent)
	i := slices.IndexFunc(ls, func(l Login) bool { return strings.EqualFold(l.User, user) })
	if user == "" || i < 0 {
		return fmt.Errorf("no %s account %q", name, user)
	}
	if ls[i].Lapsed != "" {
		// as the scheduled warm-up leaves it out (codexWarmUsage)
		return fmt.Errorf("%s needs signing in again: %s", ls[i].User, ls[i].Lapsed)
	}
	ctx, cancel := context.WithTimeout(ctx, warmNowWait)
	defer cancel()
	if agent == "codex" {
		if err := warmCodexLogin(ctx, ls[i].User); err != nil {
			return err
		}
		StaleAllowance("codex", ls[i].User)
		return nil
	}
	if claudeWarmSend == nil {
		return errors.New("Claude Code isn't available to send the warm-up")
	}
	if err := warmClaudeLogin(claudeWarmSend)(ctx, ls[i].User); err != nil {
		return err
	}
	claudeWarmedNow(ls[i].User)
	return nil
}

// WarmAllResult is what Warm up all came to for one account: Skip says why
// it was left alone ("off" or "running"), Err why its warm-up failed or
// couldn't be sent; both empty, it was warmed.
type WarmAllResult struct {
	User string `json:"user"`
	Skip string `json:"skip,omitempty"`
	Err  string `json:"error,omitempty"`
}

// warmAllAtOnce is how many of Warm up all's requests run together: each
// Claude one is a Claude Code run.
const warmAllAtOnce = 3

// WarmAll is Warm up now for each of agent's accounts at once
// (CoderMaurice on X: one press for several Claude accounts). It sends the
// warm-up to every account in use (the one the agent is on, or one turned
// on beside it) whose windows aren't all running already, as the cached
// reads the Usage page shares tell them. An account turned off or paused,
// and one already in its windows, are left alone and listed with why; one
// whose sign-in lapsed is sent nothing and failed, as Warm up now has it.
func WarmAll(ctx context.Context, agent string) ([]WarmAllResult, error) {
	name := map[string]string{"codex": "ChatGPT", "claude": "Claude"}[agent]
	if name == "" {
		return nil, fmt.Errorf("%q has no warm-up", agent)
	}
	ls := Logins(agent)
	if len(ls) == 0 {
		return nil, fmt.Errorf("no %s account to warm up", name)
	}
	usage := LoginUsage(ctx, agent)
	return warmAll(ctx, ls, usage, time.Now(), func(ctx context.Context, user string) error { return WarmNow(ctx, agent, user) }), nil
}

// warmAll is WarmAll on ls read as usage at now, each warm-up sent by send.
func warmAll(ctx context.Context, ls []Login, usage map[string]SubscriptionQuota, now time.Time, send func(context.Context, string) error) []WarmAllResult {
	var out []WarmAllResult
	var due []int
	for _, l := range ls {
		if l.User == "" || slices.ContainsFunc(out, func(r WarmAllResult) bool { return strings.EqualFold(r.User, l.User) }) {
			continue
		}
		r := WarmAllResult{User: l.User}
		switch {
		case l.Lapsed != "":
			// as Warm up now says of it
			r.Err = "needs signing in again: " + l.Lapsed
		case !l.Active && !l.On || l.Paused:
			r.Skip = "off"
		case windowsRunning(usage[l.User], now):
			r.Skip = "running"
		default:
			due = append(due, len(out))
		}
		out = append(out, r)
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, warmAllAtOnce)
	for _, i := range due {
		wg.Add(1)
		go func() {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			if err := send(ctx, out[i].User); err != nil {
				out[i].Err = err.Error()
			}
		}()
	}
	wg.Wait()
	return out
}

// windowsRunning says whether every one of q's own windows with a span is
// running as of now, so a warm-up would start none: false when none is
// known, as for an account never read.
func windowsRunning(q SubscriptionQuota, now time.Time) bool {
	n := 0
	for _, w := range q.Windows {
		if w.Span <= 0 || w.Aside || w.Text || w.Model != "" || w.Lane != "" {
			continue
		}
		if idle(asOf(w, now), now) {
			return false
		}
		n++
	}
	return n > 0
}

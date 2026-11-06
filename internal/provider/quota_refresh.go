package provider

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A card read again on its own, from its refresh button on the Usage page
// (Hu9956, #840: 只刷新单个套餐的用量): the provider's, and of the one
// account user when it has several ("" for every one). Only that card is
// asked for; the others stay as they were last read.
type cardRefresh struct{ provider, user string }

type cardRefreshKey struct{}

type quotaReadKey struct{}

var quotaReadSequence atomic.Uint64

// quotaReading orders reads when they start, not when the vendor answers.
// A card refresh shares its order across the three kinds of usage.
// A read the user didn't ask for, while they have allowances read only
// when asked, sends nothing (holdUnasked).
func quotaReading(ctx context.Context) (context.Context, uint64) {
	ctx = holdUnasked(ctx)
	if seq, ok := ctx.Value(quotaReadKey{}).(uint64); ok {
		return ctx, seq
	}
	seq := quotaReadSequence.Add(1)
	return context.WithValue(ctx, quotaReadKey{}, seq), seq
}

// keepReading also orders the last good reading on disk: a late response
// must not become the fallback for the next failed request.
func keepReading(ctx context.Context, q SubscriptionQuota, user string) SubscriptionQuota {
	q.readSeq, _ = ctx.Value(quotaReadKey{}).(uint64)
	return keepLast(q, user)
}

// refreshing is the card a read is for, when it is for one card alone.
func refreshing(ctx context.Context) (cardRefresh, bool) {
	r, ok := ctx.Value(cardRefreshKey{}).(cardRefresh)
	return r, ok
}

// wantsCard reports whether a read asks for the card of provider and
// account user: every card, unless it reads one again on its own.
func wantsCard(ctx context.Context, provider, user string) bool {
	r, ok := refreshing(ctx)
	return !ok || r.wants(provider, user)
}

func (r cardRefresh) wants(provider, user string) bool {
	return provider == r.provider && (r.user == "" || strings.EqualFold(user, r.user))
}

// refreshLogins is the accounts of ls a read asks for, each read afresh
// rather than from the minute's cache when it reads one card again.
func refreshLogins(ctx context.Context, ls []Login) []Login {
	r, ok := refreshing(ctx)
	if !ok {
		return ls
	}
	var out []Login
	for _, l := range ls {
		if r.user != "" && !strings.EqualFold(l.User, r.user) {
			continue
		}
		forgetLoginReading(l)
		out = append(out, l)
	}
	return out
}

// forgetLoginReading has l's next reading asked for, keeping what it was
// for a hiccup (as AskUsage does for every account).
func forgetLoginReading(l Login) {
	key := l.Agent + "/" + strings.ToLower(l.User)
	c := &loginUsageCache
	c.Lock()
	if e, ok := c.m[key]; ok {
		e.at = time.Time{}
		c.m[key] = e
	}
	c.Unlock()
}

// RefreshUsage reads the card of provider (of account user, "" for every
// account it has) again, whether a subscription's, a plan's or a key's
// balance, and keeps it with the others, which aren't asked. The next
// Quotas has it.
func RefreshUsage(ctx context.Context, provider, user string) {
	refreshCards(ctx, provider, user)
	// the menu bar shows the same card: it reads it again now (#2357)
	toldUsage()
}

// RefreshEach is RefreshUsage of each of cards, read side by side: a
// gateway key told some cards has every one of them read again on a
// Refresh of them all, and one after another, a saved Claude account's
// /usage about 5 seconds each, a few of them outlasted the gateway's 30
// seconds (#5443). Each read is bounded as RefreshUsage's is.
func RefreshEach(ctx context.Context, cards []SubscriptionQuota) {
	var wg sync.WaitGroup
	for _, q := range cards {
		wg.Add(1)
		go func() {
			defer wg.Done()
			refreshCards(ctx, q.Provider, q.User)
		}()
	}
	wg.Wait()
	if len(cards) > 0 {
		toldUsage() // the menu bar shows the same cards (#2357), told once
	}
}

// RefreshReport is RefreshUsage answering the cards it read, as
// QuotaReport tells them: magpie quota refresh (#2104), which reads the
// card the Usage page's refresh button does and shows only it. A process
// that has read no other card yet (the CLI) has nothing to merge it into,
// so the cards come from the read itself, not from the caches.
func RefreshReport(ctx context.Context, provider, user string, now time.Time) []Quota {
	subs, plans, balances := refreshCards(ctx, provider, user)
	plans, subs = seatCards(plans, subs)
	subs = withAccountNames(subs)
	return withServed(quotaReport(subs, plans, balances, now), LastServed())
}

// refreshCards is RefreshUsage, with the cards it read: only the asked
// card's provider (and account), each kind apart.
func refreshCards(ctx context.Context, provider, user string) (subs, plans, balances []SubscriptionQuota) {
	// a remote magpie's card is read again there (remote_quotas.go)
	if p, card, ok := remoteOf(provider); ok {
		refreshRemote(ctx, p, card, user)
		for _, q := range append(remoteCards(ctx, p, false), remoteKeyCards(ctx, p, false, "")...) {
			if card != "" && !strings.EqualFold(q.Provider, provider) || user != "" && !strings.EqualFold(q.User, user) {
				continue
			}
			switch q.Kind {
			case "plan":
				plans = append(plans, q)
			case "balance":
				balances = append(balances, q)
			default:
				subs = append(subs, q)
			}
		}
		return subs, plans, balances
	}
	r := cardRefresh{provider, user}
	ctx = context.WithValue(Asked(ctx), cardRefreshKey{}, r) // a card's refresh is the user asking
	ctx, seq := quotaReading(ctx)
	if provider == "claude" {
		// Claude Code's /usage, run when the user asks (claudeWindows)
		markClaudeUsageAsked()
	}
	// what PlanQuotas and KeyBalances answer of a card read again is
	// that read's cards alone; kept to the card asked all the same
	asked := func(qs []SubscriptionQuota) []SubscriptionQuota {
		return slices.DeleteFunc(slices.Clone(qs), func(q SubscriptionQuota) bool { return !r.wants(q.Provider, q.User) })
	}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); plans = asked(PlanQuotas(ctx)) }()
	go func() { defer wg.Done(); balances = asked(KeyBalances(ctx)) }()
	go func() {
		defer wg.Done()
		out := fetchSubscriptionUsage(ctx)
		now := time.Now()
		noteDailyCredits(out, now)
		noteCardHistory(out, now)
		c := &subscriptionUsageCache
		c.Lock()
		c.data = cacheCards(c.data, out, seq, true)
		c.Unlock()
		subs = out
	}()
	wg.Wait()
	return subs, plans, balances
}

// keepCardRead keeps q, an account's card read outside a refresh of it
// (the keep-alive's run, renewClaude), as refreshCards keeps a card read
// again: in place of the card, newer reads standing, with its history,
// and the menu bar told. A card the Usage page doesn't have (hidden, or
// nothing read yet) isn't added.
func keepCardRead(q SubscriptionQuota, seq uint64) {
	out := []SubscriptionQuota{q}
	c := &subscriptionUsageCache
	c.Lock()
	has := slices.ContainsFunc(c.data, func(k SubscriptionQuota) bool {
		return k.Provider == q.Provider && strings.EqualFold(k.User, q.User)
	})
	if has {
		c.data = cacheCards(c.data, out, seq, true)
	}
	c.Unlock()
	if has {
		noteQuotaHistory(out, time.Now())
		toldUsage()
	}
}

// cacheCards commits a batch without overwriting cards read by a newer
// batch. A whole-page read still replaces the other cards (and drops ones
// no longer present); a partial read keeps the rest as they were.
func cacheCards(kept, got []SubscriptionQuota, seq uint64, partial bool) []SubscriptionQuota {
	out := slices.Clone(got)
	for i := range out {
		out[i].readSeq = seq
	}
	if partial {
		return mergeCards(kept, out)
	}
	for _, q := range kept {
		if q.readSeq > seq {
			out = mergeCards(out, []SubscriptionQuota{q})
		}
	}
	return out
}

// mergeCards is the cards of kept with each of got in place of the one of
// its provider and account, or after them when it is new. Nothing kept,
// nothing is: the next read reads every card.
func mergeCards(kept, got []SubscriptionQuota) []SubscriptionQuota {
	if kept == nil {
		return nil
	}
	out := slices.Clone(kept)
	for _, g := range got {
		i := slices.IndexFunc(out, func(q SubscriptionQuota) bool {
			return q.Provider == g.Provider && strings.EqualFold(q.User, g.User)
		})
		if i < 0 {
			out = append(out, g)
		} else if g.readSeq >= out[i].readSeq {
			out[i] = g
		}
	}
	return out
}

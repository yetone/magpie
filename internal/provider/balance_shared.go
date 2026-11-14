package provider

// One account's balance read through several of its keys (mimi on
// Discord: a site's three keys each showed its $8.39 and its own curve).
// A balance query that tells the account's (balanceAccountWide) answers
// every key of the account the same, so its card would be shown once a
// key. Two keys are taken for one account only when their balances moved
// together: the same amount before and after their latest change, read
// within balanceHistGap of each other. Two accounts with the same amount
// (a fresh grant each, or both at zero) stay apart until one is spent
// from, and keys whose amounts differ are never one. A key's own query
// (new-api's /api/usage/token, a quota of its own) is never shared.
//
// Each key is still read, kept and drawn over time on its own; the keys
// found to be one account are told by SharedBalance, the first one's
// name, for the Usage page to show their balance once.

import (
	"context"
	"strings"
	"time"
)

// balanceMove is a balance's latest change: from one amount to another,
// the new one first read at At.
type balanceMove struct {
	from, to float64
	at       time.Time
}

// lastBalanceMove is the latest change in pts; false when it has none.
func lastBalanceMove(pts []BalancePoint) (balanceMove, bool) {
	k := len(pts)
	if k < 2 {
		return balanceMove{}, false
	}
	to := pts[k-1].Amount
	i := k - 1
	for i > 0 && pts[i-1].Amount == to {
		i--
	}
	if i == 0 {
		return balanceMove{}, false
	}
	return balanceMove{pts[i-1].Amount, to, pts[i].At}, true
}

// movedTogether says two balances' latest changes were the same change,
// at the same time.
func movedTogether(a, b []BalancePoint) bool {
	ma, ok := lastBalanceMove(a)
	mb, ok2 := lastBalanceMove(b)
	if !ok || !ok2 || ma.from != mb.from || ma.to != mb.to {
		return false
	}
	d := ma.at.Sub(mb.at)
	return d <= balanceHistGap && -d <= balanceHistGap
}

// sharesAccountBalance says q is a key's card whose balance, read just
// now, is its account's: one amount, nothing kept from before.
func sharesAccountBalance(q SubscriptionQuota) bool {
	return q.wideBalance && q.User != "" && q.Balance != "" && q.Error == "" && q.AsOf == nil &&
		len(q.BalanceParts) == 0 && balanceCurrencies(q.Balance) == nil
}

// sharedBalanceHist is the balances' history shareBalances goes by, read
// before the cards' lock is taken; nil when none of cards is an account's
// balance, as then none is shared.
func sharedBalanceHist(cards []SubscriptionQuota) balanceHist {
	for _, q := range cards {
		if sharesAccountBalance(q) {
			balanceHistMu.Lock()
			defer balanceHistMu.Unlock()
			return balanceHistShared() // shareBalances only reads it
		}
	}
	return nil
}

// shareBalances sets SharedBalance on the keys of qs found to read one
// account's balance (see above) by their history h, afresh each time, in
// place: qs is a copy of the caller's.
func shareBalances(qs []SubscriptionQuota, h balanceHist) []SubscriptionQuota {
	for i := range qs {
		qs[i].SharedBalance = ""
	}
	if h == nil {
		return qs
	}
	for i := range qs {
		a := &qs[i]
		if a.SharedBalance != "" || !sharesAccountBalance(*a) {
			continue
		}
		for j := i + 1; j < len(qs); j++ {
			b := &qs[j]
			if b.SharedBalance != "" || b.Provider != a.Provider || b.From != a.From || b.Balance != a.Balance || !sharesAccountBalance(*b) {
				continue
			}
			if movedTogether(h[quotaHistKey(a.Provider, a.User)], h[quotaHistKey(b.Provider, b.User)]) {
				a.SharedBalance, b.SharedBalance = a.User, a.User
			}
		}
	}
	return qs
}

// sharesAskedCard says the key user of provider shows its balance on the
// card a refresh asks for (another key's, SharedBalance): read again with
// it, so the card stays one.
func sharesAskedCard(ctx context.Context, kept []SubscriptionQuota, provider, user string) bool {
	r, ok := refreshing(ctx)
	if !ok || r.user == "" {
		return false
	}
	for _, q := range kept {
		if q.Provider == provider && strings.EqualFold(q.User, user) && q.SharedBalance != "" {
			return r.wants(provider, q.SharedBalance)
		}
	}
	return false
}

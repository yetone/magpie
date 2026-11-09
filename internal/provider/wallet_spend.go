package provider

// A wallet's window (QuotaWindow.Balance: AStudio's 通用积分 and 星火积分,
// SuperMagic's points) tells only what is left: no total, no share used.
// A usage cap had nothing to hold it at. It read 0% used until the wallet
// was empty, so a cap by share never tripped and one by amount, which
// needs the window's total, left it aside; an account capped at 4 spent
// over 1,000 credits past it (#5770, 9won_25273 on Discord).
//
// So magpie counts what such a wallet spends itself. Each reading's
// balance is set against the one before: a fall is spent, a rise (a
// top-up, a daily grant) is not. The count runs over a period, the
// window's own when it tells a reset, else the local day (a wallet never
// renews, and a cap that, once reached, held the account for good would
// turn it off with nothing to say when it comes back; the ask was a
// daily one). A period's count starts at 0 at its first reading. What
// went before that reading, or between the last reading of one period
// and the first of the next, isn't known and isn't counted.
//
// On an account with a usage cap, the window then reads as Amount spent
// of Limit, what the period had (spent plus what is left), and Used that
// share: a cap by share or by amount holds it as it holds any window, in
// routing (allowanceOf takes them as they are) and on the GUI's row.
// Without a cap the window keeps its balance alone, as before, and
// routing goes by that (Allowance.Left).
//
// The counts are kept in wallet-spend.json beside providers.json, by
// provider, account and window, so a restart doesn't start a period's
// count again.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// walletSpend is one wallet window's count for its period.
type walletSpend struct {
	Ends  time.Time `json:"ends"`  // when the period ends
	Since time.Time `json:"since"` // its first reading
	Spent float64   `json:"spent"`
	Last  float64   `json:"last"` // the balance last read
}

var walletSpends struct {
	sync.Mutex
	m    map[string]walletSpend
	path string // the file m was read from: another HOME reads its own
}

func walletSpendPath() string { return filepath.Join(filepath.Dir(Path()), "wallet-spend.json") }

// walletPeriodEnd is when the period a reading of w at now starts ends:
// the window's reset when it tells one, else the next local midnight.
func walletPeriodEnd(w QuotaWindow, now time.Time) time.Time {
	switch {
	case w.ResetsAt != nil && w.ResetsAt.After(now):
		return *w.ResetsAt
	case w.ResetSecs > 0:
		return now.Add(time.Duration(w.ResetSecs) * time.Second)
	}
	y, m, d := now.Date()
	return time.Date(y, m, d+1, 0, 0, 0, 0, now.Location())
}

// noteWalletSpend counts the reading of wallet window w at now under key
// and is its period's count.
func noteWalletSpend(key string, w QuotaWindow, now time.Time) walletSpend {
	b := *w.Balance
	path := walletSpendPath()
	c := &walletSpends
	c.Lock()
	defer c.Unlock()
	if c.path != path {
		c.path, c.m = path, nil
		if raw, err := os.ReadFile(path); err == nil {
			// one that can't be read starts each count again from now,
			// which each window's SpentSince says
			json.Unmarshal(raw, &c.m)
		}
	}
	s, ok := c.m[key]
	switch {
	case !ok || !now.Before(s.Ends) || now.Before(s.Since):
		s = walletSpend{Ends: walletPeriodEnd(w, now), Since: now, Last: b}
	case b < s.Last:
		s.Spent += s.Last - b
		s.Last = b
	case b > s.Last:
		s.Last = b // topped up: nothing spent
	default:
		return s
	}
	if c.m == nil {
		c.m = map[string]walletSpend{}
	}
	c.m[key] = s
	for k, e := range c.m {
		if now.Sub(e.Ends) > 7*24*time.Hour {
			delete(c.m, k) // a wallet not read for a week
		}
	}
	if raw, err := json.Marshal(c.m); err == nil {
		os.MkdirAll(filepath.Dir(path), 0o700)
		writeFileAtomic(path, raw)
	}
	return s
}

// withWalletSpend is a fresh reading q of account user with each wallet
// window's spend counted (Spent, SpentSince) and, on an account with a
// usage cap, read as that much used of what its period had. q's windows
// are its own, changed in place.
func withWalletSpend(q SubscriptionQuota, user string, now time.Time) SubscriptionQuota {
	if q.Error != "" || q.Provider == "" || user == "" {
		return q
	}
	var caps *WindowCaps
	for i := range q.Windows {
		w := &q.Windows[i]
		if w.Balance == nil || w.Text {
			continue
		}
		s := noteWalletSpend(q.Provider+"/"+accountKey(user)+"/"+WindowCapID(w.Name), *w, now)
		spent, since := s.Spent, s.Since
		w.Spent, w.SpentSince = &spent, &since
		if caps == nil {
			c := AccountCapsOf(q.Provider, user)
			caps = &c
		}
		of := spent + max(*w.Balance, 0)
		if !caps.Holds() || of <= 0 {
			continue
		}
		w.Amount, w.Limit = spent, of
		w.Used = max(w.Used, spent/of*100)
	}
	return q
}

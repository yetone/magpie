package provider

// A gateway key's limit can follow one subscription account's own window
// (ljayic on X): its 5 hours, its week or its month, starting and ending
// when the vendor says that account's does. Here is where such a window
// is found: in the account's last good reading (quotas.json, keepLast),
// never by asking the vendor on the request's path.

import (
	"context"
	"strings"
	"sync/atomic"
	"time"
)

// AccountWindow is the window named name of account ("<provider>/<user>",
// as a key's accounts are written) as the account's last good reading
// tells it: when it began and when it resets. known is false when there
// is no such reading, it has no such window, the window has no reset
// clock or no length, or its reset has passed with no reading since.
//
// When it isn't known, start is the latest time known to be no later than
// the window's start: the reset that passed (a window never begins before
// the last one ended), or zero when nothing is. A limit counting from it
// counts every call of the window it follows, and maybe more, never less.
//
// It asks no vendor itself: it is on every keyed request's path. When the
// window isn't known, the accounts are read again in the background, at
// most once a minute, as the Usage page would (and only when allowances
// are read without being asked, settings.QuotaReads).
func AccountWindow(account, name string, now time.Time) (start, reset time.Time, known bool) {
	return accountWindowVia(account, name, now)
}

var accountWindowVia = accountWindow

// AccountWindowVia has AccountWindow answer with f until restore is
// called: a test's accounts and windows, with no reading and no vendor.
func AccountWindowVia(f func(account, name string, now time.Time) (start, reset time.Time, known bool)) (restore func()) {
	was := accountWindowVia
	accountWindowVia = f
	return func() { accountWindowVia = was }
}

func accountWindow(account, name string, now time.Time) (start, reset time.Time, known bool) {
	pid, user, ok := strings.Cut(account, "/")
	if !ok || pid == "" || user == "" || strings.TrimSpace(name) == "" {
		return time.Time{}, time.Time{}, false
	}
	defer func() {
		if !known {
			readAccountWindows(now)
		}
	}()
	c := &lastQuotas
	c.Lock()
	c.load()
	e, had := c.m[strings.ToLower(pid)+"/"+strings.ToLower(user)]
	c.Unlock()
	if !had {
		return time.Time{}, time.Time{}, false
	}
	for i, w := range e.Q.Windows {
		if !sameWindowName(w, name) {
			continue
		}
		if i < len(e.Spans) {
			w.Span = e.Spans[i].Span
		}
		s, end, ok := w.Bounds(e.At)
		if !ok {
			if end := resetOf(w, e.At); !end.IsZero() && !end.After(now) {
				return end, time.Time{}, false
			}
			return time.Time{}, time.Time{}, false
		}
		if !end.After(now) {
			// over, and the next one not read yet
			return end, time.Time{}, false
		}
		return s, end, true
	}
	return time.Time{}, time.Time{}, false
}

// resetOf is when w, read at readAt, resets; zero when not told.
func resetOf(w QuotaWindow, readAt time.Time) time.Time {
	switch {
	case w.ResetsAt != nil:
		return *w.ResetsAt
	case w.ResetSecs > 0:
		return readAt.Add(time.Duration(w.ResetSecs) * time.Second)
	}
	return time.Time{}
}

// sameWindowName is whether w is the window a limit names: by its own
// name or, for a pool's, the name its line is kept under, in any case.
func sameWindowName(w QuotaWindow, name string) bool {
	id := WindowCapID(name)
	return WindowCapID(w.Name) == id || WindowCapID(w.LineName()) == id
}

// KeyWindow is a subscription account's window a gateway key's limit can
// follow, as the limit editor lists it.
type KeyWindow struct {
	Account  string    `json:"account"`  // "<provider>/<user>"
	Window   string    `json:"window"`   // the window's name, as the account's card says it
	Provider string    `json:"provider"` // the card's name: "Codex", "Claude"
	User     string    `json:"user"`
	Alias    string    `json:"alias,omitempty"`
	Start    time.Time `json:"start"`
	Reset    time.Time `json:"reset"`
}

// KeyWindows are the windows of every subscription account a key's limit
// can follow: each that runs out and starts again on a clock the vendor
// tells, from the accounts' cards as last read. One with no reset clock
// can't place a window, nor can money set aside, a text row or another
// magpie's card.
func KeyWindows(ctx context.Context, now time.Time) []KeyWindow {
	var out []KeyWindow
	names := AccountNames()
	for _, q := range SubscriptionUsage(ctx) {
		if q.User == "" || q.From != "" || q.GatewayKey != nil {
			continue
		}
		account := q.Provider + "/" + q.User
		for _, w := range q.Windows {
			if w.Text || w.Unlimited || w.Lane != "" || w.Aside || w.Balance != nil {
				continue
			}
			name := w.LineName()
			s, end, ok := accountWindow(account, name, now)
			if !ok {
				continue
			}
			kw := KeyWindow{Account: account, Window: name, Provider: q.Name, User: q.User, Start: s, Reset: end}
			if n := names[strings.ToLower(account)]; n != "" {
				kw.Alias = n
			}
			out = append(out, kw)
		}
	}
	return out
}

// accountWindowsAsked is when readAccountWindows last began a read,
// UnixNano.
var accountWindowsAsked atomic.Int64

// readAccountWindows reads the accounts' allowances again in the
// background, at most once a minute, without waiting for it.
var readAccountWindows = func(now time.Time) {
	last := accountWindowsAsked.Load()
	if now.UnixNano()-last < int64(time.Minute) && now.UnixNano() >= last {
		return
	}
	if accountWindowsAsked.CompareAndSwap(last, now.UnixNano()) {
		go readAccounts() // never under the caller's locks
	}
}

// readAccounts begins a read of every account's allowances and waits
// for nothing.
var readAccounts = func() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	SubscriptionUsage(ctx)
}

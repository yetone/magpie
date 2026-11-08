package provider

// A key's balance typed by hand (tenacious_eagle on Discord): for a key
// whose vendor tells no balance, at no Balance URL magpie knows or finds,
// the user types what the vendor's own page says is left. The key's card
// then shows that less what magpie's own usage prices the calls it sent
// on the key since, marked as an estimate ("≈$12.34"). Typing the balance
// again sets it anew, from then.
//
// A balance the vendor tells always wins: a key with a balance source, or
// whose relay's query is found (balance_probe.go), shows that, and the
// typed one stays saved, unused, as the user left it.

// The balance may be typed in yuan (tenacious_eagle_96548): the spend,
// priced in USD, is then counted at the dollar's rate in yuan magpie last
// learned (internal/fx, fetched behind the page and cached), and while it
// has never learned one what is left is unknown, never counted at a guess.
// It may refill (the same user: $10 a month from the 1st): at each refill,
// at midnight local time on a day of the month or of the week, the amount
// typed is whole again and the spend counted from then.

// It may have a second part that expires (the same user: some vendors
// split a balance into money that stays and credit good until a date).
// That part is spent first, as DeepSeek spends its granted balance before
// the money topped up, and from the end of its last day, local time, it
// counts no more; the card says how much of it is left and in how many
// days it goes. A balance typed before has none and is counted as it was.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/fx"
)

// OwnBalance is a key's balance as the user typed it: USD left at At, or,
// with CNY set, that many yuan; with Refill, whole again at each refill.
type OwnBalance struct {
	USD float64 `json:"usd,omitempty"`
	// CNY is the amount when it was typed in yuan; USD is then unused
	CNY    *float64  `json:"cny,omitempty"`
	At     time.Time `json:"at"`
	Refill *Refill   `json:"refill,omitempty"`
	// Expiring is a part of the balance, in its currency, good until a
	// day; nil for none
	Expiring *Expiring `json:"expiring,omitempty"`
}

// Expiring is credit on a key that is gone after a day: Amount (in the
// balance's currency) as it stood At, spent before the rest of the
// balance, and counted through Until (a date, 2006-01-02), to midnight
// after it in the user's local time.
type Expiring struct {
	Amount float64   `json:"amount"`
	Until  string    `json:"until"`
	At     time.Time `json:"at"`
}

// End is when the credit counts no more: midnight after Until, in loc.
// false when Until is no date.
func (x Expiring) End(loc *time.Location) (time.Time, bool) {
	d, err := time.ParseInLocation(time.DateOnly, x.Until, loc)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, loc), true
}

// check says what is wrong with x, nil when nothing is.
func (x Expiring) check() error {
	if !(x.Amount > 0 && x.Amount <= MaxOwnBalance) {
		return fmt.Errorf("credit that expires is an amount above 0 and up to %d, not %v", MaxOwnBalance, x.Amount)
	}
	if _, ok := x.End(time.UTC); !ok {
		return fmt.Errorf("credit expires on a date (2026-12-31), not %q", x.Until)
	}
	return nil
}

// Amount is the balance typed and its currency, "cny" or "usd".
func (b OwnBalance) Amount() (float64, string) {
	if b.CNY != nil {
		return *b.CNY, "cny"
	}
	return b.USD, "usd"
}

// Refill is when a balance typed is whole again: at midnight, local time,
// on Day of each month (1–31, a month without that day on its last) or of
// each week (1 Monday to 7 Sunday).
type Refill struct {
	Every string `json:"every"`
	Day   int    `json:"day"`
}

// How often a balance typed refills.
const (
	RefillMonth = "month"
	RefillWeek  = "week"
)

// Last is the last refill at or before now, in now's location.
func (r Refill) Last(now time.Time) time.Time {
	y, m, d := now.Date()
	loc := now.Location()
	if r.Every == RefillWeek {
		iso := (int(now.Weekday())+6)%7 + 1
		return time.Date(y, m, d-(iso-r.Day+7)%7, 0, 0, 0, 0, loc)
	}
	t := monthDay(y, m, r.Day, loc)
	if t.After(now) {
		t = monthDay(y, m-1, r.Day, loc)
	}
	return t
}

// Next is the first refill after now, in now's location.
func (r Refill) Next(now time.Time) time.Time {
	l := r.Last(now)
	y, m, d := l.Date()
	if r.Every == RefillWeek {
		return time.Date(y, m, d+7, 0, 0, 0, 0, l.Location())
	}
	return monthDay(y, m+1, r.Day, l.Location())
}

// monthDay is midnight on day of month m of year y, or on the month's last
// day when it has fewer.
func monthDay(y int, m time.Month, day int, loc *time.Location) time.Time {
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, loc).Day()
	return time.Date(y, m, min(day, last), 0, 0, 0, 0, loc)
}

// check says what is wrong with r, nil when nothing is.
func (r Refill) check() error {
	switch {
	case r.Every == RefillMonth && r.Day >= 1 && r.Day <= 31:
	case r.Every == RefillWeek && r.Day >= 1 && r.Day <= 7:
	default:
		return fmt.Errorf("a balance refills each month on a day from 1 to 31, or each week on a day from 1 (Monday) to 7 (Sunday), not %s %d", r.Every, r.Day)
	}
	return nil
}

// String is r as the CLI takes and shows it: monthly:1, weekly:mon.
func (r Refill) String() string {
	if r.Every == RefillWeek {
		return "weekly:" + strings.ToLower(time.Weekday(r.Day % 7).String()[:3])
	}
	return fmt.Sprintf("monthly:%d", r.Day)
}

// MaxOwnBalance bounds a balance typed by hand, in USD or in yuan.
const MaxOwnBalance = 1_000_000

// BalanceEstimate is what a card shows of a balance typed by hand: the
// amount typed (in Currency), when, and what magpie counted spent on the
// key since (in USD), from the last refill when it refills.
type BalanceEstimate struct {
	Set   float64   `json:"set"`
	At    time.Time `json:"at"`
	Spent float64   `json:"spent"`
	// Currency is the amount's, "cny" when typed in yuan, else unset (USD)
	Currency string `json:"currency,omitempty"`
	// Rate is the yuan a dollar is counted at, for one in yuan
	Rate float64 `json:"rate,omitempty"`
	// Left is what is left, in Currency; nil while it can't be told (in
	// yuan with spend counted and no rate ever learned)
	Left *float64 `json:"left"`
	// From is what the spend is counted from: At, or the last refill
	From   time.Time  `json:"from"`
	Refill *Refill    `json:"refill,omitempty"`
	Next   *time.Time `json:"next,omitempty"`
	// Expiring is the part of the balance that expires, when it has one
	// and that part hasn't expired yet
	Expiring *ExpiringLeft `json:"expiring,omitempty"`
	// Expired is the end of a part that has expired; Left no longer
	// counts it
	Expired *time.Time `json:"expired,omitempty"`
}

// ExpiringLeft is what is left of a balance's part that expires: Set as
// typed, At, Left of it now (in the balance's currency; nil while Left
// is) and Ends when it counts no more. Left is part of the estimate's
// Left, not on top of it.
type ExpiringLeft struct {
	Set  float64   `json:"set"`
	At   time.Time `json:"at"`
	Left *float64  `json:"left"`
	Ends time.Time `json:"ends"`
}

// KeySpent is usage.KeySpent, which package usage sets: what the calls
// magpie sent on the key keyID from start on cost, in USD. nil (a test
// of this package alone) counts nothing.
var KeySpent func(keyID string, start time.Time) float64

// ownNow is the clock a balance typed is counted by: when it was typed,
// and which refill is the last.
var ownNow = time.Now

// cnyPerUSD is the dollar's rate in yuan magpie last learned, false when
// it has never learned one; never waited for.
var cnyPerUSD = func() (float64, bool) {
	r, ok := fx.Known()
	return r.CNYPerUSD, ok && r.CNYPerUSD > 0
}

// KeyOwnBalance is the balance typed for p's key keyID, and whether it
// has one.
func (p Provider) KeyOwnBalance(keyID string) (OwnBalance, bool) {
	b, ok := p.KeyOwnBalances[strings.ToLower(keyID)]
	return b, ok
}

// OwnBalanceEstimate is what is left of the balance typed for p's key
// keyID, and whether it has one.
func (p Provider) OwnBalanceEstimate(keyID string) (BalanceEstimate, bool) {
	b, ok := p.KeyOwnBalance(keyID)
	if !ok {
		return BalanceEstimate{}, false
	}
	now := ownNow()
	amount, cur := b.Amount()
	e := BalanceEstimate{Set: amount, At: b.At, From: b.At}
	if b.Refill != nil {
		r := *b.Refill
		e.Refill = &r
		// now in local time (ownNow's), as the user means "the 1st"
		if l := r.Last(now); l.After(e.From) {
			e.From = l
		}
		next := r.Next(now)
		e.Next = &next
	}
	spent := func(start time.Time) float64 {
		if KeySpent == nil || !start.Before(now) {
			return 0
		}
		return KeySpent(keyID, start)
	}
	e.Spent = spent(e.From)
	// the spend in the balance's currency; false while it can't be told
	inCur := func(usd float64) (float64, bool) { return usd, true }
	if cur == "cny" {
		e.Currency = cur
		rate, known := cnyPerUSD()
		if known {
			e.Rate = rate
		}
		inCur = func(usd float64) (float64, bool) {
			if usd == 0 {
				return 0, true
			}
			return usd * rate, known
		}
	}
	// a part that expires takes the spend first, from when it was typed
	// to its end; what it took after From is not the rest's to pay
	took := 0.0
	if x := b.Expiring; x != nil {
		if end, ok := x.End(now.Location()); ok {
			between := func(from, to time.Time) float64 { return spent(from) - spent(to) }
			stop := minTime(end, now)
			usd := 0.0
			if x.At.Before(stop) {
				usd = between(x.At, stop)
			}
			before := 0.0
			if x.At.Before(e.From) {
				before = between(x.At, minTime(e.From, stop))
			}
			s, ok1 := inCur(usd)
			sb, ok2 := inCur(before)
			if !ok1 || !ok2 {
				return e, true // spend counted at no rate: unknown
			}
			used := min(x.Amount, s)
			took = max(0, used-min(x.Amount, sb))
			if end.After(now) {
				l := x.Amount - used
				e.Expiring = &ExpiringLeft{Set: x.Amount, At: x.At, Left: &l, Ends: end}
			} else {
				e.Expired = &end
			}
		}
	}
	s, ok := inCur(e.Spent)
	if !ok {
		return e, true // spend counted at no rate: unknown
	}
	left := amount - (s - took)
	if e.Expiring != nil {
		left += *e.Expiring.Left
	}
	e.Left = &left
	return e, true
}

// TakesOwnBalance says p's keys may have a balance typed by hand: keys
// whose vendor tells none magpie knows where to ask. A relay whose query
// is looked for (probesBalance) takes one too: when none is found, the
// typed balance is shown.
func (p Provider) TakesOwnBalance() bool {
	if p.Account != nil || p.Key == "" {
		return false
	}
	if hasBalanceSource(p) || clineKeyCard(p) || openCodeZen(p) {
		return false
	}
	_, plan := planQuotaSourceOf(p)
	return !plan
}

// ownBalanceCard is the card of key (one of p's) with a balance typed by
// hand, user naming it when p has several: what was typed less what was
// spent since. false when the key has none.
func ownBalanceCard(p Provider, key, user string) (SubscriptionQuota, bool) {
	e, ok := p.OwnBalanceEstimate(keyID(key))
	if !ok {
		return SubscriptionQuota{}, false
	}
	now := time.Now()
	q := SubscriptionQuota{
		Provider: p.ID, Name: p.Name, Icon: p.Icon, User: user, Windows: []QuotaWindow{},
		Estimate: &e,
		ReadAt:   &now,
	}
	if e.Left == nil {
		q.Error = "the dollar's rate in yuan isn't known yet, so what is left of the balance in yuan can't be counted"
	} else {
		q.Balance = ownBalanceText(*e.Left, e.Currency)
		// of it, the part that expires, and when (a line of its own)
		if x := e.Expiring; x != nil && *x.Left > 0 {
			ends := x.Ends
			q.BalanceSplit = []BalancePart{{Text: ownBalanceText(*x.Left, e.Currency), Expires: &ends}}
		}
	}
	return q, true
}

// ownBalanceText is an estimated balance as a card shows it, "≈$12.34" or
// "≈¥88.00", below nothing as "≈$-1.20": the calls priced since went past
// it.
func ownBalanceText(v float64, currency string) string {
	if math.Abs(v) < 0.005 {
		v = 0
	}
	sign := "$"
	if currency == "cny" {
		sign = "¥"
	}
	return fmt.Sprintf("≈%s%.2f", sign, v)
}

// SetKeyOwnBalance gives the key ref (its id or name) of the provider id
// the balance b (its amount, in USD or CNY), counted from now; nil takes
// it off. With no Refill of its own, b keeps the one the key had, and with
// no Expiring, the part that expires the key had in the same currency (in
// the other, its amount would mean another sum, so it goes).
func SetKeyOwnBalance(id, ref string, b *OwnBalance) error {
	if b != nil {
		if err := checkOwnBalance(*b); err != nil {
			return err
		}
	}
	return editOwnBalance(id, ref, func(had OwnBalance, ok bool) (*OwnBalance, error) {
		if b == nil {
			return nil, nil
		}
		v := *b
		v.At = ownNow().UTC()
		if v.Refill == nil && ok {
			v.Refill = had.Refill
		}
		if v.Expiring == nil && ok && (v.CNY == nil) == (had.CNY == nil) {
			v.Expiring = had.Expiring
		}
		return &v, nil
	})
}

// SetKeyExpiring gives the balance typed for the key ref of the provider
// id the part x that expires, in the balance's currency, counted from now;
// nil takes it off. The rest of the balance stays as it was. A key with
// no balance typed takes none: type the balance that stays first, 0 when
// all of it expires.
func SetKeyExpiring(id, ref string, x *Expiring) error {
	if x != nil {
		if err := x.check(); err != nil {
			return err
		}
	}
	return editOwnBalance(id, ref, func(had OwnBalance, ok bool) (*OwnBalance, error) {
		if !ok {
			return nil, fmt.Errorf("the key has no balance typed: type the balance that stays first (0 when all of it expires)")
		}
		if x == nil {
			had.Expiring = nil
			return &had, nil
		}
		v := *x
		v.At = ownNow().UTC()
		had.Expiring = &v
		return &had, nil
	})
}

// SetKeyBalanceRefill gives the balance typed for the key ref of the
// provider id the refill r, nil for none; its amount and when it was
// typed stay. A key with no balance typed takes none.
func SetKeyBalanceRefill(id, ref string, r *Refill) error {
	if r != nil {
		if err := r.check(); err != nil {
			return err
		}
	}
	return editOwnBalance(id, ref, func(had OwnBalance, ok bool) (*OwnBalance, error) {
		if !ok {
			return nil, fmt.Errorf("the key has no balance typed to refill: type one first")
		}
		had.Refill = r
		return &had, nil
	})
}

// editOwnBalance saves what edit makes of the balance typed for the key
// ref of the provider id, nil taking it off.
func editOwnBalance(id, ref string, edit func(had OwnBalance, ok bool) (*OwnBalance, error)) error {
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account != nil {
		return fmt.Errorf("%s is a subscription: a balance typed by hand is for an API key", p.Name)
	}
	r, ok := p.accountRef(ref)
	if !ok {
		return fmt.Errorf("%s has no key %q", p.Name, ref)
	}
	k := strings.ToLower(r)
	m := map[string]OwnBalance{}
	for id, v := range p.KeyOwnBalances {
		m[id] = v
	}
	had, ok := m[k]
	b, err := edit(had, ok)
	if err != nil {
		return err
	}
	if b == nil {
		if !ok {
			return nil
		}
		delete(m, k)
	} else {
		m[k] = *b
	}
	p.KeyOwnBalances = m
	if err := Save(*p); err != nil {
		return err
	}
	// the cards are drawn again with it; what routing knows of each
	// key's allowance stays, as a typed balance tells routing nothing
	keyBalanceCache.Lock()
	keyBalanceCache.data = nil
	keyBalanceCache.Unlock()
	return nil
}

// checkOwnBalance says what is wrong with a balance typed, nil when
// nothing is.
func checkOwnBalance(b OwnBalance) error {
	v, _ := b.Amount()
	if !(v >= 0 && v <= MaxOwnBalance) {
		return fmt.Errorf("a balance is an amount from 0 to %d, in USD or in yuan (¥), not %v", MaxOwnBalance, v)
	}
	if b.CNY != nil && b.USD != 0 {
		return fmt.Errorf("a balance is in USD or in yuan, not both")
	}
	if b.Expiring != nil {
		if err := b.Expiring.check(); err != nil {
			return err
		}
	}
	if b.Refill != nil {
		return b.Refill.check()
	}
	return nil
}

// ParseExpiring reads credit that expires as the CLI takes it, an amount
// and the last day it counts: "20@2026-12-31", the amount in the
// balance's currency (so no sign of its own); off for none (nil).
func ParseExpiring(s string) (*Expiring, error) {
	a := strings.TrimSpace(s)
	switch strings.ToLower(a) {
	case "off", "none", "no", "-":
		return nil, nil
	}
	bad := fmt.Errorf("credit that expires is amount@date (20@2026-12-31) or off, not %q", s)
	amount, until, ok := strings.Cut(a, "@")
	if !ok {
		return nil, bad
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(amount), ",", ""), 64)
	if err != nil {
		return nil, bad
	}
	x := Expiring{Amount: v, Until: strings.TrimSpace(until)}
	if err := x.check(); err != nil {
		return nil, err
	}
	return &x, nil
}

// ParseOwnBalance reads a balance as the CLI takes it, an amount in USD
// ("20", "$20", "12.5 usd") or in yuan ("¥100", "100cny", "100 rmb",
// "100元"); off for none (nil).
func ParseOwnBalance(amount string) (*OwnBalance, error) {
	a := strings.ToLower(strings.TrimSpace(amount))
	switch a {
	case "off", "none", "no", "-":
		return nil, nil
	}
	cny := false
	for _, s := range []string{"¥", "￥", "cny", "rmb"} {
		if strings.HasPrefix(a, s) {
			a, cny = strings.TrimPrefix(a, s), true
		}
	}
	for _, s := range []string{"元", "cny", "rmb", "yuan"} {
		if strings.HasSuffix(a, s) {
			a, cny = strings.TrimSuffix(a, s), true
		}
	}
	if !cny {
		a = strings.TrimSuffix(strings.TrimPrefix(a, "$"), "usd")
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(a), ",", ""), 64)
	if err != nil {
		return nil, fmt.Errorf("a balance is an amount in USD (20, 12.5), in yuan (¥140) or off, not %q", amount)
	}
	b := OwnBalance{USD: v}
	if cny {
		b = OwnBalance{CNY: &v}
	}
	if err := checkOwnBalance(b); err != nil {
		return nil, err
	}
	return &b, nil
}

// ParseRefill reads a refill as the CLI takes it: monthly (on the 1st),
// monthly:15, weekly (on Monday), weekly:fri or weekly:5; off or never
// for none (nil).
func ParseRefill(s string) (*Refill, error) {
	a := strings.ToLower(strings.TrimSpace(s))
	switch a {
	case "off", "none", "no", "never", "-":
		return nil, nil
	}
	every, day, _ := strings.Cut(a, ":")
	bad := fmt.Errorf("a refill is monthly[:day 1-31], weekly[:mon…sun] or off, not %q", s)
	var r Refill
	switch every {
	case "monthly", "month", "m":
		r = Refill{Every: RefillMonth, Day: 1}
		if day != "" {
			n, err := strconv.Atoi(day)
			if err != nil {
				return nil, bad
			}
			r.Day = n
		}
	case "weekly", "week", "w":
		r = Refill{Every: RefillWeek, Day: 1}
		if day != "" {
			r.Day = 0
			for i, w := range []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"} {
				if strings.HasPrefix(day, w) || day == strconv.Itoa(i+1) {
					r.Day = i + 1
				}
			}
		}
	default:
		return nil, bad
	}
	if err := r.check(); err != nil {
		return nil, err
	}
	return &r, nil
}

// normalKeyOwnBalances is m as it is kept: key ids in lower case, a
// balance out of bounds left out; nil for none. One written by hand with
// no date counts from the start of the usage log.
func normalKeyOwnBalances(m map[string]OwnBalance) map[string]OwnBalance {
	var out map[string]OwnBalance
	for k, b := range m {
		k = strings.ToLower(strings.TrimSpace(k))
		if b.Refill != nil && b.Refill.check() != nil {
			b.Refill = nil // a refill that isn't one, written by hand
		}
		if b.Expiring != nil && b.Expiring.check() != nil {
			b.Expiring = nil // nor credit that expires
		}
		if k == "" || checkOwnBalance(b) != nil {
			continue
		}
		if out == nil {
			out = map[string]OwnBalance{}
		}
		out[k] = b
	}
	return out
}

// ownKey is one of a provider's keys with a balance typed for it: the
// provider as asked with that key, and the card's user, its name when
// the provider has several keys on, as KeyBalances names a key's card.
type ownKey struct {
	p    Provider
	user string
}

// ownBalanceKeys are p's keys on that have a balance typed for them.
func ownBalanceKeys(p Provider) []ownKey {
	if len(p.KeyOwnBalances) == 0 {
		return nil
	}
	others := 0
	for _, k := range p.Keys {
		if !k.Off && k.Key != "" && k.Key != p.Key {
			others++
		}
	}
	var out []ownKey
	if _, ok := p.KeyOwnBalance(keyID(p.Key)); ok {
		user := ""
		if others > 0 {
			if user = p.KeyName; user == "" {
				user = Mask(p.Key)
			}
		}
		out = append(out, ownKey{p, user})
	}
	for _, k := range p.Keys {
		if k.Off || k.Key == "" || k.Key == p.Key {
			continue
		}
		if _, ok := p.KeyOwnBalance(keyID(k.Key)); !ok {
			continue
		}
		q := p
		q.Key = k.Key
		name := k.Name
		if name == "" {
			name = Mask(k.Key)
		}
		out = append(out, ownKey{q, name})
	}
	return out
}

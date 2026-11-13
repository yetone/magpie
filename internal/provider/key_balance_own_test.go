package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// spentStub counts what each key spent, as usage.KeySpent would, and
// what start it was asked from.
func spentStub(t *testing.T, spent map[string]float64) map[string]time.Time {
	t.Helper()
	asked := map[string]time.Time{}
	var mu sync.Mutex // the cards are drawn at once
	old := KeySpent
	KeySpent = func(id string, start time.Time) float64 {
		mu.Lock()
		defer mu.Unlock()
		asked[id] = start
		return spent[id]
	}
	t.Cleanup(func() { KeySpent = old })
	return asked
}

func ownCards(t *testing.T) map[string]SubscriptionQuota {
	t.Helper()
	keyBalanceCache.Lock()
	keyBalanceCache.data = nil
	keyBalanceCache.Unlock()
	out := map[string]SubscriptionQuota{}
	for _, q := range KeyBalances(context.Background()) {
		out[q.Provider+"|"+q.User] = q
	}
	return out
}

// A key whose vendor tells no balance (tenacious_eagle on Discord): the
// user types what the vendor's page says is left, and its card shows that
// less what magpie priced the calls it sent on the key since, marked as
// an estimate. Typing it again counts from then; off takes the card
// away. A key with none typed has no card, nor does a key turned off, and
// an estimate is no reading the balance history keeps.
func TestTypedBalanceCountsDown(t *testing.T) {
	probeSandbox(t)
	probeLocal = false // a relay on this computer is never looked at
	main, spare, off := keyID("sk-main"), keyID("sk-spare"), keyID("sk-off")
	asked := spentStub(t, map[string]float64{main: 7.5, spare: 3, off: 1})
	if err := Save(Provider{ID: "plain", Name: "Plain", Chat: "http://127.0.0.1:1/v1", Key: "sk-main", KeyName: "Main",
		Keys: []KeyAccount{{Name: "Spare", Key: "sk-spare"}, {Name: "Idle", Key: "sk-idle"}, {Name: "Off", Key: "sk-off", Off: true}}}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("plain")
	if !p.TakesOwnBalance() {
		t.Fatal("a key with no balance source takes none typed")
	}
	if c := ownCards(t); len(c) != 0 {
		t.Fatalf("cards with nothing typed: %+v", c)
	}
	twenty, one := &OwnBalance{USD: 20}, &OwnBalance{USD: 1}
	before := time.Now().UTC()
	for _, s := range []struct {
		ref string
		usd *OwnBalance
	}{{"Main", twenty}, {spare, one}, {"Off", one}} {
		if err := SetKeyOwnBalance("plain", s.ref, s.usd); err != nil {
			t.Fatal(err)
		}
	}
	c := ownCards(t)
	m, ok := c["plain|Main"]
	if !ok || m.Balance != "≈$12.50" || m.Estimate == nil || m.Estimate.Set != 20 || m.Estimate.Spent != 7.5 || m.Estimate.At.Before(before) {
		t.Fatalf("Main's card %+v (estimate %+v), want ≈$12.50 of 20 less 7.5 from now", m, m.Estimate)
	}
	if !asked[main].Equal(m.Estimate.At) {
		t.Errorf("spent counted from %v, not from when it was typed %v", asked[main], m.Estimate.At)
	}
	if s := c["plain|Spare"]; s.Balance != "≈$-2.00" {
		t.Errorf("Spare spent past what was typed: %q, want ≈$-2.00", s.Balance)
	}
	if n, ok := BalanceNumber(m.Balance); !ok || n != 12.5 {
		t.Errorf("a low-balance alert reads %v %v from %q", n, ok, m.Balance)
	}
	if len(c) != 2 {
		t.Errorf("cards %v: Idle has none typed and Off is off", c)
	}
	if bh := readBalanceHist(); len(bh) != 0 {
		t.Errorf("an estimate kept in the balance history: %+v", bh)
	}

	// typed again: counted from now
	time.Sleep(10 * time.Millisecond)
	if err := SetKeyOwnBalance("plain", "Main", &OwnBalance{USD: 30}); err != nil {
		t.Fatal(err)
	}
	if again := ownCards(t)["plain|Main"]; again.Balance != "≈$22.50" || !again.Estimate.At.After(m.Estimate.At) {
		t.Fatalf("typed again: %+v %+v", again, again.Estimate)
	}
	// off: no card, and nothing to take off is no error
	if err := SetKeyOwnBalance("plain", "Main", nil); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyOwnBalance("plain", "Main", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := ownCards(t)["plain|Main"]; ok {
		t.Fatal("Main's card after its balance was taken off")
	}
	// the key's value replaced: its balance goes with it
	if _, _, err := ReplaceKey("plain", spare, "sk-spare-2"); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("plain")
	if b, ok := p.KeyOwnBalance(keyID("sk-spare-2")); !ok || b.USD != 1 {
		t.Fatalf("the replaced key's balance %+v %v", b, ok)
	}
	// refused: a key the provider hasn't, an amount out of bounds
	if err := SetKeyOwnBalance("plain", "nobody", one); err == nil {
		t.Error("a key the provider hasn't got a balance")
	}
	for _, bad := range []float64{-MaxOwnBalance - 1, MaxOwnBalance + 1} {
		if err := SetKeyOwnBalance("plain", "Spare", &OwnBalance{USD: bad}); err == nil {
			t.Errorf("%v was taken", bad)
		}
	}
	// a key the vendor let run past its balance is typed as it is: below 0
	for _, ok := range []float64{-1.84, -MaxOwnBalance} {
		if err := SetKeyOwnBalance("plain", "Spare", &OwnBalance{USD: ok}); err != nil {
			t.Errorf("%v refused: %v", ok, err)
		}
	}
}

// A balance the vendor tells always wins: with a Balance URL the card is
// the vendor's, and the balance typed before stays saved as the user left
// it. A relay whose query is looked for and found none shows the typed one.
func TestToldBalanceWinsOverTyped(t *testing.T) {
	probeSandbox(t)
	spentStub(t, map[string]float64{keyID("sk-told"): 4})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/balance" {
			w.Write([]byte(`{"left":2}`))
			return
		}
		w.Write([]byte(`<html>ok</html>`)) // no relay's query answers here
	}))
	defer srv.Close()
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	typed := map[string]OwnBalance{keyID("sk-told"): {USD: 50, At: at}}
	if err := Save(Provider{ID: "told", Name: "Told", Chat: srv.URL + "/v1", Key: "sk-told", KeyOwnBalances: typed,
		BalanceURL: srv.URL + "/balance", BalancePath: "$left"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "probed", Name: "Probed", Chat: srv.URL + "/v1", Key: "sk-told", KeyOwnBalances: typed}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("told")
	if p.TakesOwnBalance() {
		t.Error("a provider with a Balance URL takes a balance typed")
	}
	c := ownCards(t)
	if q := c["told|"]; q.Balance != "$2.00" || q.Estimate != nil {
		t.Errorf("the vendor's balance %+v, want $2.00 and no estimate", q)
	}
	if q := c["probed|"]; q.Balance != "≈$46.00" || q.Estimate == nil {
		t.Errorf("a relay with no balance found: %+v, want the typed ≈$46.00", q)
	}
	p, _ = Find("told")
	if b, ok := p.KeyOwnBalance(keyID("sk-told")); !ok || b.USD != 50 || !b.At.Equal(at) {
		t.Errorf("the typed balance was changed: %+v %v", b, ok)
	}
}

// What the CLI and a hand-edited providers.json may hold.
func TestOwnBalanceParsed(t *testing.T) {
	for in, want := range map[string]float64{"20": 20, "$12.5": 12.5, " 1,234.50 ": 1234.5, "0": 0} {
		if got, err := ParseOwnBalance(in); err != nil || got == nil || got.USD != want || got.CNY != nil {
			t.Errorf("%q: %v %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"off", "none", "-"} {
		if got, err := ParseOwnBalance(in); err != nil || got != nil {
			t.Errorf("%q: %v %v, want none", in, got, err)
		}
	}
	for _, in := range []string{"lots", "--5", "-1000001", "1000001", "NaN"} {
		if _, err := ParseOwnBalance(in); err == nil {
			t.Errorf("%q was taken", in)
		}
	}
	n := normalKeyOwnBalances(map[string]OwnBalance{"ABC": {USD: 5}, "bad": {USD: -2e6}, " ": {USD: 1}})
	if len(n) != 1 || n["abc"].USD != 5 {
		t.Errorf("normalised %+v", n)
	}
	if got := ownBalanceText(-0.001, ""); got != "≈$0.00" {
		t.Errorf("a hair below nothing reads %q", got)
	}
	if !strings.HasPrefix(ownBalanceText(3, "cny"), "≈¥") {
		t.Error("an estimate not marked")
	}
}

// rateStub holds the dollar's rate in yuan magpie knows: ok false is
// none ever learned.
func rateStub(t *testing.T, rate float64, ok bool) {
	t.Helper()
	old := cnyPerUSD
	cnyPerUSD = func() (float64, bool) { return rate, ok }
	t.Cleanup(func() { cnyPerUSD = old })
}

// clockStub holds the clock a balance typed is counted by.
func clockStub(t *testing.T, now time.Time) {
	t.Helper()
	old := ownNow
	ownNow = func() time.Time { return now }
	t.Cleanup(func() { ownNow = old })
}

// A balance typed in yuan (tenacious_eagle_96548 on Discord) is counted
// down by the spend, priced in USD, at the dollar's rate magpie last
// learned, and shown in yuan. While no rate was ever learned, what is
// left with spend counted is unknown: never the spend at a rate of 0, 1
// or a guess. With nothing spent yet, it needs no rate.
func TestTypedBalanceInYuan(t *testing.T) {
	probeSandbox(t)
	probeLocal = false
	spentStub(t, map[string]float64{keyID("sk-cny"): 2})
	if err := Save(Provider{ID: "cnrelay", Name: "CN Relay", Chat: "http://127.0.0.1:1/v1", Key: "sk-cny",
		Keys: []KeyAccount{{Name: "Fresh", Key: "sk-fresh"}}}); err != nil {
		t.Fatal(err)
	}
	b, err := ParseOwnBalance("¥100")
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{keyID("sk-cny"), "Fresh"} {
		if err := SetKeyOwnBalance("cnrelay", ref, b); err != nil {
			t.Fatal(err)
		}
	}
	rateStub(t, 7.1, true)
	var q SubscriptionQuota
	for _, v := range ownCards(t) {
		if v.Estimate.Spent == 2 {
			q = v
		} else if v.Balance != "≈¥100.00" {
			t.Errorf("Fresh, nothing spent: %q", v.Balance)
		}
	}
	if q.Estimate == nil {
		t.Fatal("no card for the key spent on")
	}
	if q.Balance != "≈¥85.80" || q.Estimate.Currency != "cny" || q.Estimate.Rate != 7.1 || q.Estimate.Set != 100 {
		t.Fatalf("¥100 less $2 at 7.1: %+v %+v, want ≈¥85.80", q, q.Estimate)
	}
	if n, ok := BalanceNumber(q.Balance); !ok || n != 85.8 {
		t.Errorf("a low-balance alert reads %v %v from %q", n, ok, q.Balance)
	}

	rateStub(t, 0, false)
	for _, q := range ownCards(t) {
		e := q.Estimate
		switch {
		case e.Spent == 2 && (q.Balance != "" || e.Left != nil || q.Error == ""):
			t.Errorf("spend counted at no rate: %+v %+v, want unknown", q, e)
		case e.Spent == 0 && q.Balance != "≈¥100.00":
			t.Errorf("nothing spent needs no rate: %q", q.Balance)
		}
	}
	p, _ := Find("cnrelay")
	if got, ok := p.KeyOwnBalance(keyID("sk-cny")); !ok || got.CNY == nil || *got.CNY != 100 || got.USD != 0 {
		t.Fatalf("kept as %+v", got)
	}
}

// A balance that refills (tenacious_eagle_96548: $10 a month from the
// 1st) is whole again at midnight local time on its day, and the spend
// is counted from the last refill, or from when it was typed when that
// came after. Held at a clock in UTC+14 and UTC-10, so the local day,
// not UTC's, is the one that counts.
func TestTypedBalanceRefills(t *testing.T) {
	probeSandbox(t)
	probeLocal = false
	asked := spentStub(t, map[string]float64{keyID("sk-r"): 3})
	if err := Save(Provider{ID: "monthly", Name: "Monthly", Chat: "http://127.0.0.1:1/v1", Key: "sk-r"}); err != nil {
		t.Fatal(err)
	}
	for _, zone := range []string{"Pacific/Kiritimati", "Pacific/Honolulu", "UTC"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		typed := time.Date(2026, 10, 20, 9, 0, 0, 0, loc)
		clockStub(t, typed)
		if err := SetKeyOwnBalance("monthly", keyID("sk-r"), &OwnBalance{USD: 10, Refill: &Refill{Every: RefillMonth, Day: 1}}); err != nil {
			t.Fatal(err)
		}
		// before the 1st: counted from when it was typed
		clockStub(t, time.Date(2026, 10, 31, 23, 59, 0, 0, loc))
		p, _ := Find("monthly")
		e, _ := p.OwnBalanceEstimate(keyID("sk-r"))
		if !e.From.Equal(typed) || *e.Left != 7 || !e.Next.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, loc)) {
			t.Fatalf("%s before the refill: from %v next %v left %v", zone, e.From, e.Next, *e.Left)
		}
		// past midnight on the 1st, local time: whole again
		clockStub(t, time.Date(2026, 11, 8, 10, 0, 0, 0, loc))
		q := ownCards(t)["monthly|"]
		nov1 := time.Date(2026, 11, 1, 0, 0, 0, 0, loc)
		if !q.Estimate.From.Equal(nov1) || !asked[keyID("sk-r")].Equal(nov1) || q.Balance != "≈$7.00" {
			t.Fatalf("%s after the refill: %+v from %v, asked from %v", zone, q, q.Estimate.From, asked[keyID("sk-r")])
		}
		if !q.Estimate.Next.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, loc)) {
			t.Errorf("%s next refill %v", zone, q.Estimate.Next)
		}
	}

	// typed again: the refill stays; taken off: counted from when typed
	clockStub(t, time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC))
	if err := SetKeyOwnBalance("monthly", keyID("sk-r"), &OwnBalance{USD: 12}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("monthly")
	if b, _ := p.KeyOwnBalance(keyID("sk-r")); b.Refill == nil || b.Refill.Day != 1 {
		t.Fatalf("typed again, the refill went: %+v", b)
	}
	if err := SetKeyBalanceRefill("monthly", keyID("sk-r"), &Refill{Every: RefillWeek, Day: 5}); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("monthly")
	if b, _ := p.KeyOwnBalance(keyID("sk-r")); b.USD != 12 || b.Refill.Every != RefillWeek || !b.At.Equal(time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("a refill changed the amount or when it was typed: %+v", b)
	}
	if err := SetKeyBalanceRefill("monthly", keyID("sk-r"), nil); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("monthly")
	if e, _ := p.OwnBalanceEstimate(keyID("sk-r")); e.Refill != nil || e.Next != nil {
		t.Fatalf("no refill: %+v", e)
	}
	// refused: a refill that isn't one; one for a key with no balance
	for _, bad := range []Refill{{RefillMonth, 0}, {RefillMonth, 32}, {RefillWeek, 8}, {"year", 1}} {
		if err := SetKeyBalanceRefill("monthly", keyID("sk-r"), &bad); err == nil {
			t.Errorf("%+v taken", bad)
		}
	}
	if err := Save(Provider{ID: "keyless", Name: "Keyless", Chat: "http://127.0.0.1:1/v1", Key: "sk-none"}); err != nil {
		t.Fatal(err)
	}
	if err := SetKeyBalanceRefill("keyless", keyID("sk-none"), &Refill{RefillMonth, 1}); err == nil {
		t.Error("a refill for a key with no balance typed")
	}
}

// When each refill falls: on the day, at local midnight; a month without
// the day refills on its last; a week from Monday (1) to Sunday (7).
func TestRefillDays(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	at := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, loc) }
	for _, c := range []struct {
		r          Refill
		now        time.Time
		last, next time.Time
	}{
		{Refill{RefillMonth, 1}, at(2026, 11, 8, 10), at(2026, 11, 1, 0), at(2026, 12, 1, 0)},
		{Refill{RefillMonth, 1}, at(2026, 11, 1, 0), at(2026, 11, 1, 0), at(2026, 12, 1, 0)},
		{Refill{RefillMonth, 15}, at(2026, 11, 8, 10), at(2026, 10, 15, 0), at(2026, 11, 15, 0)},
		{Refill{RefillMonth, 31}, at(2027, 3, 5, 0), at(2027, 2, 28, 0), at(2027, 3, 31, 0)},
		{Refill{RefillMonth, 31}, at(2028, 3, 5, 0), at(2028, 2, 29, 0), at(2028, 3, 31, 0)},
		{Refill{RefillMonth, 31}, at(2026, 12, 31, 12), at(2026, 12, 31, 0), at(2027, 1, 31, 0)},
		{Refill{RefillMonth, 5}, at(2027, 1, 3, 0), at(2026, 12, 5, 0), at(2027, 1, 5, 0)},
		// 2026-11-08 is a Sunday
		{Refill{RefillWeek, 1}, at(2026, 11, 8, 10), at(2026, 11, 2, 0), at(2026, 11, 9, 0)},
		{Refill{RefillWeek, 7}, at(2026, 11, 8, 10), at(2026, 11, 8, 0), at(2026, 11, 15, 0)},
		{Refill{RefillWeek, 5}, at(2026, 11, 8, 10), at(2026, 11, 6, 0), at(2026, 11, 13, 0)},
	} {
		if l, n := c.r.Last(c.now), c.r.Next(c.now); !l.Equal(c.last) || !n.Equal(c.next) {
			t.Errorf("%+v at %v: last %v next %v, want %v and %v", c.r, c.now, l, n, c.last, c.next)
		}
	}
}

// What the CLI takes for an amount in yuan and for a refill.
func TestOwnBalanceParsedInYuanAndRefill(t *testing.T) {
	for _, in := range []string{"¥100", "￥100", "100cny", "100 CNY", "100 rmb", "100元", "cny 100", "¥1,00"} {
		b, err := ParseOwnBalance(in)
		if err != nil || b == nil || b.CNY == nil || *b.CNY != 100 || b.USD != 0 {
			t.Errorf("%q: %+v %v, want ¥100", in, b, err)
		}
	}
	if b, err := ParseOwnBalance("12 usd"); err != nil || b.CNY != nil || b.USD != 12 {
		t.Errorf("12 usd: %+v %v", b, err)
	}
	// overdrawn: below 0, the sign before or after the currency's
	for in, want := range map[string]float64{"-1.84": -1.84, "-$1.84": -1.84, "$-1.84": -1.84, "-1.84 usd": -1.84} {
		if b, err := ParseOwnBalance(in); err != nil || b == nil || b.CNY != nil || b.USD != want {
			t.Errorf("%q: %+v %v, want $%v", in, b, err, want)
		}
	}
	for _, in := range []string{"-¥5", "¥-5", "-5元", "-5 cny"} {
		if b, err := ParseOwnBalance(in); err != nil || b == nil || b.CNY == nil || *b.CNY != -5 {
			t.Errorf("%q: %+v %v, want ¥-5", in, b, err)
		}
	}
	for _, in := range []string{"¥--1", "¥lots", "¥", "¥2000000", "¥-2000000", "-$-5"} {
		if _, err := ParseOwnBalance(in); err == nil {
			t.Errorf("%q taken", in)
		}
	}
	for in, want := range map[string]Refill{"monthly": {RefillMonth, 1}, "monthly:15": {RefillMonth, 15}, "weekly": {RefillWeek, 1},
		"weekly:fri": {RefillWeek, 5}, "weekly:Sunday": {RefillWeek, 7}, "weekly:3": {RefillWeek, 3}} {
		if r, err := ParseRefill(in); err != nil || r == nil || *r != want {
			t.Errorf("%q: %+v %v, want %+v", in, r, err, want)
		}
		if r, _ := ParseRefill(want.String()); r == nil || *r != want {
			t.Errorf("%+v as %q reads back %+v", want, want.String(), r)
		}
	}
	if r, err := ParseRefill("off"); err != nil || r != nil {
		t.Errorf("off: %+v %v", r, err)
	}
	for _, in := range []string{"monthly:0", "monthly:32", "monthly:x", "weekly:x", "yearly", "--help"} {
		if _, err := ParseRefill(in); err == nil {
			t.Errorf("%q taken", in)
		}
	}
	n := normalKeyOwnBalances(map[string]OwnBalance{"a": {USD: 5, Refill: &Refill{"year", 1}}})
	if n["a"].USD != 5 || n["a"].Refill != nil {
		t.Errorf("a refill that isn't one, written by hand: %+v", n)
	}
}

// spendAt stubs usage.KeySpent with calls at given times: what the key
// spent from start up to the clock ownNow holds.
func spendAt(t *testing.T, calls map[time.Time]float64) {
	t.Helper()
	old := KeySpent
	KeySpent = func(_ string, start time.Time) float64 {
		n, sum := ownNow(), 0.0
		for at, usd := range calls {
			if !at.Before(start) && !at.After(n) {
				sum += usd
			}
		}
		return sum
	}
	t.Cleanup(func() { KeySpent = old })
}

// A balance with a part that expires (tenacious_eagle_96548: a vendor's
// money that stays and credit good until a date): the part is spent
// first, as DeepSeek spends granted credit before money topped up; it
// counts through its last day to local midnight and then no more, what
// it took before then never charged to the rest. Held in UTC+14, UTC-10
// and UTC, so the local day ends it, not UTC's.
func TestTypedBalanceExpires(t *testing.T) {
	probeSandbox(t)
	probeLocal = false
	rateStub(t, 7, true)
	if err := Save(Provider{ID: "gift", Name: "Gift", Chat: "http://127.0.0.1:1/v1", Key: "sk-g"}); err != nil {
		t.Fatal(err)
	}
	k := keyID("sk-g")
	for _, zone := range []string{"Pacific/Kiritimati", "Pacific/Honolulu", "UTC"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		at := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, loc) }
		spendAt(t, map[time.Time]float64{at(2, 10, 0): 2, at(5, 10, 0): 4, at(12, 10, 0): 1})
		clockStub(t, at(1, 9, 0))
		if err := SetKeyOwnBalance("gift", k, &OwnBalance{USD: 10}); err != nil {
			t.Fatal(err)
		}
		if err := ClearKeyExpiring("gift", k); err != nil {
			t.Fatal(err)
		}
		if err := AddKeyExpiring("gift", k, Expiring{Amount: 5, Until: "2026-11-10"}); err != nil {
			t.Fatal(err)
		}
		left := func(when time.Time) BalanceEstimate {
			t.Helper()
			clockStub(t, when)
			p, _ := Find("gift")
			e, _ := p.OwnBalanceEstimate(k)
			return e
		}
		// $2 spent: all of it from the part that expires
		e := left(at(3, 12, 0))
		if *e.Left != 13 || len(e.Expiring) != 1 || *e.Expiring[0].Left != 3 || !e.Expiring[0].Ends.Equal(at(11, 0, 0)) || e.Expired != nil {
			t.Fatalf("%s, $2 spent: left %v, expiring %+v", zone, *e.Left, e.Expiring)
		}
		q := ownCards(t)["gift|"]
		if q.Balance != "≈$13.00" || len(q.BalanceSplit) != 1 || q.BalanceSplit[0].Text != "≈$3.00" || !q.BalanceSplit[0].Expires.Equal(at(11, 0, 0)) {
			t.Fatalf("%s card: %+v", zone, q)
		}
		// $6 spent: $5 of it the part's, $1 the rest's; nothing of the part
		// left, so the card has no line for it
		e = left(at(10, 23, 59))
		if *e.Left != 9 || len(e.Expiring) != 1 || *e.Expiring[0].Left != 0 {
			t.Fatalf("%s, $6 spent before the end: left %v, expiring %+v", zone, *e.Left, e.Expiring)
		}
		if q := ownCards(t)["gift|"]; q.Balance != "≈$9.00" || len(q.BalanceSplit) != 0 {
			t.Fatalf("%s card with the part spent: %+v", zone, q)
		}
		// past midnight local: expired, the $5 it took still not the rest's
		e = left(at(13, 0, 0))
		if *e.Left != 8 || e.Expiring != nil || e.Expired == nil || !e.Expired.Equal(at(11, 0, 0)) {
			t.Fatalf("%s after it expired, $1 more: left %v, expired %v", zone, *e.Left, e.Expired)
		}
	}

	// unspent credit that expires counts no more after its day; spend
	// after it is the rest's alone
	loc := time.UTC
	at := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, loc) }
	spendAt(t, map[time.Time]float64{at(20, 1): 3})
	clockStub(t, at(15, 9))
	if err := SetKeyOwnBalance("gift", k, &OwnBalance{USD: 10}); err != nil {
		t.Fatal(err)
	}
	if err := AddKeyExpiring("gift", k, Expiring{Amount: 50, Until: "2026-11-16"}); err != nil {
		t.Fatal(err)
	}
	clockStub(t, at(21, 0))
	p, _ := Find("gift")
	if e, _ := p.OwnBalanceEstimate(k); *e.Left != 7 || e.Expiring != nil {
		t.Fatalf("$50 expired unspent, $3 after: %+v", e)
	}

	// typed again: the part stays in the same currency, and the spend it
	// took after the retype isn't the rest's; in the other currency it
	// would mean another sum, so it goes
	spendAt(t, map[time.Time]float64{at(22, 1): 1, at(23, 1): 2})
	clockStub(t, at(21, 12))
	if err := AddKeyExpiring("gift", k, Expiring{Amount: 2, Until: "2026-12-31"}); err != nil {
		t.Fatal(err)
	}
	clockStub(t, at(22, 12))
	if err := SetKeyOwnBalance("gift", k, &OwnBalance{USD: 20}); err != nil {
		t.Fatal(err)
	}
	clockStub(t, at(24, 0))
	p, _ = Find("gift")
	// the part took $1 before the retype and $1 after; the rest pays $1
	if e, _ := p.OwnBalanceEstimate(k); len(e.Expiring) != 1 || *e.Expiring[0].Left != 0 || *e.Left != 19 {
		t.Fatalf("typed again in USD: left %v, expiring %+v", *e.Left, e.Expiring)
	}
	cny := 100.0
	if err := SetKeyOwnBalance("gift", k, &OwnBalance{CNY: &cny}); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("gift")
	if b, _ := p.KeyOwnBalance(k); len(b.Expiring) != 0 {
		t.Fatalf("typed again in yuan, the part in dollars kept: %+v", b)
	}

	// in yuan: the part is in yuan too, the spend at the rate (7)
	clockStub(t, at(24, 0))
	spendAt(t, map[time.Time]float64{at(24, 1): 1})
	if err := AddKeyExpiring("gift", k, Expiring{Amount: 30, Until: "2026-12-31"}); err != nil {
		t.Fatal(err)
	}
	clockStub(t, at(24, 2))
	p, _ = Find("gift")
	if e, _ := p.OwnBalanceEstimate(k); *e.Left != 123 || len(e.Expiring) != 1 || *e.Expiring[0].Left != 23 {
		t.Fatalf("¥100 and ¥30 less $1 at 7: %v, %+v", *e.Left, e.Expiring)
	}
	if q := ownCards(t)["gift|"]; q.Balance != "≈¥123.00" || q.BalanceSplit[0].Text != "≈¥23.00" || SplitWords(q.BalanceSplit) != "≈¥23.00 until Dec 31" {
		t.Fatalf("card in yuan: %+v (%q)", q, SplitWords(q.BalanceSplit))
	}
	rateStub(t, 0, false)
	if q := ownCards(t)["gift|"]; q.Balance != "" || q.Error == "" || len(q.BalanceSplit) != 0 {
		t.Fatalf("spend at no rate is unknown, the part too: %+v", q)
	}

	// refused: none for a key with no balance, an amount or date that
	// isn't one; a part written by hand that isn't one is dropped
	if err := Save(Provider{ID: "bare", Name: "Bare", Chat: "http://127.0.0.1:1/v1", Key: "sk-b"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKeyExpiring("bare", keyID("sk-b"), Expiring{Amount: 5, Until: "2026-12-31"}); err == nil {
		t.Error("credit that expires on a key with no balance")
	}
	for _, bad := range []Expiring{{Amount: 0, Until: "2026-12-31"}, {Amount: -1, Until: "2026-12-31"}, {Amount: 2e6, Until: "2026-12-31"}, {Amount: 5, Until: "12/31"}, {Amount: 5}} {
		if err := AddKeyExpiring("gift", k, bad); err == nil {
			t.Errorf("%+v taken", bad)
		}
	}
	n := normalKeyOwnBalances(map[string]OwnBalance{"a": {USD: 5, Expiring: []Expiring{{Amount: 5, Until: "soon"}, {Amount: 3, Until: "2026-12-31"}}}})
	if n["a"].USD != 5 || len(n["a"].Expiring) != 1 || n["a"].Expiring[0].Amount != 3 {
		t.Errorf("a part that isn't one, written by hand: %+v", n)
	}
}

// A balance saved before parts that expire is read and counted as it was.
func TestTypedBalanceSavedBeforeExpiring(t *testing.T) {
	var b OwnBalance
	if err := json.Unmarshal([]byte(`{"cny":100,"at":"2026-11-01T01:00:00Z","refill":{"every":"month","day":1}}`), &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Expiring) != 0 || *b.CNY != 100 || b.Refill.Day != 1 {
		t.Fatalf("%+v", b)
	}
	out, _ := json.Marshal(b)
	if strings.Contains(string(out), "expiring") || strings.Contains(string(out), "expirings") {
		t.Errorf("saved again with a part it never had: %s", out)
	}
}

// What the CLI takes for credit that expires.
func TestExpiringParsed(t *testing.T) {
	for in, want := range map[string]Expiring{"20@2026-12-31": {Amount: 20, Until: "2026-12-31"}, " 1,000.5 @ 2027-01-02 ": {Amount: 1000.5, Until: "2027-01-02"}} {
		if x, err := ParseExpiring(in); err != nil || x == nil || *x != want {
			t.Errorf("%q: %+v %v", in, x, err)
		}
	}
	if x, err := ParseExpiring("off"); err != nil || x != nil {
		t.Errorf("off: %+v %v", x, err)
	}
	for _, in := range []string{"20", "@2026-12-31", "20@", "20@2026-13-01", "¥20@2026-12-31", "0@2026-12-31", "--help"} {
		if _, err := ParseExpiring(in); err == nil {
			t.Errorf("%q taken", in)
		}
	}
}

// Two grants of credit that expires on one key (tenacious_eagle_96548:
// promotional credit going tomorrow and the day after): the spend goes to
// the one that ends soonest, whichever was typed first, never to one
// typed after it; each counts to its own midnight, and what an expired
// grant left unspent never reaches the rest.
func TestTypedBalanceGrantsSoonestFirst(t *testing.T) {
	probeSandbox(t)
	probeLocal = false
	if err := Save(Provider{ID: "promo", Name: "Promo", Chat: "http://127.0.0.1:1/v1", Key: "sk-p"}); err != nil {
		t.Fatal(err)
	}
	k := keyID("sk-p")
	for _, zone := range []string{"Pacific/Kiritimati", "UTC"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		at := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, loc) }
		spendAt(t, map[time.Time]float64{at(9, 10): 3, at(10, 10): 4, at(11, 2): 3})
		clockStub(t, at(9, 9))
		if err := SetKeyOwnBalance("promo", k, &OwnBalance{USD: 10}); err != nil {
			t.Fatal(err)
		}
		if err := ClearKeyExpiring("promo", k); err != nil {
			t.Fatal(err)
		}
		// the day after's typed first; tomorrow's two hours later
		if err := AddKeyExpiring("promo", k, Expiring{Amount: 5, Until: "2026-11-11"}); err != nil {
			t.Fatal(err)
		}
		clockStub(t, at(9, 11))
		if err := AddKeyExpiring("promo", k, Expiring{Amount: 5, Until: "2026-11-10"}); err != nil {
			t.Fatal(err)
		}
		left := func(when time.Time) BalanceEstimate {
			t.Helper()
			clockStub(t, when)
			p, _ := Find("promo")
			e, _ := p.OwnBalanceEstimate(k)
			return e
		}
		// $3 at 10:00 went to the day after's, the only one then; $4 the
		// next day to tomorrow's, which ends first
		e := left(at(10, 12))
		if *e.Left != 13 || len(e.Expiring) != 2 || e.Expiring[0].Until != "2026-11-10" || *e.Expiring[0].Left != 1 ||
			e.Expiring[1].Until != "2026-11-11" || *e.Expiring[1].Left != 2 || e.Expired != nil {
			t.Fatalf("%s: left %v, grants %+v", zone, *e.Left, e.Expiring)
		}
		q := ownCards(t)["promo|"]
		if q.Balance != "≈$13.00" || len(q.BalanceSplit) != 2 || q.BalanceSplit[0].Text != "≈$1.00" || !q.BalanceSplit[0].Expires.Equal(at(11, 0)) ||
			q.BalanceSplit[1].Text != "≈$2.00" || !q.BalanceSplit[1].Expires.Equal(at(12, 0)) {
			t.Fatalf("%s card: %+v", zone, q)
		}
		// tomorrow's gone with $1 unspent; the $3 after it: $2 the day
		// after's, $1 the rest's
		e = left(at(11, 3))
		if *e.Left != 9 || len(e.Expiring) != 1 || *e.Expiring[0].Left != 0 || e.Expired == nil || !e.Expired.Equal(at(11, 0)) {
			t.Fatalf("%s, tomorrow's gone: left %v, grants %+v, expired %v", zone, *e.Left, e.Expiring, e.Expired)
		}
		e = left(at(12, 1))
		if *e.Left != 9 || len(e.Expiring) != 0 || e.Expired == nil || !e.Expired.Equal(at(12, 0)) {
			t.Fatalf("%s, both gone: left %v, grants %+v, expired %v", zone, *e.Left, e.Expiring, e.Expired)
		}
	}
}

// Grants are added and taken off one at a time, by when each was typed
// or by its day; off takes them all; a key holds MaxExpiring; one that
// ended before the balance was typed again goes when the key is next
// written.
func TestTypedBalanceGrantsEdited(t *testing.T) {
	probeSandbox(t)
	probeLocal = false
	spendAt(t, nil)
	if err := Save(Provider{ID: "promo", Name: "Promo", Chat: "http://127.0.0.1:1/v1", Key: "sk-p"}); err != nil {
		t.Fatal(err)
	}
	k := keyID("sk-p")
	at := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, time.UTC) }
	clockStub(t, at(9, 9))
	if err := SetKeyOwnBalance("promo", k, &OwnBalance{USD: 0}); err != nil {
		t.Fatal(err)
	}
	grants := func() []Expiring {
		t.Helper()
		p, _ := Find("promo")
		b, _ := p.KeyOwnBalance(k)
		return b.Expiring
	}
	for i, x := range []Expiring{{Amount: 5, Until: "2026-11-10"}, {Amount: 7, Until: "2026-11-11"}, {Amount: 9, Until: "2026-11-11"}} {
		clockStub(t, at(9, 10+i))
		if err := AddKeyExpiring("promo", k, x); err != nil {
			t.Fatal(err)
		}
	}
	if g := grants(); len(g) != 3 || !g[1].At.Equal(at(9, 11)) {
		t.Fatalf("three added: %+v", g)
	}
	p, _ := Find("promo")
	if e, _ := p.OwnBalanceEstimate(k); *e.Left != 21 {
		t.Fatalf("0 and 5+7+9: %v", *e.Left)
	}
	// by when it was typed: only that one
	if err := RemoveKeyExpiring("promo", k, Expiring{At: at(9, 11)}); err != nil {
		t.Fatal(err)
	}
	if g := grants(); len(g) != 2 || g[0].Amount != 5 || g[1].Amount != 9 {
		t.Fatalf("the 7 taken off: %+v", g)
	}
	// by day and amount; one that matches nothing is refused and changes nothing
	if err := RemoveKeyExpiring("promo", k, Expiring{Amount: 1, Until: "2026-11-11"}); err == nil {
		t.Error("taking off a grant there isn't")
	}
	if err := RemoveKeyExpiring("promo", k, Expiring{At: at(1, 1)}); err == nil {
		t.Error("taking off a grant typed at no such time")
	}
	if err := RemoveKeyExpiring("promo", k, Expiring{Amount: 9, Until: "2026-11-11"}); err != nil {
		t.Fatal(err)
	}
	if g := grants(); len(g) != 1 || g[0].Amount != 5 {
		t.Fatalf("the 9 taken off: %+v", g)
	}
	// MaxExpiring and no more
	for len(grants()) < MaxExpiring {
		if err := AddKeyExpiring("promo", k, Expiring{Amount: 1, Until: "2026-12-31"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := AddKeyExpiring("promo", k, Expiring{Amount: 1, Until: "2026-12-31"}); err == nil {
		t.Errorf("grant %d taken", MaxExpiring+1)
	}
	// by day: every one through it
	if err := RemoveKeyExpiring("promo", k, Expiring{Until: "2026-12-31"}); err != nil {
		t.Fatal(err)
	}
	if g := grants(); len(g) != 1 || g[0].Amount != 5 {
		t.Fatalf("2026-12-31's taken off: %+v", g)
	}
	// typed again after the 5's day: it took nothing the new balance
	// pays for, so it goes
	clockStub(t, at(12, 9))
	if err := SetKeyOwnBalance("promo", k, &OwnBalance{USD: 4}); err != nil {
		t.Fatal(err)
	}
	if g := grants(); len(g) != 0 {
		t.Fatalf("a grant over before the balance was typed kept: %+v", g)
	}
	if err := AddKeyExpiring("promo", k, Expiring{Amount: 2, Until: "2026-12-01"}); err != nil {
		t.Fatal(err)
	}
	if err := AddKeyExpiring("promo", k, Expiring{Amount: 3, Until: "2026-12-02"}); err != nil {
		t.Fatal(err)
	}
	if err := ClearKeyExpiring("promo", k); err != nil {
		t.Fatal(err)
	}
	p, _ = Find("promo")
	if b, _ := p.KeyOwnBalance(k); len(b.Expiring) != 0 || b.USD != 4 {
		t.Fatalf("off: %+v", b)
	}
}

// A v0.1.3305 balance's one grant (`expiring`, an object) is read as the
// first of its grants and saved in the list (`expirings`), counted as it
// was; a balance carrying both keeps each grant once.
func TestTypedBalanceOneGrantMigrated(t *testing.T) {
	var b OwnBalance
	old := `{"usd":10,"at":"2026-11-01T01:00:00Z","expiring":{"amount":5,"until":"2026-11-10","at":"2026-11-01T02:00:00Z"}}`
	if err := json.Unmarshal([]byte(old), &b); err != nil {
		t.Fatal(err)
	}
	want := Expiring{Amount: 5, Until: "2026-11-10", At: time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)}
	if b.USD != 10 || len(b.Expiring) != 1 || !b.Expiring[0].At.Equal(want.At) || b.Expiring[0].Amount != 5 || b.Expiring[0].Until != want.Until {
		t.Fatalf("%+v", b)
	}
	out, _ := json.Marshal(b)
	if !strings.Contains(string(out), `"expirings":[{"amount":5,"until":"2026-11-10"`) || strings.Contains(string(out), `"expiring":`) {
		t.Fatalf("saved again: %s", out)
	}
	var again OwnBalance
	if err := json.Unmarshal(out, &again); err != nil || len(again.Expiring) != 1 {
		t.Fatalf("read again: %+v %v", again, err)
	}
	both := `{"usd":10,"at":"2026-11-01T01:00:00Z","expiring":{"amount":5,"until":"2026-11-10","at":"2026-11-01T02:00:00Z"},` +
		`"expirings":[{"amount":5,"until":"2026-11-10","at":"2026-11-01T02:00:00Z"},{"amount":3,"until":"2026-11-11","at":"2026-11-01T03:00:00Z"}]}`
	if err := json.Unmarshal([]byte(both), &b); err != nil || len(b.Expiring) != 2 || b.Expiring[1].Amount != 3 {
		t.Fatalf("both: %+v %v", b, err)
	}

	// counted as v0.1.3305 counted it: $2 spent of 10 and 5 that expires
	probeSandbox(t)
	probeLocal = false
	at := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, time.UTC) }
	spendAt(t, map[time.Time]float64{at(2, 10): 2})
	clockStub(t, at(3, 9))
	if err := json.Unmarshal([]byte(old), &b); err != nil {
		t.Fatal(err)
	}
	if err := Save(Provider{ID: "old", Name: "Old", Chat: "http://127.0.0.1:1/v1", Key: "sk-o", KeyOwnBalances: map[string]OwnBalance{keyID("sk-o"): b}}); err != nil {
		t.Fatal(err)
	}
	p, _ := Find("old")
	if e, _ := p.OwnBalanceEstimate(keyID("sk-o")); *e.Left != 13 || len(e.Expiring) != 1 || *e.Expiring[0].Left != 3 {
		t.Fatalf("migrated: %+v", e)
	}
}

// What the CLI takes for credit to take off.
func TestExpiringDayParsed(t *testing.T) {
	if x, err := ParseExpiringDay("2026-12-31"); err != nil || *x != (Expiring{Until: "2026-12-31"}) {
		t.Errorf("a day: %+v %v", x, err)
	}
	if x, err := ParseExpiringDay("20@2026-12-31"); err != nil || *x != (Expiring{Amount: 20, Until: "2026-12-31"}) {
		t.Errorf("amount@day: %+v %v", x, err)
	}
	for _, in := range []string{"", "off", "20", "12/31", "20@", "--help"} {
		if _, err := ParseExpiringDay(in); err == nil {
			t.Errorf("%q taken", in)
		}
	}
}

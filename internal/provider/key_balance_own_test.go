package provider

import (
	"context"
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
	twenty, one := 20.0, 1.0
	before := time.Now().UTC()
	for _, s := range []struct {
		ref string
		usd *float64
	}{{"Main", &twenty}, {spare, &one}, {"Off", &one}} {
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
	thirty := 30.0
	if err := SetKeyOwnBalance("plain", "Main", &thirty); err != nil {
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
	if err := SetKeyOwnBalance("plain", "nobody", &one); err == nil {
		t.Error("a key the provider hasn't got a balance")
	}
	for _, bad := range []float64{-1, MaxOwnBalance + 1} {
		if err := SetKeyOwnBalance("plain", "Spare", &bad); err == nil {
			t.Errorf("%v was taken", bad)
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
		if got, err := ParseOwnBalance(in); err != nil || got == nil || *got != want {
			t.Errorf("%q: %v %v, want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"off", "none", "-"} {
		if got, err := ParseOwnBalance(in); err != nil || got != nil {
			t.Errorf("%q: %v %v, want none", in, got, err)
		}
	}
	for _, in := range []string{"lots", "-5", "1000001", "NaN"} {
		if _, err := ParseOwnBalance(in); err == nil {
			t.Errorf("%q was taken", in)
		}
	}
	n := normalKeyOwnBalances(map[string]OwnBalance{"ABC": {USD: 5}, "bad": {USD: -1}, " ": {USD: 1}})
	if len(n) != 1 || n["abc"].USD != 5 {
		t.Errorf("normalised %+v", n)
	}
	if got := ownBalanceText(-0.001); got != "≈$0.00" {
		t.Errorf("a hair below nothing reads %q", got)
	}
	if !strings.HasPrefix(ownBalanceText(3), "≈") {
		t.Error("an estimate not marked")
	}
}

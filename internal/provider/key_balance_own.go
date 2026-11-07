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

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// OwnBalance is a key's balance as the user typed it: USD left at At.
type OwnBalance struct {
	USD float64   `json:"usd"`
	At  time.Time `json:"at"`
}

// MaxOwnBalance bounds a balance typed by hand, in USD.
const MaxOwnBalance = 1_000_000

// BalanceEstimate is what a card shows of a balance typed by hand: the
// amount typed, when, and what magpie counted spent on the key since.
type BalanceEstimate struct {
	Set   float64   `json:"set"`
	At    time.Time `json:"at"`
	Spent float64   `json:"spent"`
}

// KeySpent is usage.KeySpent, which package usage sets: what the calls
// magpie sent on the key keyID from start on cost, in USD. nil (a test
// of this package alone) counts nothing.
var KeySpent func(keyID string, start time.Time) float64

// KeyOwnBalance is the balance typed for p's key keyID, and whether it
// has one.
func (p Provider) KeyOwnBalance(keyID string) (OwnBalance, bool) {
	b, ok := p.KeyOwnBalances[strings.ToLower(keyID)]
	return b, ok
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
	id := keyID(key)
	b, ok := p.KeyOwnBalance(id)
	if !ok {
		return SubscriptionQuota{}, false
	}
	var spent float64
	if KeySpent != nil {
		spent = KeySpent(id, b.At)
	}
	now := time.Now()
	return SubscriptionQuota{
		Provider: p.ID, Name: p.Name, Icon: p.Icon, User: user, Windows: []QuotaWindow{},
		Balance:  ownBalanceText(b.USD - spent),
		Estimate: &BalanceEstimate{Set: b.USD, At: b.At, Spent: spent},
		ReadAt:   &now,
	}, true
}

// ownBalanceText is an estimated balance as a card shows it, "≈$12.34",
// below nothing as "≈$-1.20": the calls priced since went past it.
func ownBalanceText(usd float64) string {
	if math.Abs(usd) < 0.005 {
		usd = 0
	}
	return fmt.Sprintf("≈$%.2f", usd)
}

// SetKeyOwnBalance gives the key ref (its id or name) of the provider id
// the balance usd, counted from now; nil takes it off.
func SetKeyOwnBalance(id, ref string, usd *float64) error {
	if usd != nil {
		if err := checkOwnBalance(*usd); err != nil {
			return err
		}
	}
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
	if usd == nil {
		if _, had := m[k]; !had {
			return nil
		}
		delete(m, k)
	} else {
		m[k] = OwnBalance{USD: *usd, At: time.Now().UTC()}
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
func checkOwnBalance(usd float64) error {
	if !(usd >= 0 && usd <= MaxOwnBalance) {
		return fmt.Errorf("a balance is an amount in USD from 0 to %d, not %v", MaxOwnBalance, usd)
	}
	return nil
}

// ParseOwnBalance reads a balance as the CLI takes it, an amount in USD
// ("20", "$20", "12.5"); off for none (nil).
func ParseOwnBalance(amount string) (*float64, error) {
	a := strings.ToLower(strings.TrimSpace(amount))
	switch a {
	case "off", "none", "no", "-":
		return nil, nil
	}
	usd, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimPrefix(a, "$"), ",", ""), 64)
	if err != nil {
		return nil, fmt.Errorf("a balance is an amount in USD (20, 12.5) or off, not %q", amount)
	}
	if err := checkOwnBalance(usd); err != nil {
		return nil, err
	}
	return &usd, nil
}

// normalKeyOwnBalances is m as it is kept: key ids in lower case, a
// balance out of bounds left out; nil for none. One written by hand with
// no date counts from the start of the usage log.
func normalKeyOwnBalances(m map[string]OwnBalance) map[string]OwnBalance {
	var out map[string]OwnBalance
	for k, b := range m {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" || checkOwnBalance(b.USD) != nil {
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

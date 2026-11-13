package provider

// A key's own price rate (@tnt12231 on X): a relay sells keys in groups
// that bill at different rates against the official price, 0.5× on one
// and 1.2× on another, under one endpoint. Each key the user gave a rate
// of its own bills at it wherever a call it sent is counted (the Usage
// page, a key's spend cap and its typed balance); every other key bills at
// the provider's PriceRate. A price the user set for a model stands as
// set, as it does under the provider's rate.

import (
	"fmt"
	"strconv"
	"strings"
)

// KeyPriceRate is the price rate the key keyID bills at: its own when the
// user gave it one, else the provider's (0: the price as listed). own
// says which.
func (p Provider) KeyPriceRate(keyID string) (rate float64, own bool) {
	if r, ok := p.KeyPriceRates[strings.ToLower(keyID)]; ok && r > 0 {
		return r, true
	}
	return p.PriceRate, false
}

// atKeyRate is p as the key keyID bills: its price rate the key's own when
// it has one, so pricing p prices that key's calls.
func (p Provider) atKeyRate(keyID string) Provider {
	if keyID == "" {
		return p
	}
	p.PriceRate, _ = p.KeyPriceRate(keyID)
	return p
}

// KeyPriceRateOK says what is wrong with a key's own price rate, "" when
// nothing: above 0 up to 1000, at most three decimals. None is no value:
// the key bills at the provider's rate.
func KeyPriceRateOK(r float64) string {
	if bad := PriceRateOK(r); bad != "" {
		return bad
	}
	if r == 0 {
		return "a key's own price rate is above 0; take it off for the provider's"
	}
	return ""
}

// SetKeyPriceRate gives the key ref (its id or name) of the provider id a
// price rate of its own, or with nil the provider's again.
func SetKeyPriceRate(id, ref string, rate *float64) error {
	if rate != nil {
		if bad := KeyPriceRateOK(*rate); bad != "" {
			return fmt.Errorf("%s, not %v", bad, *rate)
		}
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	if p.Account != nil {
		return fmt.Errorf("%s is a subscription: a price rate of its own is for an API key (the provider's is in its editor)", p.Name)
	}
	r, ok := p.accountRef(ref)
	if !ok {
		return fmt.Errorf("%s has no key %q", p.Name, ref)
	}
	k := strings.ToLower(r)
	m := map[string]float64{}
	for id, v := range p.KeyPriceRates {
		m[id] = v
	}
	if rate == nil {
		if _, had := m[k]; !had {
			return nil
		}
		delete(m, k)
	} else {
		m[k] = *rate
	}
	p.KeyPriceRates = m
	return Save(*p)
}

// ParseKeyPriceRate reads a key's price rate as the CLI takes it: a number
// ("0.8", "1.5x", "×1.2"), or default (nil) for the provider's.
func ParseKeyPriceRate(s string) (*float64, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "default", "provider", "off", "none", "-":
		return nil, nil
	}
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(v, "×"), "x"), "x"))
	r, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return nil, fmt.Errorf("a key's price rate is a number like 0.8 or 1.5, or default for the provider's, not %q", s)
	}
	if bad := KeyPriceRateOK(r); bad != "" {
		return nil, fmt.Errorf("%s, not %v", bad, r)
	}
	return &r, nil
}

// normalKeyPriceRates is m as it is kept: key ids in lower case, rates
// that aren't one left out; nil for none.
func normalKeyPriceRates(m map[string]float64) map[string]float64 {
	var out map[string]float64
	for k, r := range m {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" || KeyPriceRateOK(r) != "" {
			continue
		}
		if out == nil {
			out = map[string]float64{}
		}
		out[k] = r
	}
	return out
}

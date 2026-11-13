package cli

import (
	"fmt"
	"strconv"

	"github.com/yetone/magpie/internal/provider"
)

const keyRateUsage = "magpie provider key-rate <id> [key [rate|default]]"

// keyRateCmd shows, or sets, the price rate each of a provider's API keys
// bills at (@tnt12231 on X: one relay, keys in groups at 0.5× and 1.2×):
// a key's own, or the provider's (provider/key_rate.go).
func keyRateCmd(rest []string) error {
	if len(rest) < 1 || len(rest) > 3 {
		return fmt.Errorf("%s", keyRateUsage)
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if p.Account != nil {
		return fmt.Errorf("%s is a subscription: a price rate of its own is for an API key (the provider's is in its editor)", p.Name)
	}
	if len(rest) > 2 {
		r, err := provider.ParseKeyPriceRate(rest[2])
		if err != nil {
			return err
		}
		if err := provider.SetKeyPriceRate(p.ID, rest[1], r); err != nil {
			return err
		}
		if p, err = provider.Find(p.ID); err != nil {
			return err
		}
	}
	keys := p.KeyList()
	if len(rest) > 1 {
		ref, ok := p.AccountRefOf(rest[1])
		if !ok {
			return fmt.Errorf("%s has no key %q", p.Name, rest[1])
		}
		for _, k := range keys {
			if k.ID == ref {
				keys = []provider.KeyInfo{k}
				break
			}
		}
	}
	if len(keys) == 0 {
		fmt.Println(muted.Render(p.Name + " has no key"))
		return nil
	}
	for _, k := range keys {
		name := k.Masked + " " + muted.Render(k.ID)
		if k.Name != "" {
			name = k.Name + " " + muted.Render(k.Masked+" · "+k.ID)
		}
		r, own := p.KeyPriceRate(k.ID)
		switch {
		case own:
			fmt.Println(name, "· ×"+rateText(r))
		case r > 0:
			fmt.Println(name, muted.Render("· ×"+rateText(r)+", the provider's"))
		default:
			fmt.Println(name, muted.Render("· ×1, the provider's (the official price)"))
		}
	}
	return nil
}

// rateText is a price rate as a line tells it: 0.8, 1.25, 2.
func rateText(r float64) string { return strconv.FormatFloat(r, 'f', -1, 64) }

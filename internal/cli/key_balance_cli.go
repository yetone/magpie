package cli

import (
	"fmt"
	"strings"

	"github.com/yetone/magpie/internal/provider"
)

const keyBalanceUsage = "magpie provider key-balance <id> [key [usd|¥cny|off] [--refill monthly[:day]|weekly[:mon…sun]|off]]"

// keyBalanceCmd shows, or sets, the balance typed by hand on a provider's
// API keys whose vendor tells none (tenacious_eagle on Discord): what is
// left of it is the amount less what magpie priced the key's calls at
// since it was typed, or since it last refilled
// (provider/key_balance_own.go). An amount in yuan ("¥100", "100cny") is
// counted down at the dollar's rate magpie last learned.
func keyBalanceCmd(args []string) error {
	var rest []string
	var refill *string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--refill" && i+1 < len(args):
			i++
			refill = &args[i]
		case strings.HasPrefix(a, "--refill="):
			v := strings.TrimPrefix(a, "--refill=")
			refill = &v
		case strings.HasPrefix(a, "-") && a != "-":
			// a flag magpie doesn't know, or a negative amount: never data
			return fmt.Errorf("%s", keyBalanceUsage)
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) < 1 || len(rest) > 3 || (refill != nil && len(rest) < 2) {
		return fmt.Errorf("%s", keyBalanceUsage)
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if p.Account != nil {
		return fmt.Errorf("%s is a subscription: a balance typed by hand is for an API key", p.Name)
	}
	var r *provider.Refill
	if refill != nil {
		if r, err = provider.ParseRefill(*refill); err != nil {
			return err
		}
	}
	var b *provider.OwnBalance
	if len(rest) > 2 {
		if b, err = provider.ParseOwnBalance(rest[2]); err != nil {
			return err
		}
		if b == nil && refill != nil {
			return fmt.Errorf("%s", keyBalanceUsage)
		}
	}
	switch {
	case b != nil:
		b.Refill = r
		if err := provider.SetKeyOwnBalance(p.ID, rest[1], b); err != nil {
			return err
		}
		// typed again with --refill off: it refills no more
		if refill != nil && r == nil {
			if err := provider.SetKeyBalanceRefill(p.ID, rest[1], nil); err != nil {
				return err
			}
		}
	case len(rest) > 2:
		if err := provider.SetKeyOwnBalance(p.ID, rest[1], nil); err != nil {
			return err
		}
	case refill != nil:
		if err := provider.SetKeyBalanceRefill(p.ID, rest[1], r); err != nil {
			return err
		}
	}
	if len(rest) > 2 || refill != nil {
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
	if !p.TakesOwnBalance() {
		fmt.Println(muted.Render(p.Name + " tells its balance itself: the Usage page shows that, not one typed"))
	}
	for _, k := range keys {
		name := k.Masked + " " + muted.Render(k.ID)
		if k.Name != "" {
			name = k.Name + " " + muted.Render(k.Masked+" · "+k.ID)
		}
		e, ok := p.OwnBalanceEstimate(k.ID)
		if !ok {
			fmt.Println(name, muted.Render("· no balance typed"))
			continue
		}
		fmt.Println(name, "·", keyBalanceLine(e))
	}
	return nil
}

// keyBalanceLine is what is left of a balance typed, as the CLI says it.
func keyBalanceLine(e provider.BalanceEstimate) string {
	money := func(v float64) string {
		if e.Currency == "cny" {
			return fmt.Sprintf("¥%.2f", v)
		}
		return fmt.Sprintf("$%.2f", v)
	}
	left := "what is left can't be told yet: the dollar's rate in yuan isn't known"
	if e.Left != nil {
		left = "about " + money(*e.Left) + " left"
	}
	from := "typed " + e.At.Local().Format("Jan 2 15:04")
	if e.From.After(e.At) {
		from = "refilled " + e.From.Local().Format("Jan 2 15:04")
	}
	spent := fmt.Sprintf("$%.2f spent since", e.Spent)
	if e.Currency == "cny" && e.Rate > 0 {
		spent += fmt.Sprintf(" at ¥%.4g a dollar", e.Rate)
	}
	out := fmt.Sprintf("%s · %s %s, %s", left, money(e.Set), from, spent)
	if e.Refill != nil && e.Next != nil {
		out += " · refills " + e.Refill.String() + ", next " + e.Next.Local().Format("Jan 2")
	}
	return out
}

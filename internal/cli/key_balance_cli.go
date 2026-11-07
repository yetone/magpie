package cli

import (
	"fmt"

	"github.com/yetone/magpie/internal/provider"
	stats "github.com/yetone/magpie/internal/usage"
)

const keyBalanceUsage = "magpie provider key-balance <id> [key [usd|off]]"

// keyBalanceCmd shows, or sets, the balance typed by hand on a provider's
// API keys whose vendor tells none (tenacious_eagle on Discord): what is
// left of it is the amount less what magpie priced the key's calls at
// since it was typed (provider/key_balance_own.go).
func keyBalanceCmd(rest []string) error {
	if len(rest) < 1 || len(rest) > 3 {
		return fmt.Errorf("%s", keyBalanceUsage)
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if p.Account != nil {
		return fmt.Errorf("%s is a subscription: a balance typed by hand is for an API key", p.Name)
	}
	if len(rest) > 2 {
		usd, err := provider.ParseOwnBalance(rest[2])
		if err != nil {
			return err
		}
		if err := provider.SetKeyOwnBalance(p.ID, rest[1], usd); err != nil {
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
	if !p.TakesOwnBalance() {
		fmt.Println(muted.Render(p.Name + " tells its balance itself: the Usage page shows that, not one typed"))
	}
	for _, k := range keys {
		name := k.Masked + " " + muted.Render(k.ID)
		if k.Name != "" {
			name = k.Name + " " + muted.Render(k.Masked+" · "+k.ID)
		}
		b, ok := p.KeyOwnBalance(k.ID)
		if !ok {
			fmt.Println(name, muted.Render("· no balance typed"))
			continue
		}
		spent := stats.KeySpent(k.ID, b.At)
		fmt.Printf("%s · about $%.2f left · $%.2f typed %s, $%.2f spent since\n", name, b.USD-spent, b.USD, b.At.Local().Format("Jan 2 15:04"), spent)
	}
	return nil
}

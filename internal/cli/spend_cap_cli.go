package cli

import (
	"fmt"
	"time"

	"github.com/yetone/magpie/internal/provider"
	stats "github.com/yetone/magpie/internal/usage"
)

const spendCapUsage = "magpie provider spend-cap <id> [key [usd [day|month|total]|off]]"

// spendCapCmd shows, or sets, how much a provider's API keys may spend
// (sparkling_lemur_62868 on Discord): in USD as the Usage page prices
// their calls, a day, a month or in all. At it, routing takes the key for
// used up (provider/key_spend.go).
func spendCapCmd(rest []string) error {
	if len(rest) < 1 || len(rest) > 4 {
		return fmt.Errorf("%s", spendCapUsage)
	}
	p, err := provider.Find(rest[0])
	if err != nil {
		return err
	}
	if p.Account != nil {
		return fmt.Errorf("%s is a subscription: a spend cap is for an API key (an account's cap is a share of its usage windows, magpie provider account-cap)", p.Name)
	}
	if len(rest) > 2 {
		period := ""
		if len(rest) > 3 {
			period = rest[3]
		}
		c, err := provider.ParseSpendCap(rest[2], period)
		if err != nil {
			return err
		}
		if err := provider.SetKeySpendCap(p.ID, rest[1], c); err != nil {
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
	now := time.Now()
	for _, k := range keys {
		name := k.Masked + " " + muted.Render(k.ID)
		if k.Name != "" {
			name = k.Name + " " + muted.Render(k.Masked+" · "+k.ID)
		}
		sc, ok := p.KeySpendCap(k.ID)
		if !ok {
			fmt.Println(name, muted.Render("· no spend cap"))
			continue
		}
		start, reset := sc.Window(now)
		spent := stats.KeySpent(k.ID, start)
		line := fmt.Sprintf("%s · $%.2f of $%.2f %s", name, spent, sc.USD, spendCapPeriod(sc, start))
		switch {
		case spent >= sc.USD && reset.IsZero():
			line += " · at its cap"
		case spent >= sc.USD:
			line += " · at its cap until " + reset.Format("Jan 2 15:04")
		}
		fmt.Println(line)
	}
	return nil
}

// spendCapPeriod is the period of sc as a line tells it.
func spendCapPeriod(sc provider.SpendCap, start time.Time) string {
	switch sc.Period {
	case provider.SpendMonth:
		return "this month"
	case provider.SpendTotal:
		return "in all since " + start.Local().Format("Jan 2 2006")
	}
	return "today"
}

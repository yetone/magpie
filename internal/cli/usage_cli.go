package cli

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/fx"
	pr "github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	stats "github.com/yetone/magpie/internal/usage"
)

// costSettings, costRate and costYuan are Settings' currency choice, the
// CNY-per-USD rate to show a cost in yuan at, and, for the choice
// "provider", the providers that sell in yuan — loadCostCurrency sets
// them once at a command's start, not again for every row it prints.
var (
	costSettings settings.Settings
	costRate     float64
	costYuan     map[string]bool
)

// loadCostCurrency reads Settings' currency choice and, only when a cost
// may be in yuan, the exchange rate (internal/fx, its own 12h cache —
// this asks the network only when that's stale).
func loadCostCurrency() {
	costSettings = settings.Load()
	costRate, costYuan = 0, nil
	if costSettings.Currency == settings.CurrencyByProvider {
		costYuan = pr.YuanPricedSet()
	}
	if costSettings.Currency != "usd" && costSettings.Currency != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		costRate = fx.Get(ctx).CNYPerUSD
		cancel()
	}
}

// usageCmd: `magpie usage [today|7d|30d|all]` — tokens and cost per agent,
// model, subscription account and session; with --csv, every request of the
// period as CSV, one row each, to set beside a vendor's bill, and with
// --account <name> too, only the calls that account answered (#557). With
// --days or --last, a rolling window ending now rather than a preset's
// calendar boundary (#2075), as magpie sessions and magpie quota history
// take --days.
func usageCmd(args []string) error {
	return usageTo(os.Stdout, args)
}

func usageTo(out io.Writer, args []string) error {
	const how = "usage: magpie usage [--csv [--account <name>]] [--days <n>|--last <dur>] [today|7d|30d|all]"
	asCSV := false
	if i := slices.Index(args, "--csv"); i > 0 {
		asCSV, args = true, slices.Delete(slices.Clone(args), i, i+1)
	}
	var f stats.Filter
	if i := slices.Index(args, "--account"); i > 0 {
		if i+1 >= len(args) || !asCSV {
			return fmt.Errorf("%s", how)
		}
		f.Account, args = args[i+1], slices.Delete(slices.Clone(args), i, i+2)
	}
	// a rolling window: --days takes a number of days (1, 7, even 1.5, as
	// magpie quota history's does) and --last a duration (24h, 90m), both
	// ending now, where the presets begin at a local midnight (#2075)
	var last time.Duration
	take := func(flag string, parse func(string) (time.Duration, error)) (bool, error) {
		v := ""
		for i := 0; i < len(args); i++ {
			switch a := args[i]; {
			case a == flag && i+1 < len(args):
				v, args = args[i+1], slices.Delete(slices.Clone(args), i, i+2)
				i--
			case strings.HasPrefix(a, flag+"="):
				v, args = strings.TrimPrefix(a, flag+"="), slices.Delete(slices.Clone(args), i, i+1)
				i--
			case a == flag || strings.HasPrefix(a, flag+"="):
				return true, fmt.Errorf("%s takes a value\n%s", flag, how)
			}
		}
		if v == "" {
			return false, nil
		}
		d, err := parse(v)
		if err != nil || d <= 0 {
			return true, fmt.Errorf("%s takes a %s greater than zero, not %q\n%s", flag, strings.TrimPrefix(flag, "--"), v, how)
		}
		last = d
		return true, nil
	}
	daysTaken, err := take("--days", func(v string) (time.Duration, error) {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(n) || n > 100*365 {
			return 0, fmt.Errorf("not a number of days: %q", v)
		}
		return time.Duration(n * 24 * float64(time.Hour)), nil
	})
	if err != nil {
		return err
	}
	if lastTaken, err := take("--last", time.ParseDuration); err != nil {
		return err
	} else if daysTaken && lastTaken {
		return fmt.Errorf("take --days or --last, not both\n%s", how)
	}
	if len(args) > 2 {
		return fmt.Errorf("%s", how)
	}
	period := stats.Month
	if last > 0 {
		if len(args) > 1 {
			// a preset beside the window: which one was meant can't be told
			return fmt.Errorf("take --days/--last or a period (%s), not both\n%s", args[1], how)
		}
		p, ok := stats.LastOf(last)
		if !ok {
			return fmt.Errorf("%s", how)
		}
		period = p
	} else if len(args) > 1 {
		switch strings.ToLower(args[1]) {
		case "today", "day":
			period = stats.Today
		case "7d", "week":
			period = stats.Week
		case "30d", "month":
			period = stats.Month
		case "all":
			period = stats.All
		default:
			return fmt.Errorf("%s", how)
		}
	}
	if asCSV {
		rows, _, _ := stats.Ledger(period, f)
		return stats.WriteCSV(out, rows)
	}
	loadCostCurrency()
	// s is the calls through magpie, d the calls the agents made on their
	// own, read from their session files: the window's Requests tab counts
	// them too (Kumo31 on Discord). Asked together, both are of one day when
	// midnight falls while they are read.
	s, d := stats.Summaries(period)
	title := map[stats.Period]string{stats.Today: "today", stats.Week: "last 7 days", stats.Month: "last 30 days", stats.All: "all time"}[s.Period]
	if dur := s.Period.Last(); dur > 0 {
		title = "last " + humanWindow(dur)
	}
	if s.Calls == 0 && d.Calls == 0 {
		fmt.Fprintln(out, muted.Render("no calls "+title+" ·"), "route an agent through magpie and its usage shows up here")
		fmt.Fprintln(out, faint.Render("  "+stats.Path()))
		return nil
	}
	names := map[string]string{}
	for _, a := range agent.All() {
		names[a.ID] = a.Name
	}
	if s.Calls == 0 {
		fmt.Fprintln(out, muted.Render("no calls through magpie "+title))
	} else {
		printUsage(out, s, title, names)
	}
	if d.Calls > 0 {
		fmt.Fprintln(out)
		fmt.Fprintln(out, muted.Render("not through magpie · the agents' own requests, read from their session files"))
		printUsage(out, d, title, names)
	}
	fmt.Fprintln(out, faint.Render("  "+stats.Path()))
	return nil
}

// humanWindow says a rolling window's length the way a title reads it:
// whole days as days, whole hours as hours, the rest in minutes.
func humanWindow(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return plural(int(d/(24*time.Hour)), "day")
	case d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	default:
		return plural(int(d/time.Minute)+boolToInt(d%time.Minute > 0), "minute")
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// printUsage is a summary's totals and its tables: agents, models,
// upstream keys, accounts, gateway keys and the top sessions.
func printUsage(out io.Writer, s stats.Summary, title string, names map[string]string) {
	fmt.Fprintln(out, bold.Render(fmtTokens(s.Tokens())+" tokens"), muted.Render(title+" ·"), plural(s.Calls, "call"), muted.Render("·"), cost(s.Totals)+unpricedNote(s.Unpriced)+credits(s.Totals))
	fmt.Fprintln(out, muted.Render("  in "+fmtTokens(s.Input)+"  out "+fmtTokens(s.Output)+"  cache read "+fmtTokens(s.CacheRead)+"  cache write "+fmtTokens(s.CacheWrite)+"  reasoning "+fmtTokens(s.Reasoning)),
		func() string {
			// requests, each counted once by how it ended: one a later try
			// answered is not an error (小一 on Discord)
			more := ""
			if s.Errors > 0 {
				more += muted.Render(" · ") + plural(s.Errors, "error")
			}
			if s.Retried > 0 {
				more += muted.Render(" · ") + fmt.Sprintf("%d answered on a retry", s.Retried)
			}
			return more
		}())
	table := func(head string, gs []stats.Group, name func(stats.Group) string) {
		fmt.Fprintln(out)
		fmt.Fprintln(out, faint.Render("  "+head))
		w := 0
		for _, g := range gs {
			w = max(w, len(name(g)))
		}
		for _, g := range gs {
			share := ""
			if s.Tokens() > 0 {
				share = fmt.Sprintf("%3.0f%%", 100*float64(g.Tokens())/float64(s.Tokens()))
			}
			fmt.Fprintln(out, "  "+pad(name(g), w), muted.Render(share), pad(fmtTokens(g.Tokens()), 7), faint.Render(pad(plural(g.Calls, "call"), 10)), costOf(g.Totals, g.Provider), faint.Render(speed(g.Totals)), muted.Render(stats.FormatCredits(g.Totals)))
		}
	}
	provider := func(id string) string {
		if id == stats.UnknownProvider {
			return "local session"
		}
		return id
	}
	table("agents", s.Agents, func(g stats.Group) string {
		if n := names[g.ID]; n != "" {
			return n
		}
		return g.ID
	})
	table("models", s.Models, func(g stats.Group) string {
		if g.Provider == stats.UnknownProvider {
			return provider(g.Provider) + "/" + g.Model
		}
		return g.ID
	})
	if len(s.ProviderKeys) > 0 {
		table("upstream provider keys", s.ProviderKeys, func(g stats.Group) string {
			name := g.ProviderKeyName
			if name == "" {
				name = g.ProviderKeyID
			}
			if name == "" {
				name = "key not recorded"
			}
			return g.Provider + " / " + name
		})
	}
	if len(s.Accounts) > 0 {
		table("accounts", s.Accounts, func(g stats.Group) string {
			// by the name the user gave it now (plugins#310)
			who := pr.AccountShownOf(g.Provider, g.Account)
			if who == "" {
				who = "account not recorded"
			}
			return provider(g.Provider) + " / " + who
		})
	}
	if len(s.CallerKeys) > 0 {
		table("gateway keys", s.CallerKeys, func(g stats.Group) string {
			if g.CallerKeyName != "" {
				return g.CallerKeyName
			}
			return g.CallerKeyID
		})
	}
	if len(s.Sessions) > 0 {
		top := s.Sessions[:min(len(s.Sessions), 10)]
		head := "sessions"
		if s.SessionCount > len(top) {
			head = fmt.Sprintf("sessions · top %d of %d", len(top), s.SessionCount)
		}
		table(head, top, func(g stats.Group) string {
			n := names[g.Agent]
			if n == "" {
				n = g.Agent
			}
			return n + "  " + g.ID
		})
	}
}

// speed is how long the timed calls took to their first token, and how
// fast they wrote after it (#196); "" when none was timed.
func speed(t stats.Totals) string {
	if t.Timed == 0 {
		return ""
	}
	out := "ttft " + fmtMs(t.MeanTTFT())
	if v := t.Speed(); v > 0 {
		out += fmt.Sprintf(" · %.0f tok/s", v)
	}
	return out
}

func fmtMs(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	return fmt.Sprintf("%.1f s", float64(ms)/1000)
}

// unpricedNote is how many calls a total's cost leaves out for having no
// known price, said beside it, so the sum never reads as if they cost
// nothing (#1856)
func unpricedNote(n int) string {
	if n == 0 {
		return ""
	}
	return faint.Render(" (" + plural(n, "call") + " unpriced)")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func fmtTokens(n int) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.2fB", float64(n)/1e9)
	case n >= 10_000_000:
		return fmt.Sprintf("%.0fM", float64(n)/1e6)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 100_000:
		return fmt.Sprintf("%.0fK", float64(n)/1e3)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// credits is " · " and what the credited calls cost in their plans'
// credits, as the replies said (#2512); "" when none said.
func credits(t stats.Totals) string {
	if c := stats.FormatCredits(t); c != "" {
		return muted.Render(" · " + c)
	}
	return ""
}

// cost renders a list-price cost of several providers' calls, saying
// when some could not be priced, in the currency Settings shows a total in
// at costRate (set once per command by loadCostCurrency).
func cost(t stats.Totals) string { return costOf(t, "") }

// costOf is cost of one provider's calls, in the currency Settings shows
// that provider's in (its own, with the choice "provider"); "" is a sum
// of several.
func costOf(t stats.Totals, providerID string) string {
	if t.Cost == 0 && t.Unpriced > 0 {
		return faint.Render("no price")
	}
	cncy := costSettings.TotalCostIn()
	if providerID != "" && providerID != stats.UnknownProvider {
		cncy = costSettings.CostIn(costYuan[providerID])
	}
	s := stats.FormatCost(t.Cost, cncy, costRate)
	if t.Unpriced > 0 {
		s += muted.Render("+")
	}
	return green.Render("≈" + s)
}

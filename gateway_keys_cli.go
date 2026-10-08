package main

import (
	"cmp"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/provider"
)

func gatewayKeys(args []string) error { return gatewayKeysTo(os.Stdout, args) }

func gatewayKeysTo(out io.Writer, args []string) error {
	args = args[1:]
	if len(args) == 0 {
		args = []string{"list"}
	}
	action := args[0]
	if action == "limit" {
		access.MigrateLegacyLANKeyBestEffort()
		return gatewayKeyLimit(out, args[1:])
	}
	if action == "models" {
		access.MigrateLegacyLANKeyBestEffort()
		return gatewayKeyModels(out, args[1:])
	}
	if action == "accounts" {
		access.MigrateLegacyLANKeyBestEffort()
		return gatewayKeyAccounts(out, args[1:])
	}
	// add <name> --key <value>: the key keeps a value its clients already
	// send (love1sbug on X); "--key -" reads it from stdin, out of the
	// shell's history
	var own string
	if action == "add" && len(args) == 4 && args[2] == "--key" {
		own = args[3]
		if own == "-" {
			b, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
			if err != nil {
				return err
			}
			own = strings.TrimSpace(string(b))
			if own == "" {
				return fmt.Errorf("no key came on stdin")
			}
		}
		args = args[:2]
	}
	if (action == "list" && len(args) != 1) || (action != "list" && len(args) != 2) {
		return fmt.Errorf("usage: magpie gateway-key list | add <name> [--key <value> | --key -] | rotate <id> | remove <id> | limit <id> [off | day|week|month [--tokens N] [--cost USD] [--cache-reads]] | models <id> [all | <provider>/<model>|<provider>/* ...] | accounts <id> [all | <provider>/<account>|<provider>/<key id> ...]")
	}
	switch action {
	case "list", "add", "rotate", "remove":
	default:
		return fmt.Errorf("unknown gateway-key command %q", action)
	}
	access.MigrateLegacyLANKeyBestEffort()
	if action == "list" {
		keys, err := access.List()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSTATE\tGATEWAY KEY\tLIMIT\tMODELS\tACCOUNTS")
		now := time.Now()
		// an account is shown by who is signed in, kept by its stable id (#905)
		names := provider.AccountNames()
		for _, k := range keys {
			state := "enabled"
			if k.Off {
				state = "disabled"
			}
			models := "all"
			if len(k.Models) > 0 {
				models = strings.Join(k.Models, ",")
			}
			accounts := "all"
			if len(k.Accounts) > 0 {
				shown := make([]string, len(k.Accounts))
				for i, a := range k.Accounts {
					shown[i] = cmp.Or(names[a], a)
				}
				accounts = strings.Join(shown, ",")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, strings.Map(func(r rune) rune {
				if r < 32 || r == 127 {
					return ' '
				}
				return r
			}, k.Name), state, k.Masked, limitWords(k, now), models, accounts)
		}
		return w.Flush()
	}
	in := access.Change{Key: args[1]}
	if action == "add" {
		in = access.Change{Name: args[1], Secret: own}
	}
	secret, err := access.Update(action+"-key", in)
	if err != nil {
		return err
	}
	if secret != "" {
		_, err = fmt.Fprintln(out, secret)
	} else {
		_, err = fmt.Fprintln(out, "Gateway key removed")
	}
	return err
}

// gatewayKeyLimit shows or sets a key's limit (#585):
//
//	magpie gateway-key limit <id>                       what it has used of its limit
//	magpie gateway-key limit <id> off                   no limit
//	magpie gateway-key limit <id> day|week|month [--tokens N] [--cost USD] [--cache-reads]
func gatewayKeyLimit(out io.Writer, args []string) error {
	usage := fmt.Errorf("usage: magpie gateway-key limit <id> [off | day|week|month [--tokens N] [--cost USD] [--cache-reads]]")
	if len(args) == 0 {
		return usage
	}
	id := args[0]
	if len(args) > 1 {
		var lim *access.Limit
		if args[1] != "off" {
			lim = &access.Limit{Period: args[1]}
			if !slices.Contains(access.Periods, lim.Period) {
				return fmt.Errorf("a limit's period is day, week or month, not %q", lim.Period)
			}
			rest := args[2:]
			for i := 0; i < len(rest); i++ {
				flag, val, has := strings.Cut(rest[i], "=")
				next := func() (string, error) {
					if has {
						return val, nil
					}
					if i+1 >= len(rest) {
						return "", fmt.Errorf("%s needs a value", flag)
					}
					i++
					return rest[i], nil
				}
				switch flag {
				case "--tokens":
					v, err := next()
					if err != nil {
						return err
					}
					n, err := parseTokens(v)
					if err != nil || n < 0 {
						return fmt.Errorf("--tokens is a count, like 1000000 or 2m: %q", v)
					}
					lim.Tokens = int64(n)
				case "--cost":
					v, err := next()
					if err != nil {
						return err
					}
					if lim.Cost, err = strconv.ParseFloat(strings.TrimPrefix(v, "$"), 64); err != nil {
						return fmt.Errorf("--cost is US dollars, like 5 or 2.50: %q", v)
					}
				case "--cache-reads":
					lim.CacheReads = true
				default:
					return usage
				}
			}
			if !lim.Limited() {
				return fmt.Errorf("say --tokens, --cost or both, or off for no limit")
			}
		} else if len(args) > 2 {
			return usage
		}
		if _, err := access.Update("limit-key", access.Change{Key: id, Limit: lim}); err != nil {
			return err
		}
	}
	keys, err := access.List()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(keys, func(k access.Key) bool { return k.ID == id })
	if i < 0 {
		return fmt.Errorf("Key not found")
	}
	k, now := keys[i], time.Now()
	st := budget.Of(k, now)
	if st == nil {
		_, err = fmt.Fprintf(out, "%s: no limit\n", k.Name)
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Key\t%s (%s)\n", k.Name, k.ID)
	fmt.Fprintf(w, "Window\tthis %s, %s – %s (local time)\n", st.Period, st.Start.Format("2006-01-02 15:04"), st.Reset.Format("2006-01-02 15:04"))
	if st.TokenLimit > 0 {
		counted := "input, output and cache writes"
		if st.CacheReads {
			counted += ", and cache reads"
		}
		fmt.Fprintf(w, "Tokens\t%d used of %d, %d left (%s)\n", st.Tokens, st.TokenLimit, st.TokensLeft, counted)
	} else {
		fmt.Fprintf(w, "Tokens\t%d used, no cap\n", st.Tokens)
	}
	unpriced := ""
	if st.Unpriced > 0 {
		unpriced = fmt.Sprintf(", %d calls without a known price not in it", st.Unpriced)
	}
	if st.CostLimit > 0 {
		fmt.Fprintf(w, "Cost\t$%.2f used of $%.2f, $%.2f left (estimate at Usage prices%s)\n", st.Cost, st.CostLimit, st.CostLeft, unpriced)
	} else {
		fmt.Fprintf(w, "Cost\t$%.2f used, no cap (estimate%s)\n", st.Cost, unpriced)
	}
	state := "open"
	if st.Spent {
		state = "spent: requests are refused until " + st.Reset.Format("2006-01-02 15:04")
	}
	fmt.Fprintf(w, "State\t%s\n", state)
	return w.Flush()
}

// gatewayKeyModels shows or sets the models a key may use (#882):
//
//	magpie gateway-key models <id>                          the models it may use
//	magpie gateway-key models <id> all                      every model
//	magpie gateway-key models <id> openai/gpt-5 anthropic/*  only these
func gatewayKeyModels(out io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: magpie gateway-key models <id> [all | <provider>/<model>|<provider>/* ...]")
	}
	id := args[0]
	if len(args) > 1 {
		ms := args[1:]
		if len(ms) == 1 && ms[0] == "all" {
			ms = nil
		}
		if _, err := access.Update("models-key", access.Change{Key: id, Models: ms}); err != nil {
			return err
		}
	}
	keys, err := access.List()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(keys, func(k access.Key) bool { return k.ID == id })
	if i < 0 {
		return fmt.Errorf("Key not found")
	}
	k := keys[i]
	if len(k.Models) == 0 {
		_, err = fmt.Fprintf(out, "%s: every model\n", k.Name)
		return err
	}
	_, err = fmt.Fprintf(out, "%s: only %s\n", k.Name, strings.Join(k.Models, ", "))
	return err
}

// limitWords is a key's limit in the list: "-" for none, else what it has
// used of it, "1200/1000000 tokens/day".
func limitWords(k access.Key, now time.Time) string {
	st := budget.Of(k, now)
	if st == nil {
		return "-"
	}
	var parts []string
	if st.TokenLimit > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d tokens", st.Tokens, st.TokenLimit))
	}
	if st.CostLimit > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f/$%.2f", st.Cost, st.CostLimit))
	}
	s := strings.Join(parts, " ") + " per " + st.Period
	if st.Spent {
		s += " (spent until " + st.Reset.Format("01-02 15:04") + ")"
	}
	return s
}

// gatewayKeyAccounts shows or sets a key's account whitelist (#905): the
// accounts and keys it may use, "<provider>/<account>" — a signed-in
// account by its name, a key by its KeyID — every one when none are set.
func gatewayKeyAccounts(out io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: magpie gateway-key accounts <id> [all | <provider>/<account>|<provider>/<key id> ...]")
	}
	id := args[0]
	if len(args) > 1 {
		as := args[1:]
		if len(as) == 1 && as[0] == "all" {
			as = nil
		}
		if _, err := access.Update("accounts-key", access.Change{Key: id, Accounts: as}); err != nil {
			return err
		}
	}
	keys, err := access.List()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(keys, func(k access.Key) bool { return k.ID == id })
	if i < 0 {
		return fmt.Errorf("Key not found")
	}
	k := keys[i]
	if len(k.Accounts) == 0 {
		_, err = fmt.Fprintf(out, "%s: every account\n", k.Name)
		return err
	}
	// shown by who is signed in, kept by the account's stable id (#905)
	names := provider.AccountNames()
	shown := make([]string, len(k.Accounts))
	for i, a := range k.Accounts {
		shown[i] = cmp.Or(names[a], a)
	}
	_, err = fmt.Fprintf(out, "%s: only %s\n", k.Name, strings.Join(shown, ", "))
	return err
}

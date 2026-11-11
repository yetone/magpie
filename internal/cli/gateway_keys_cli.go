package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/budget"
	"github.com/yetone/magpie/internal/oidc"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
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
	if action == "aliases" {
		access.MigrateLegacyLANKeyBestEffort()
		return gatewayKeyAliases(out, args[1:])
	}
	if action == "accounts" {
		access.MigrateLegacyLANKeyBestEffort()
		return gatewayKeyAccounts(out, args[1:])
	}
	if action == "groups" || action == "group" {
		return gatewayKeyGroups(out, args[1:])
	}
	if action == "quotas" {
		access.MigrateLegacyLANKeyBestEffort()
		return gatewayKeyQuotas(out, args[1:])
	}
	if action == "concurrency" {
		access.MigrateLegacyLANKeyBestEffort()
		return gatewayKeyConcurrency(out, args[1:])
	}
	if action == "tier" {
		access.MigrateLegacyLANKeyBestEffort()
		return gatewayKeyTier(out, args[1:])
	}
	if action == "mcp" {
		access.MigrateLegacyLANKeyBestEffort()
		return gatewayKeyMCP(out, args[1:])
	}
	if action == "access" {
		access.MigrateLegacyLANKeyBestEffort()
		return gatewayKeyAccess(out, args[1:])
	}
	if action == "move" {
		access.MigrateLegacyLANKeyBestEffort()
		return gatewayKeyMove(out, args[1:])
	}
	if action == "issue" {
		access.MigrateLegacyLANKeyBestEffort()
		return gatewayKeyIssue(out, args[1:])
	}
	if action == "issuer" {
		access.MigrateLegacyLANKeyBestEffort()
		return gatewayKeyIssuer(out, args[1:])
	}
	if action == "oidc" {
		access.MigrateLegacyLANKeyBestEffort()
		return gatewayKeyOIDC(out, args[1:])
	}
	// add <name> --read-only: a key that reads stats and uses no model
	// (Steven on Discord: a desk display)
	readOnly := false
	if action == "add" {
		if i := slices.Index(args[1:], "--read-only"); i >= 0 {
			readOnly = true
			args = slices.Delete(slices.Clone(args), 1+i, 2+i)
		}
	}
	// add <name> --key <value>: the key keeps a value its clients already
	// send (love1sbug on X); rotate <id> --key <value> gives a key one
	// (isaac_bai on Discord); "--key -" reads it from stdin, out of the
	// shell's history
	var own string
	if (action == "add" || action == "rotate") && len(args) == 4 && args[2] == "--key" {
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
		return fmt.Errorf("usage: magpie gateway-key list | add <name> [--key <value> | --key -] [--read-only] | rotate <id> [--key <value> | --key -] | remove <id> | limit <id> [list | off | reset [<n>] | remove <n> | [add] day|week|month|<N>d|<N>h [--tokens N] [--cost USD] [--cache-reads] [--model <provider>/<model>|<provider>/* ...]] | models <id> [all | <provider>/<model>|<provider>/*|re:<regexp> ...] | aliases <id> [none | <name>=<provider>/<model> ...] | accounts <id> [all | <provider>/<account>|<provider>/<key id> ...] | quotas <id> [on | off] | concurrency <id> [<n> | off] | tier <id> [follow | standard | fast | ultrafast] | mcp <id> [all | none | <server>|<server>/<tool> ...] | access <id> [read-only | full] | move <id> <place> | issue --ttl <duration> [--name <name>] [--models a,b] [--accounts a,b] [--tokens N] [--cost USD] [--read-only] [--from <id>] | issuer <id> [on | off] | oidc [list | add --key <id> --repo owner/name … | remove <rule id>]")
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
		fmt.Fprintln(w, "ID\tNAME\tSTATE\tGATEWAY KEY\tLIMIT\tMODELS\tACCOUNTS\tACCESS")
		now := time.Now()
		// an account is shown by who is signed in, kept by its stable id (#905)
		names := provider.AccountNames()
		for _, k := range keys {
			state := "enabled"
			switch {
			case k.Off:
				state = "disabled"
			case !k.Expires.IsZero() && !now.Before(k.Expires):
				state = "expired"
			case !k.Expires.IsZero():
				state = "until " + k.Expires.Local().Format("01-02 15:04")
			}
			if k.Issuer != "" {
				state += " (issued by " + k.Issuer + ")"
			} else if k.Issues {
				state += ", issues keys"
			}
			// a key that names accounts and no model may use only its
			// accounts' providers' models (access.HeldModels)
			models := "all"
			if ms := access.HeldModels(k.Models, access.ExpandGroups(k.Accounts)); len(ms) > 0 {
				models = strings.Join(ms, ",")
			}
			accounts := "all"
			if len(k.Accounts) > 0 {
				// a column: no space after each comma, as before
				accounts = strings.ReplaceAll(access.AccountsShown(names, k.Accounts), ", ", ",")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, strings.Map(func(r rune) rune {
				if r < 32 || r == 127 {
					return ' '
				}
				return r
			}, k.Name), state, k.Masked, limitWords(k, now), models, accounts, accessWord(k))
		}
		return w.Flush()
	}
	in := access.Change{Key: args[1]}
	if action == "rotate" {
		in.Secret = own
	}
	if action == "add" {
		in = access.Change{Name: args[1], Secret: own, ReadOnly: readOnly}
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

// gatewayKeyLimit shows or sets a key's limits (#585, several since #2227):
//
//	magpie gateway-key limit <id> [list]                what it has used of each limit
//	magpie gateway-key limit <id> off                   no limit
//	magpie gateway-key limit <id> day|week|month|<N>d|<N>h [--tokens N] [--cost USD] [--cache-reads]
//	                                                    its only limit, or the one of several with that window
//	magpie gateway-key limit <id> add <window> [--tokens N] [--cost USD] [--cache-reads] [--model openai/*]
//	                                                    one more limit; a request passes only while every one has something left
//	magpie gateway-key limit <id> remove <n>            take off the nth limit
//	magpie gateway-key limit <id> reset [<n>]           count from 0 again, every limit or the nth; a new N-day or N-hour cycle from now (#1509)
//
// --model (again, or a comma list) makes a limit count only those models'
// calls, "<provider>/<model>" or "<provider>/*" (zola_xynb on X): a cap for
// one model or provider under one key, the key's other models served once
// it is spent.
func gatewayKeyLimit(out io.Writer, args []string) error {
	usage := fmt.Errorf("usage: magpie gateway-key limit <id> [off | reset [<n>] | remove <n> | [add] day|week|month|<N>d|<N>h [--tokens N] [--cost USD] [--cache-reads] [--model <provider>/<model>|<provider>/* ...]]")
	if len(args) == 0 {
		return usage
	}
	id := args[0]
	if len(args) > 1 {
		keys, err := access.List()
		if err != nil {
			return err
		}
		i := slices.IndexFunc(keys, func(k access.Key) bool { return k.ID == id })
		if i < 0 {
			return fmt.Errorf("Key not found")
		}
		have := keys[i].Limits()
		// nth reads a limit's number as the list shows it, from 1
		nth := func(v string) (*int, error) {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > len(have) {
				if len(have) == 0 {
					return nil, fmt.Errorf("%s has no limit", keys[i].Name)
				}
				return nil, fmt.Errorf("a limit's number is 1 to %d, as `magpie gateway-key limit %s` lists them: %q", len(have), id, v)
			}
			n--
			return &n, nil
		}
		switch args[1] {
		case "list":
			if len(args) > 2 {
				return usage
			}
		case "reset":
			in := access.Change{Key: id}
			if len(args) > 3 {
				return usage
			}
			if len(args) == 3 {
				if in.Index, err = nth(args[2]); err != nil {
					return err
				}
			}
			if _, err := access.Update("reset-limit-key", in); err != nil {
				return err
			}
		case "off":
			if len(args) > 2 {
				return usage
			}
			if _, err := access.Update("limit-key", access.Change{Key: id}); err != nil {
				return err
			}
		case "remove":
			if len(args) != 3 {
				return usage
			}
			n, err := nth(args[2])
			if err != nil {
				return err
			}
			if _, err := access.Update("limit-key", access.Change{Key: id, Index: n}); err != nil {
				return err
			}
		case "add":
			if len(args) < 3 {
				return usage
			}
			lim, err := parseKeyLimit(args[2], args[3:], usage)
			if err != nil {
				return err
			}
			n := len(have)
			if _, err := access.Update("limit-key", access.Change{Key: id, Limit: lim, Index: &n}); err != nil {
				return err
			}
		default:
			lim, err := parseKeyLimit(args[1], args[2:], usage)
			if err != nil {
				return err
			}
			in := access.Change{Key: id, Limit: lim}
			// of several, the one counted in the same window is set; the
			// only one is replaced, as before #2227
			if len(have) > 1 {
				v, _ := lim.Valid()
				n := slices.IndexFunc(have, func(l *access.Limit) bool {
					return v != nil && l.Period == v.Period && l.Days == v.Days && l.Hours == v.Hours && l.Scope() == v.Scope()
				})
				if n < 0 {
					return fmt.Errorf("%s has %d limits and none %s%s: add one with `magpie gateway-key limit %s add %s ...`, or take one off with `remove <n>`", keys[i].Name, len(have), perPhrase(v), onModels(v), id, args[1])
				}
				in.Index = &n
			}
			if _, err := access.Update("limit-key", in); err != nil {
				return err
			}
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
	all := budget.All(k, now)
	if len(all) == 0 {
		_, err = fmt.Fprintf(out, "%s: no limit\n", k.Name)
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "Key\t%s (%s)\n", k.Name, k.ID)
	for n, st := range all {
		if len(all) > 1 {
			fmt.Fprintf(w, "\t\n")
			fmt.Fprintf(w, "Limit %d\t%s, all must have something left\n", n+1, perPhrase(&access.Limit{Period: st.Period, Days: st.Days, Hours: st.Hours}))
		}
		if len(st.Models) > 0 {
			fmt.Fprintf(w, "Models\t%s: only their calls count, and only requests for them are refused once it is spent\n", strings.Join(st.Models, ", "))
		}
		fmt.Fprintf(w, "Window\tthis %s, %s – %s (local time)\n", (&access.Limit{Period: st.Period, Days: st.Days, Hours: st.Hours}).Per(), st.Start.Format("2006-01-02 15:04"), st.Reset.Format("2006-01-02 15:04"))
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
			if len(st.Models) > 0 {
				state = "spent: requests for these models are refused until " + st.Reset.Format("2006-01-02 15:04")
			}
		}
		fmt.Fprintf(w, "State\t%s\n", state)
	}
	return w.Flush()
}

// perPhrase is a limit's window as the CLI says it: "per week", "every 5
// hours".
func perPhrase(l *access.Limit) string {
	if l == nil {
		return ""
	}
	switch l.Period {
	case "days":
		return fmt.Sprintf("every %d days", l.Days)
	case "hours":
		return fmt.Sprintf("every %d hours", l.Hours)
	}
	return "per " + l.Period
}

// onModels is " on <models>" for a limit of some models, "" for one of the
// whole key.
func onModels(l *access.Limit) string {
	if !l.Scoped() {
		return ""
	}
	return " on " + strings.Join(l.Models, ", ")
}

// parseKeyLimit reads a limit as typed: its window, then --tokens,
// --cost and --cache-reads.
func parseKeyLimit(window string, rest []string, usage error) (*access.Limit, error) {
	lim := &access.Limit{Period: window}
	if window == "day" || window == "week" || window == "month" {
		// a period by its name: month ends in an h, and is no count of hours
	} else if n, ok := strings.CutSuffix(window, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days < 1 || days > access.MaxDays {
			return nil, fmt.Errorf("an N-day cycle is a whole number of days from 1 to %d, like 10d: %q", access.MaxDays, window)
		}
		lim = &access.Limit{Period: "days", Days: days}
	} else if n, ok := strings.CutSuffix(window, "h"); ok {
		// every N hours (#2102), a subscription's 5-hour window's pace
		hours, err := strconv.Atoi(n)
		if err != nil || hours < 1 || hours > access.MaxHours {
			return nil, fmt.Errorf("an N-hour cycle is a whole number of hours from 1 to %d, like 5h: %q", access.MaxHours, window)
		}
		lim = &access.Limit{Period: "hours", Hours: hours}
	} else if !slices.Contains(access.Periods, lim.Period) || lim.Period == "days" || lim.Period == "hours" {
		return nil, fmt.Errorf("a limit's period is day, week, month, <N>d (every N days) or <N>h (every N hours), not %q", lim.Period)
	}
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
				return nil, err
			}
			n, err := parseTokens(v)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("--tokens is a count, like 1000000 or 2m: %q", v)
			}
			lim.Tokens = int64(n)
		case "--cost":
			v, err := next()
			if err != nil {
				return nil, err
			}
			if lim.Cost, err = strconv.ParseFloat(strings.TrimPrefix(v, "$"), 64); err != nil {
				return nil, fmt.Errorf("--cost is US dollars, like 5 or 2.50: %q", v)
			}
		case "--cache-reads":
			lim.CacheReads = true
		case "--model", "--models":
			v, err := next()
			if err != nil {
				return nil, err
			}
			for _, m := range strings.Split(v, ",") {
				if m = strings.TrimSpace(m); m != "" {
					lim.Models = append(lim.Models, m)
				}
			}
		default:
			return nil, usage
		}
	}
	if !lim.Limited() {
		return nil, fmt.Errorf("say --tokens, --cost or both, or off for no limit")
	}
	return lim, nil
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
	if ms := access.HeldModels(nil, access.ExpandGroups(k.Accounts)); len(k.Models) == 0 && len(ms) > 0 {
		_, err = fmt.Fprintf(out, "%s: only its accounts' providers' models, %s\n", k.Name, strings.Join(ms, ", "))
		return err
	}
	if len(k.Models) == 0 {
		_, err = fmt.Fprintf(out, "%s: every model\n", k.Name)
		return err
	}
	_, err = fmt.Fprintf(out, "%s: only %s\n", k.Name, strings.Join(k.Models, ", "))
	return err
}

// gatewayKeyAliases shows or sets the names a key gives models
// (tsingchao on X):
//
//	magpie gateway-key aliases <id>                          its names
//	magpie gateway-key aliases <id> none                     no names
//	magpie gateway-key aliases <id> fast=openai/gpt-5 ...    only these
func gatewayKeyAliases(out io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: magpie gateway-key aliases <id> [none | <name>=<provider>/<model> ...]")
	}
	id := args[0]
	if len(args) > 1 {
		as := map[string]string{}
		if !(len(args) == 2 && args[1] == "none") {
			for _, a := range args[1:] {
				name, target, ok := strings.Cut(a, "=")
				if !ok {
					return fmt.Errorf("usage: magpie gateway-key aliases <id> [none | <name>=<provider>/<model> ...]: %q has no =", a)
				}
				as[name] = target
			}
		}
		if _, err := access.Update("aliases-key", access.Change{Key: id, Aliases: as}); err != nil {
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
	if len(k.Aliases) == 0 {
		_, err = fmt.Fprintf(out, "%s: no model names\n", k.Name)
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(k.Aliases)) {
		if _, err = fmt.Fprintf(out, "%s: %s = %s\n", k.Name, name, k.Aliases[name]); err != nil {
			return err
		}
	}
	return nil
}

// gatewayKeyMCP shows or sets the MCP servers and tools a key may use
// (wakaka on Discord):
//
//	magpie gateway-key mcp <id>                        what it may use
//	magpie gateway-key mcp <id> all                    every server
//	magpie gateway-key mcp <id> none                   no server
//	magpie gateway-key mcp <id> github fs/read_file    only these
func gatewayKeyMCP(out io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: magpie gateway-key mcp <id> [all | none | <server>|<server>/<tool> ...]")
	}
	id := args[0]
	if len(args) > 1 {
		l := args[1:]
		switch {
		case len(l) == 1 && l[0] == "all":
			l = nil
		case len(l) == 1 && l[0] == "none":
			l = []string{access.NoMCP}
		case slices.ContainsFunc(l, func(e string) bool { return strings.HasPrefix(e, "-") }):
			return fmt.Errorf("usage: magpie gateway-key mcp <id> [all | none | <server>|<server>/<tool> ...]")
		}
		if _, err := access.Update("mcp-key", access.Change{Key: id, MCP: l}); err != nil {
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
	switch {
	case len(k.MCP) == 0:
		_, err = fmt.Fprintf(out, "%s: every MCP server\n", k.Name)
	case slices.Equal(k.MCP, []string{access.NoMCP}):
		_, err = fmt.Fprintf(out, "%s: no MCP server\n", k.Name)
	default:
		_, err = fmt.Fprintf(out, "%s: only %s\n", k.Name, strings.Join(k.MCP, ", "))
	}
	return err
}

// limitWords is a key's limits in the list: "-" for none, else what it
// has used of each, "1200/1000000 tokens per day; $1.00/$20.00 per week".
func limitWords(k access.Key, now time.Time) string {
	all := budget.All(k, now)
	if len(all) == 0 {
		return "-"
	}
	var each []string
	for _, st := range all {
		var parts []string
		if st.TokenLimit > 0 {
			parts = append(parts, fmt.Sprintf("%d/%d tokens", st.Tokens, st.TokenLimit))
		}
		if st.CostLimit > 0 {
			parts = append(parts, fmt.Sprintf("$%.2f/$%.2f", st.Cost, st.CostLimit))
		}
		s := strings.Join(parts, " ") + " " + perPhrase(&access.Limit{Period: st.Period, Days: st.Days, Hours: st.Hours}) + onModels(&access.Limit{Models: st.Models})
		if st.Spent {
			s += " (spent until " + st.Reset.Format("01-02 15:04") + ")"
		}
		each = append(each, s)
	}
	return strings.Join(each, "; ")
}

// gatewayKeyAccounts shows or sets a key's account whitelist (#905): the
// accounts and keys it may use, "<provider>/<account>" — a signed-in
// account by its name, a key by its KeyID — every one when none are set.
// "<provider>/!<account>" keeps one out instead (LeiWang_2000 on X): the
// key may use every other, those signed in later too.
func gatewayKeyAccounts(out io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: magpie gateway-key accounts <id> [all | <provider>/<account>|<provider>/<key id>|<provider>/!<account>|@<group>|!@<group> ...]")
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
	// shown by who is signed in, kept by the account's stable id (#905);
	// one kept out, "<provider>/!<account>", as every account but it
	shown := access.AccountsShown(provider.AccountNames(), k.Accounts)
	if !strings.HasPrefix(shown, "every account but ") {
		shown = "only " + shown
	}
	_, err = fmt.Fprintf(out, "%s: %s\n", k.Name, shown)
	return err
}

// gatewayKeyGroups lists and edits the account groups a key's accounts
// may name, "@<group>" or "!@<group>" (LeiWang_2000 on X): a named set of
// accounts of any providers, read by every key naming it as it is now.
//
//	magpie gateway-key groups                          each group
//	magpie gateway-key groups add <name> <account>...  a new one
//	magpie gateway-key groups set <group> <account>... its accounts
//	magpie gateway-key groups rename <group> <name>
//	magpie gateway-key groups remove <group>           unless a key names it
func gatewayKeyGroups(out io.Writer, args []string) error {
	usage := fmt.Errorf("usage: magpie gateway-key groups [list | add <name> <provider>/<account>... | set <group> <provider>/<account>... | rename <group> <name> | remove <group>]")
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return usage
		}
		gs, err := access.AccountGroups()
		if err != nil {
			return err
		}
		if len(gs) == 0 {
			_, err = fmt.Fprintln(out, "No account groups. Add one: magpie gateway-key groups add <name> <provider>/<account>...")
			return err
		}
		names := provider.AccountNames()
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tACCOUNTS")
		for _, g := range gs {
			shown := "none"
			if len(g.Accounts) > 0 {
				shown = strings.ReplaceAll(access.AccountsShown(names, g.Accounts), ", ", ",")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", g.ID, g.Name, shown)
		}
		return w.Flush()
	case "add":
		if len(args) < 2 {
			return usage
		}
		g, err := access.SaveAccountGroup(access.AccountGroup{Name: args[1], Accounts: args[2:]})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Added the account group %s (%s). Use it: magpie gateway-key accounts <id> @%s, or !@%s to keep it out\n", g.Name, g.ID, g.ID, g.ID)
		return err
	case "set", "rename":
		if len(args) < 2 || (args[0] == "rename" && len(args) != 3) {
			return usage
		}
		g, err := access.FindAccountGroup(args[1])
		if err != nil {
			return err
		}
		if args[0] == "rename" {
			g.Name = args[2]
		} else {
			g.Accounts = args[2:]
		}
		if g, err = access.SaveAccountGroup(g); err != nil {
			return err
		}
		shown := "no account"
		if len(g.Accounts) > 0 {
			shown = access.AccountsShown(provider.AccountNames(), g.Accounts)
		}
		_, err = fmt.Fprintf(out, "%s: %s\n", g.Name, shown)
		return err
	case "remove":
		if len(args) != 2 {
			return usage
		}
		g, err := access.FindAccountGroup(args[1])
		if err != nil {
			return err
		}
		if err := access.RemoveAccountGroup(g.ID); err != nil {
			return err
		}
		_, err = fmt.Fprintf(out, "Removed the account group %s\n", g.Name)
		return err
	}
	return usage
}

// gatewayKeyQuotas shows or sets whether a key held to a budget, models
// or accounts is told the quotas of what it may use (#2216): the cards of
// the providers whose every model it may use, of its accounts and keys.
//
//	magpie gateway-key quotas <id>         whether it is
//	magpie gateway-key quotas <id> on|off  tell it them, or none (#1981)
func gatewayKeyQuotas(out io.Writer, args []string) error {
	usage := fmt.Errorf("usage: magpie gateway-key quotas <id> [on | off]")
	if len(args) == 0 || len(args) > 2 {
		return usage
	}
	id := args[0]
	if len(args) == 2 {
		if args[1] != "on" && args[1] != "off" {
			return usage
		}
		if _, err := access.Update("quotas-key", access.Change{Key: id, Quotas: args[1] == "on"}); err != nil {
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
	switch {
	case !k.Quotas:
		_, err = fmt.Fprintf(out, "%s: told no quotas when held to a budget, models or accounts\n", k.Name)
	default:
		_, err = fmt.Fprintf(out, "%s: told the quotas of the providers and accounts it may use\n", k.Name)
	}
	return err
}

// gatewayKeyConcurrency shows or sets how many of a key's requests may
// be in flight at once (young02036 on Discord); one more waits its turn,
// as over a provider's concurrency:
//
//	magpie gateway-key concurrency <id>           its cap, or none
//	magpie gateway-key concurrency <id> <n>|off   at most n at once, or no cap
func gatewayKeyConcurrency(out io.Writer, args []string) error {
	usage := fmt.Errorf("usage: magpie gateway-key concurrency <id> [<n> | off], n a whole number from 1 to %d", access.MaxKeyConcurrency)
	if len(args) == 0 || len(args) > 2 {
		return usage
	}
	id := args[0]
	if len(args) == 2 {
		n := 0
		if args[1] != "off" {
			v, err := strconv.Atoi(args[1])
			if err != nil || v < 1 || v > access.MaxKeyConcurrency {
				return usage
			}
			n = v
		}
		if _, err := access.Update("concurrency-key", access.Change{Key: id, MaxConcurrency: n}); err != nil {
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
	if k.MaxConcurrency > 0 {
		reqs := "requests"
		if k.MaxConcurrency == 1 {
			reqs = "request"
		}
		_, err = fmt.Fprintf(out, "%s: at most %d %s at once, the rest wait their turn\n", k.Name, k.MaxConcurrency, reqs)
	} else {
		_, err = fmt.Fprintf(out, "%s: no concurrency limit\n", k.Name)
	}
	return err
}

// gatewayKeyTier shows or sets the service tier a key's OpenAI-API requests
// are sent at whatever the client asked (JackyFill on X):
//
//	magpie gateway-key tier <id>                                    its tier
//	magpie gateway-key tier <id> follow|standard|fast|ultrafast     set it
func gatewayKeyTier(out io.Writer, args []string) error {
	if len(args) == 0 || len(args) > 2 {
		return fmt.Errorf("usage: magpie gateway-key tier <id> [follow | standard | fast | ultrafast]")
	}
	id := args[0]
	if len(args) == 2 {
		if _, err := access.Update("tier-key", access.Change{Key: id, ServiceTier: args[1]}); err != nil {
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
	word := map[string]string{"": "follows the request", settings.CodexTierStandard: "standard", "priority": "fast", "ultrafast": "ultrafast"}[k.ServiceTier]
	_, err = fmt.Fprintf(out, "%s: service tier %s\n", k.Name, word)
	return err
}

// accessWord is a key's access as the list says it: "browser" (Magpie
// Browser's, access.Key.Browser), "read-only" or "full".
func accessWord(k access.Key) string {
	if k.Browser {
		return "browser"
	}
	if k.ReadOnly {
		return "read-only"
	}
	return "full"
}

// gatewayKeyAccess shows or sets whether a key may use models or only
// read magpie's stats (Steven on Discord: a desk display's key):
//
//	magpie gateway-key access <id>                 which it has
//	magpie gateway-key access <id> read-only|full  quotas and usage alone, or what its other limits let it
func gatewayKeyAccess(out io.Writer, args []string) error {
	usage := fmt.Errorf("usage: magpie gateway-key access <id> [read-only | full]")
	if len(args) == 0 || len(args) > 2 {
		return usage
	}
	id := args[0]
	if len(args) == 2 {
		if args[1] != "read-only" && args[1] != "full" {
			return usage
		}
		if _, err := access.Update("access-key", access.Change{Key: id, ReadOnly: args[1] == "read-only"}); err != nil {
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
	if k.Browser {
		_, err = fmt.Fprintf(out, "%s: Magpie Browser's, it connects the extension and uses no model, MCP server, stats or write\n", k.Name)
	} else if k.ReadOnly {
		_, err = fmt.Fprintf(out, "%s: read-only, it reads quotas and usage and uses no model, MCP server or write\n", k.Name)
	} else {
		_, err = fmt.Fprintf(out, "%s: full access, models and stats as its other limits let it\n", k.Name)
	}
	return err
}

// gatewayKeyMove puts a key at a place in the list, counted from 1 as
// `magpie gateway-key list` shows them, the order the app's Gateway page
// lists them in too, where a key is dragged to its place (alextjy on
// Discord):
//
//	magpie gateway-key move <id> <place>
func gatewayKeyMove(out io.Writer, args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: magpie gateway-key move <id> <place>")
	}
	keys, err := access.List()
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(args[1])
	if err != nil || n < 1 || n > len(keys) {
		return fmt.Errorf("a key's place is 1 to %d, as `magpie gateway-key list` lists them: %q", len(keys), args[1])
	}
	n--
	if _, err := access.Update("move-key", access.Change{Key: args[0], Index: &n}); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Gateway key moved to place %d\n", n+1)
	return err
}

// gatewayKeyIssue makes a short-lived key (xazaj on X: one per sandboxed
// agent, that stops working by itself), and prints its secret alone so a
// script can take it:
//
//	magpie gateway-key issue --ttl 1h [--name n] [--models a,b] [--accounts a,b]
//	                         [--tokens N] [--cost USD] [--read-only] [--from <id>]
//
// --from issues it under a key that may issue keys (issuer <id> on), held
// to that key as POST /v1/magpie/keys with it would be.
func gatewayKeyIssue(out io.Writer, args []string) error {
	usage := fmt.Errorf("usage: magpie gateway-key issue --ttl <duration, e.g. 1h> [--name <name>] [--models <provider>/<model>,...] [--accounts <provider>/<account>,...] [--tokens N] [--cost USD] [--read-only] [--from <id>] [--json]")
	var in access.Issue
	asJSON := false
	list := func(v string) []string {
		var out []string
		for _, f := range strings.Split(v, ",") {
			if f = strings.TrimSpace(f); f != "" {
				out = append(out, f)
			}
		}
		return out
	}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		switch flag {
		case "--read-only":
			in.ReadOnly = true
			continue
		case "--json":
			asJSON = true
			continue
		case "--ttl", "--name", "--models", "--accounts", "--tokens", "--cost", "--from":
		default:
			return usage
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
			return usage
		}
		i++
		v := args[i]
		var err error
		switch flag {
		case "--ttl":
			if in.TTL, err = time.ParseDuration(v); err != nil {
				return fmt.Errorf("--ttl is a duration such as 30m, 1h or 24h")
			}
		case "--name":
			in.Name = v
		case "--models":
			in.Models = list(v)
		case "--accounts":
			in.Accounts = list(v)
		case "--tokens":
			if in.Tokens, err = strconv.ParseInt(v, 10, 64); err != nil || in.Tokens <= 0 {
				return fmt.Errorf("--tokens is a whole number above 0")
			}
		case "--cost":
			if in.Cost, err = strconv.ParseFloat(v, 64); err != nil || in.Cost <= 0 {
				return fmt.Errorf("--cost is an amount in USD above 0")
			}
		case "--from":
			in.Issuer = v
		}
	}
	if in.TTL == 0 {
		return usage
	}
	k, err := access.IssueKey(in)
	if err != nil {
		return err
	}
	if asJSON {
		b, err := json.MarshalIndent(k, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(b))
		return err
	}
	_, err = fmt.Fprintln(out, k.Key)
	return err
}

// gatewayKeyIssuer shows or sets whether a key may issue short-lived keys
// under itself (POST /v1/magpie/keys with it), each held to it:
//
//	magpie gateway-key issuer <id> [on | off]
func gatewayKeyIssuer(out io.Writer, args []string) error {
	usage := fmt.Errorf("usage: magpie gateway-key issuer <id> [on | off]")
	if len(args) == 0 || len(args) > 2 {
		return usage
	}
	id := args[0]
	if len(args) == 2 {
		if args[1] != "on" && args[1] != "off" {
			return usage
		}
		if _, err := access.Update("issuer-key", access.Change{Key: id, Issues: args[1] == "on"}); err != nil {
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
	if keys[i].Issues {
		_, err = fmt.Fprintf(out, "%s: may issue short-lived keys, each held to its own models, accounts, limits and life\n", keys[i].Name)
	} else {
		_, err = fmt.Fprintf(out, "%s: issues no keys\n", keys[i].Name)
	}
	return err
}

// gatewayKeyOIDC writes the rules a CI job's OIDC token is traded for a
// key by (#4531, package oidc):
//
//	magpie gateway-key oidc [list]
//	magpie gateway-key oidc add --key <id> --repo owner/name [--owner o] [--ref r] [--environment e] [--claim name=value]… [--ttl 1h] [--issuer URL] [--audience aud]
//	magpie gateway-key oidc remove <rule id>
func gatewayKeyOIDC(out io.Writer, args []string) error {
	usage := fmt.Errorf("usage: magpie gateway-key oidc [list] | add --key <id> --repo owner/name [--owner <owner>] [--ref refs/heads/main] [--environment <name>] [--claim name=value]… [--ttl 1h] [--issuer URL] [--audience <aud>] | remove <rule id>")
	if len(args) == 0 || args[0] == "list" {
		if len(args) > 1 {
			return usage
		}
		rules, err := oidc.List()
		if err != nil {
			return err
		}
		if len(rules) == 0 {
			_, err = fmt.Fprintln(out, "No OIDC rules: magpie gateway-key oidc add --key <id> --repo owner/name")
			return err
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tKEY\tTTL\tAUDIENCE\tCLAIMS\tISSUER")
		for _, r := range rules {
			names := slices.Sorted(maps.Keys(r.Claims))
			claims := make([]string, len(names))
			for i, k := range names {
				claims[i] = k + "=" + r.Claims[k]
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Key, r.Lifetime(), r.Audience, strings.Join(claims, " "), r.Issuer)
		}
		return w.Flush()
	}
	switch args[0] {
	case "remove":
		if len(args) != 2 {
			return usage
		}
		if err := oidc.Remove(args[1]); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "OIDC rule removed")
		return err
	case "add":
	default:
		return usage
	}
	r := oidc.Rule{Claims: map[string]string{}}
	rest := args[1:]
	for len(rest) > 0 {
		if len(rest) < 2 || strings.HasPrefix(rest[1], "--") {
			return usage
		}
		flag, v := rest[0], rest[1]
		rest = rest[2:]
		switch flag {
		case "--key":
			r.Key = v
		case "--repo":
			r.Claims["repository"] = v
		case "--owner":
			r.Claims["repository_owner"] = v
		case "--ref":
			r.Claims["ref"] = v
		case "--environment":
			r.Claims["environment"] = v
		case "--claim":
			k, val, ok := strings.Cut(v, "=")
			if !ok {
				return usage
			}
			r.Claims[k] = val
		case "--ttl":
			d, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("--ttl is a duration such as 1h or 30m: %q", v)
			}
			r.TTL = int64(d / time.Second)
			if r.TTL == 0 {
				r.TTL = -1 // refused below as too short, not taken as the default
			}
		case "--issuer":
			r.Issuer = v
		case "--audience":
			r.Audience = v
		default:
			return usage
		}
	}
	if r.Key == "" {
		return usage
	}
	r, err := oidc.Add(r)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "OIDC rule %s: a token from %s with audience %s and %d claim(s) matched gets a key issued under %s, living up to %s.\nPOST it to /v1/magpie/keys/oidc as Authorization: Bearer <token>.\n", r.ID, r.Issuer, r.Audience, len(r.Claims), r.Key, r.Lifetime())
	return err
}

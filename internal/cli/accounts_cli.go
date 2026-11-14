package cli

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// accountsCmd: `magpie accounts [agent] [--json]`, `magpie accounts add <agent>`,
// `magpie accounts switch|forget <agent> <email>`, `magpie accounts switch <claude|codex> next`,
// `magpie accounts project <gemini|antigravity> <email> <project>`,
// `magpie accounts alias <agent> <email> [<name>|--clear]`
// `magpie accounts switch-only <codex|claude> [on|off]`
// — the subscriptions magpie remembers, how much of each one's allowance is
// used, and switching the agent between them.
func accountsCmd(args []string) error {
	if len(args) > 1 && args[1] == "discovery" {
		return accountsDiscoveryCmd(args[2:])
	}
	const usage = "usage: magpie accounts [claude|codex|grok|copilot|gemini|antigravity|zed|factory|mimo|<plugin>] [--json] | magpie accounts add <claude|codex|gemini|antigravity|zed|factory|mimo|<plugin>> | magpie accounts add copilot [--host <name>.ghe.com] | magpie accounts import <codex|claude|antigravity|factory|<plugin>> <file>... [--yes] | magpie accounts import codex <file>... --access-only [--yes] | magpie accounts access-only codex <email> [on|off] | magpie accounts refresh [--json] | magpie accounts checkin [--json] | magpie accounts switch|forget <claude|codex|gemini|antigravity|zed|factory|mimo|<plugin>> <email> | magpie accounts switch <claude|codex> next | magpie accounts switch|forget copilot <login>[@<name>.ghe.com] [--host <name>.ghe.com] | magpie accounts project <gemini|antigravity> <email> <gcp-project-id> | magpie accounts alias <agent|<plugin>> <email> [<name>|--clear] | magpie accounts switch-only <codex|claude> [on|off]"
	agentID := func(s string) (string, error) {
		switch strings.ToLower(s) {
		case "claude", "cc":
			return "claude", nil
		case "codex":
			return "codex", nil
		case "gemini", "gemini-cli":
			return "gemini", nil
		case "antigravity", "ag":
			return "antigravity", nil
		case "zed":
			return "zed", nil
		case "factory", "droid":
			return "factory", nil
		case "mimo", "mimo-app", "xiaomi-mimo":
			return provider.MiMoID, nil
		case "copilot":
			return "copilot", nil
		}
		// a plugin's subscription, by its provider's id or name, as a built-in's
		if pp, err := pluginProvider(context.Background(), s); err == nil {
			return provider.PluginID(pp.ID), nil
		}
		return "", fmt.Errorf("%q: only Claude Code, Codex, Copilot, Gemini CLI, Antigravity, Zed, Factory, Xiaomi MiMo and plugins' accounts can be added and switched\n%s", s, usage)
	}
	if len(args) > 1 && args[1] == "project" {
		if len(args) != 5 {
			return fmt.Errorf("%s", usage)
		}
		id, err := agentID(args[2])
		if err != nil {
			return err
		}
		if err := provider.SetGoogleProject(id, args[3], args[4]); err != nil {
			return err
		}
		if strings.TrimSpace(args[4]) == "" {
			fmt.Println(green.Render("✓"), args[3], "no longer names a Google Cloud project")
		} else {
			fmt.Println(green.Render("✓"), args[3], "now uses Google Cloud project", args[4])
		}
		return nil
	}
	if len(args) > 1 && args[1] == "alias" {
		// a name of the user's own for an account (#1515): magpie's,
		// never written to the vendor's or plugin's sign-in
		if len(args) < 4 {
			return fmt.Errorf("%s", usage)
		}
		id, err := agentID(args[2])
		if err != nil {
			return err
		}
		user, err := accountNamed(id, args[3])
		if err != nil {
			return err
		}
		name := strings.Join(args[4:], " ")
		if name == "--clear" {
			name = ""
		}
		if len(args) == 4 {
			// no name: say what it is
			if a := provider.AccountNameOf(id, user); a != "" {
				fmt.Println(a)
			} else if s := provider.SeatNameOf(id, user); s != "" {
				fmt.Println(s, muted.Render("· the key plan this account stands in for; name it: magpie accounts alias "+args[2]+" "+args[3]+" <name>"))
			} else {
				fmt.Println(muted.Render(user + " has no name of its own · name it: magpie accounts alias " + args[2] + " " + args[3] + " <name>"))
			}
			return nil
		}
		if err := provider.SetAccountName(id, user, name); err != nil {
			return err
		}
		if name = provider.AccountNameOf(id, user); name == "" {
			fmt.Println(green.Render("✓"), user, "no longer has a name of its own")
		} else {
			fmt.Println(green.Render("✓"), user, "is now named", name)
		}
		return nil
	}
	if len(args) > 1 && args[1] == "switch-only" {
		return switchOnlyCmd(args[2:])
	}
	if len(args) > 1 && args[1] == "import" {
		return importAccounts(args[2:])
	}
	if len(args) > 1 && args[1] == "export" {
		return exportAccounts(args[2:])
	}
	if len(args) > 1 && args[1] == "access-only" {
		return accessOnlyCmd(args[2:])
	}
	if len(args) > 1 && args[1] == "refresh" {
		return refreshAccounts(len(args) > 2 && args[2] == "--json")
	}
	if len(args) > 1 && args[1] == "checkin" {
		return checkinWorkBuddy(len(args) > 2 && args[2] == "--json")
	}
	if len(args) > 1 && args[1] == "add" {
		// a Copilot account by GitHub's device code, on github.com or an
		// enterprise's <name>.ghe.com (#723)
		if len(args) >= 3 && strings.EqualFold(args[2], "copilot") {
			var host string
			switch {
			case len(args) == 5 && args[3] == "--host":
				host = args[4]
			case len(args) == 4 && strings.HasPrefix(args[3], "--host="):
				host = strings.TrimPrefix(args[3], "--host=")
			case len(args) != 3:
				return fmt.Errorf("usage: magpie accounts add copilot [--host <name>.ghe.com]")
			}
			h, err := provider.CopilotHost(host)
			if err != nil {
				return err
			}
			if h != "" {
				return addAccount("copilot:" + h)
			}
			return addAccount("copilot")
		}
		if len(args) != 3 {
			return fmt.Errorf("%s", usage)
		}
		id, err := agentID(args[2])
		if err != nil {
			return err
		}
		return addAccount(id)
	}
	if len(args) > 1 && (args[1] == "switch" || args[1] == "forget") {
		// a Copilot account's host, for a login on more than one (#1220)
		host, hasHost := "", false
		for i := 2; i < len(args); i++ {
			if v, ok := strings.CutPrefix(args[i], "--host="); ok {
				host, hasHost = v, true
				args = slices.Delete(slices.Clone(args), i, i+1)
				break
			}
			if args[i] == "--host" && i+1 < len(args) {
				host, hasHost = args[i+1], true
				args = slices.Delete(slices.Clone(args), i, i+2)
				break
			}
		}
		if len(args) != 4 {
			return fmt.Errorf("%s", usage)
		}
		id, err := agentID(args[2])
		if err != nil {
			return err
		}
		if hasHost {
			if id != "copilot" {
				return fmt.Errorf("--host names a Copilot account's host\n%s", usage)
			}
			h, err := provider.CopilotHost(host)
			if err != nil {
				return err
			}
			login, _, _ := strings.Cut(args[3], "@")
			args[3] = provider.CopilotAccountName(login, h)
		}
		over := false
		if args[1] == "switch" && strings.EqualFold(args[3], "next") {
			// the next account in order with room, or the one with the most
			// room left when every one is past its line
			if args[3], over, err = provider.NextInOrder(context.Background(), id); err != nil {
				return err
			}
		} else if args[3], err = accountNamed(id, args[3]); err != nil {
			return err
		}
		if over {
			fmt.Println(muted.Render("every other account is past its line; " + args[3] + " has the most room left"))
		}
		if args[1] == "forget" {
			moved, err := agent.Reseat(func() error { return provider.ForgetLogin(id, args[3]) })
			if err != nil {
				return err
			}
			fmt.Println(green.Render("✓"), "forgot", args[3])
			printMoved(moved)
			return nil
		}
		if err := provider.SwitchLogin(id, args[3]); err != nil {
			return err
		}
		provider.NoteReordered(id)
		if _, plug := provider.PluginOf(id); plug || id == "gemini" || id == "antigravity" || id == "copilot" {
			fmt.Println(green.Render("✓"), "magpie now uses", args[3], "for", id)
			return nil
		}
		if id == "codex" && provider.CodexNoAccount != nil && provider.CodexNoAccount() {
			// Codex's provider takes no ChatGPT account: nothing running
			// goes by it, and nothing needs a restart
			fmt.Println(green.Render("✓"), id, "is now signed in as", args[3], muted.Render("· Codex's provider takes no ChatGPT account, so nothing needs restarting"))
			return nil
		}
		fmt.Println(green.Render("✓"), id, "is now signed in as", args[3], muted.Render("· sessions already running keep their account until restarted"))
		if id == "codex" {
			if was := provider.CodexDaemonStale(); was != "" {
				fmt.Println(" ", "Codex's background service is still signed in as", was+"; restart it to use", args[3]+":", provider.CodexDaemonRestart)
				fmt.Println(" ", muted.Render("running Codex sessions will be interrupted"))
			}
			if was := provider.CodexAppStale(); was != "" {
				fmt.Println(" ", "The Codex app is still signed in as", was+", and shows its limits; quit it and open it again to use", args[3])
			}
		}
		return nil
	}
	which, asJSON := "", false
	for _, a := range args[1:] {
		switch strings.ToLower(a) {
		case "--json":
			asJSON = true
		case "grok", "copilot":
			which = strings.ToLower(a)
		default:
			id, err := agentID(a)
			if err != nil {
				return err
			}
			which = id
		}
	}
	ls := provider.Logins(which)
	rows := accountRows(ls, time.Now())
	if asJSON {
		b, _ := json.MarshalIndent(rows, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(ls) == 0 {
		fmt.Println(muted.Render("no accounts yet ·"), "add one: magpie accounts add <claude|codex|gemini|antigravity>")
		return nil
	}
	// an account named by the user, or for the key plan it stands in
	// for, by that name first (#1515)
	who := func(r accountRow) string {
		if r.Alias != "" {
			return r.Alias + " · " + r.User
		}
		return r.User
	}
	width := 0
	for _, r := range rows {
		width = max(width, lipgloss.Width(who(r)))
	}
	now := time.Now() // one instant for every window's reset time
	for _, r := range rows {
		mark := "  "
		switch {
		case r.Active:
			mark = green.Render("● ")
		case r.On:
			mark = muted.Render("○ ")
		}
		plan := ""
		if r.Plan != "" {
			plan = " · " + r.Plan
		}
		line := fmt.Sprintf("%s%-7s %s %s", mark, r.Agent, pad(who(r), width), muted.Render(fmt.Sprintf("%-8s", plan)))
		for _, w := range r.Windows {
			line += "  " + quotaCell(w, now)
		}
		if r.Balance != "" {
			line += "  " + balanceCell(r.Balance, r.Agent, r.User)
		}
		if r.Resets != nil {
			line += "  " + resetsCell(r.Resets, r.Agent, r.User)
		}
		if r.Lapsed != "" {
			line += "  " + muted.Render(r.Lapsed)
		} else if r.Error != "" {
			line += "  " + muted.Render(r.Error)
		}
		line += quotaReadingCell(r.AsOf, r.ReadAt, r.Windows)
		fmt.Println(line)
	}
	fmt.Println(faint.Render("  ● signed in · ○ takes over when it runs out · add one: magpie accounts add <agent> · switch: magpie accounts switch <agent> <email>"))
	return nil
}

// accountRow is one account and its allowance, as `magpie accounts` shows it.
type accountRow struct {
	Agent   string      `json:"agent"`
	User    string      `json:"user"`
	Plan    string      `json:"plan,omitempty"`
	Active  bool        `json:"active"` // the agent is signed in to it
	On      bool        `json:"on"`     // in use: the active one, or next in line
	Windows []quotaSpan `json:"windows"`
	Error   string      `json:"error,omitempty"`
	Lapsed  string      `json:"lapsed,omitempty"` // its sign-in has to be made again
	AsOf    *time.Time  `json:"asOf,omitempty"`   // last reading when it couldn't be read now
	ReadAt  *time.Time  `json:"readAt,omitempty"` // when what is shown was read, when known
	// Resets are a Codex account's rate-limit resets, when it holds any.
	Resets *provider.ResetCredits `json:"resets,omitempty"`
	// Balance is what the account holds beside its windows, a ChatGPT
	// account's credits, when the vendor tells it.
	Balance string `json:"balance,omitempty"`
	// Alias is the account's name of the user's own, else Seat: the name
	// of the key plan whose card this account's stands in for (#1515).
	Alias string `json:"alias,omitempty"`
	Seat  string `json:"seat,omitempty"`
}

type quotaSpan = provider.QuotaSpan

// accountRows asks each agent's accounts for their allowance at once; what
// was asked less than a minute ago comes from magpie's cache.
func accountRows(ls []provider.Login, now time.Time) []accountRow {
	// the user asking, read even when allowances are read only when asked
	ctx, cancel := context.WithTimeout(provider.Asked(context.Background()), 15*time.Second)
	defer cancel()
	usage := map[string]map[string]provider.SubscriptionQuota{}
	seen := map[string]bool{}
	provider.AskClaudeUsage()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, l := range ls {
		if seen[l.Agent] {
			continue
		}
		seen[l.Agent] = true
		wg.Add(1)
		go func(agent string) {
			defer wg.Done()
			u := provider.LoginUsageShown(ctx, agent)
			mu.Lock()
			usage[agent] = u
			mu.Unlock()
		}(l.Agent)
	}
	wg.Wait()
	rows := []accountRow{}
	for _, l := range ls {
		r := accountRow{Agent: l.Agent, User: l.User, Plan: l.Plan, Active: l.Active, On: l.On, Windows: []quotaSpan{}, Lapsed: l.Lapsed}
		r.Seat = provider.SeatNameOf(l.Agent, l.User)
		r.Alias = cmp.Or(provider.AccountLabelOf(l.Agent, l.User), r.Seat)
		for user, q := range usage[l.Agent] {
			if !strings.EqualFold(user, l.User) {
				continue
			}
			if r.Plan == "" {
				r.Plan = q.Plan
			}
			r.Error, r.Resets, r.AsOf, r.ReadAt, r.Balance = q.Error, q.Resets, q.AsOf, q.ReadAt, q.Balance
			// a pool's own windows stand in for the models' drawing on it,
			// as the usage page shows them
			for _, w := range provider.PooledWindows(q.Windows) {
				s := quotaSpan{Name: w.Name, Used: w.Used, Remaining: max(0, 100-w.Used), ResetsAt: w.ResetsAt, Display: w.Display, Text: w.Text, Pool: w.Pool, Unit: w.Unit, Balance: w.Balance}
				if s.ResetsAt == nil && w.ResetSecs > 0 {
					t := now.Add(time.Duration(w.ResetSecs) * time.Second)
					s.ResetsAt = &t
				}
				r.Windows = append(r.Windows, s)
			}
		}
		rows = append(rows, r)
	}
	return rows
}

// quotaCell is one window in a line as of now: "5h 42% ↻2h13m 14:13".
func quotaCell(w quotaSpan, now time.Time) string {
	name := strings.NewReplacer(" hours", "h", " hour", "h", " days", "d", " day", "d", " · ", " ").Replace(w.Name)
	if w.Unlimited {
		// no cap is not 0% of one: a remote magpie's gateway key held to
		// no limit (#1547) read "Limit 0%"
		return name + " unlimited"
	}
	cell := fmt.Sprintf("%s %.0f%%", name, w.Used)
	if w.Balance != nil {
		// a wallet tells what is left, never a share (#3950)
		cell = name + " " + provider.BalanceText(*w.Balance, w.Unit)
	} else if w.Text {
		// a row of text alone has no share to tell
		cell = name + " " + w.Display
	} else if w.Display != "" {
		cell += " (" + w.Display + ")"
	}
	if w.ResetsAt != nil {
		if !w.ResetsAt.After(now) {
			cell += muted.Render(" · reset time passed " + w.ResetsAt.Local().Format("Jan 2 15:04"))
		} else {
			cell += muted.Render(" ↻" + untilShort(w.ResetsAt.Sub(now)) + " " + provider.ResetClock(*w.ResetsAt, now))
		}
	}
	return cell
}

// quotaReadingCell says when a line was read: "as of … (cached)" for a
// reading kept in place of one that failed, "read 12m ago" for one a
// cache handed back (a Claude account's waits for Claude Code to tell it
// again), nothing for one just read.
func quotaReadingCell(at, readAt *time.Time, ws []quotaSpan) string {
	if at == nil {
		if readAt != nil && time.Since(*readAt) >= 2*time.Minute {
			return "  " + muted.Render("read "+ago(*readAt))
		}
		return ""
	}
	cell := "  " + muted.Render("as of "+at.Local().Format("Jan 2 15:04")+" (cached)")
	if slices.ContainsFunc(ws, func(w quotaSpan) bool { return w.ResetsAt != nil && !w.ResetsAt.After(time.Now()) }) {
		cell += muted.Render(" · expired window; current allowance unknown")
	}
	return cell
}

// untilShort is a time left as "45m", "2h13m" or "3d4h".
func untilShort(d time.Duration) string {
	switch {
	case d <= 0:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}

// addAccount signs in to one more subscription in the browser, the way the
// window's "Add account" does.
func addAccount(agentID string) error {
	if risk := map[string]string{"antigravity": provider.AntigravityRisk, "claude": provider.ClaudeRisk,
		"workbuddy": provider.WorkBuddyRisk, "workbuddy-ai": provider.WorkBuddyRisk}[agentID]; risk != "" {
		fmt.Println(bold.Render("!"), risk)
		fmt.Print("Sign in anyway? [y/N] ")
		var yes string
		fmt.Scanln(&yes)
		if !strings.EqualFold(strings.TrimSpace(yes), "y") && !strings.EqualFold(strings.TrimSpace(yes), "yes") {
			return fmt.Errorf("sign-in canceled")
		}
	}
	if _, plug := provider.PluginOf(agentID); plug || provider.PluginServes(agentID) {
		return pluginLogin(context.Background(), agentID, "")
	}
	if p, _ := claudeCode.downloaded(); agentID == "claude" && runtime.GOOS != "windows" && claudeCode.own() == "" && p == "" {
		// a server or container: the sign-in runs Claude Code, which it
		// has none of; downloading it is offered, and asked (Jorben).
		// Not on Windows, where one in WSL may be the one signed in with.
		fmt.Println(amber.Render("!"), "Claude Code isn't installed here: magpie signs in to Claude, and answers its requests, through it")
		if err := installClaudeCode(false); err != nil {
			return err
		}
	}
	st, err := provider.StartSignIn(agentID)
	if err != nil {
		return err
	}
	if st.State == "installing" {
		fmt.Printf("Installing %s, which %s is used through…\n", st.Installing, agentID)
		for st.State == "installing" {
			time.Sleep(500 * time.Millisecond)
			st, _ = provider.SignInStatus(st.ID)
		}
		if st.State != "waiting" {
			return fmt.Errorf("sign-in didn't finish: %s", st.Error)
		}
	}
	linkOnly := settings.Shared().SignInLinkOnly
	if linkOnly {
		// the user keeps sign-in pages to a browser of their own
		fmt.Println("Open this link in the browser you sign in with:")
	} else {
		fmt.Println("Finish signing in in your browser. If it didn't open, go to:")
	}
	fmt.Println(faint.Render(st.URL))
	if st.Code != "" {
		fmt.Println("and confirm the code", st.Code)
	}
	if !linkOnly {
		openInBrowser(st.URL)
	}
	ctx, stop := interruptContext()
	defer stop()
	if st.PasteCallback || st.PasteCode {
		switch {
		case st.PasteCode:
			fmt.Println("Paste the code the page shows here and press Enter:")
		case st.CallbackBlocked == "reserved":
			// the port ChatGPT comes back to can't be had (#1782)
			fmt.Println(amber.Render("!"), fmt.Sprintf("Windows keeps port %d, where the sign-in comes back, for Hyper-V, WSL or Docker, so the page the browser ends on won't load. Paste its whole address here and press Enter:", st.CallbackPort))
		case st.CallbackBlocked != "":
			fmt.Println(amber.Render("!"), fmt.Sprintf("Another program has port %d, where the sign-in comes back, so the page the browser ends on won't finish it. Paste its whole address here and press Enter:", st.CallbackPort))
		default:
			fmt.Println("If the page the browser ends on won't load (magpie on a server or in Docker), paste its whole address here and press Enter:")
		}
		id := st.ID
		go func() {
			lines := bufio.NewScanner(os.Stdin)
			for lines.Scan() {
				if current, ok := provider.SignInStatus(id); !ok || current.State != "waiting" {
					return
				}
				if err := provider.SubmitSignInCallback(id, lines.Text()); err != nil {
					fmt.Println(err)
					continue
				}
				return
			}
		}()
	}
	st, err = provider.WaitSignIn(ctx, st.ID)
	if err != nil {
		return err
	}
	switch st.State {
	case "done":
		defer sayStillOff(agentID)
		if st.Again {
			fmt.Println(green.Render("✓"), st.User, "is already listed — its sign-in was renewed")
		} else if st.Using {
			fmt.Println(green.Render("✓"), agentID, "is signed in as", st.User)
		} else if strings.HasPrefix(agentID, "copilot") {
			fmt.Println(green.Render("✓"), "added", st.User, muted.Render("· with the other Copilot accounts in magpie's window"))
		} else {
			fmt.Println(green.Render("✓"), "added", st.User, muted.Render("· use it: magpie accounts switch "+agentID+" "+st.User))
		}
		return nil
	case "failed":
		return fmt.Errorf("sign-in didn't finish: %s", st.Error)
	}
	return fmt.Errorf("sign-in canceled")
}

// importUsage is how `magpie accounts import` is run.
const importUsage = "usage: magpie accounts import <codex|claude|antigravity|factory|<plugin>> <file>... [--yes] | magpie accounts import codex <file>... --access-only [--yes]"

// importAccounts: `magpie accounts import <agent> <file>... [--yes]` brings
// in accounts from other tools' files, as the window's "Import accounts from
// a file…" does (#1453): Codex's auth.json, codexbar's config.json, and
// Cockpit Tools', CLIProxyAPI's and Sub2API's exports for ChatGPT; Claude
// Code's .credentials.json, claude-swap's export (cswap --export) and
// CLIProxyAPI's for Claude; Antigravity Cockpit's, Antigravity Manager's
// and CLIProxyAPI's for Antigravity (Factory's API keys go to its plugin,
// with accounts add factory). The files are only
// read. What importing does to the tool the file came from is said first,
// and nothing is imported until that is agreed to (--yes agrees).
func importAccounts(args []string) error {
	yes, accessOnly := false, false
	var rest []string
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			yes = true
		case "--access-only":
			accessOnly = true
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) < 2 {
		return fmt.Errorf("%s", importUsage)
	}
	var id string
	switch strings.ToLower(rest[0]) {
	case "codex", "chatgpt":
		id = "codex"
	case "claude", "cc":
		id = "claude"
	case "antigravity", "ag":
		id = "antigravity"
	case "factory", "droid":
		return fmt.Errorf("Factory runs on its plugin now: add the key with magpie accounts add factory")
	default:
		// a plugin's subscription whose plugin reads such files itself
		// (auth.import): WorkBuddy's sign-in file (marchen_cc on X)
		if provider.PluginImportable(rest[0]) {
			id = rest[0]
			break
		}
		return fmt.Errorf("%q: accounts can be imported for codex, claude, antigravity, factory and plugins that take a file\n%s", rest[0], importUsage)
	}
	if accessOnly && id != "codex" {
		return fmt.Errorf("only ChatGPT accounts can be kept access token only\n%s", importUsage)
	}
	paths := rest[1:]
	files := make([]string, len(paths))
	for i, p := range paths {
		// a file that can't be read stops the import: what it holds isn't
		// known, so it isn't taken for a file with nothing in it
		st, err := os.Stat(p)
		if err != nil {
			return err
		}
		if st.Size() > 4<<20 {
			return fmt.Errorf("%s is too big to be an export", p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[i] = string(b)
	}
	var say []string
	switch {
	case accessOnly:
		say = []string{"Each account is kept with the access token the file has, and magpie never renews its sign-in: the tool it came from (Codex CLI on another computer, codexbar, CLIProxyAPI) stays signed in.",
			"When its access token runs out (about ten days after that sign-in was last renewed), magpie stops using the account until it is imported again.",
			"The files are only read, never changed."}
	case id == "codex":
		say = []string{"Each account's sign-in is refreshed with ChatGPT before it is added, as signing in does.",
			"That spends the file's sign-in: the tool it came from (Codex CLI on another computer, codexbar, Cockpit Tools, CLIProxyAPI) is signed out of that account and has to sign in again. From then on the account is magpie's.",
			"The files are only read, never changed. An account magpie holds already is left as it is."}
	case id == "claude":
		say = []string{provider.ClaudeRisk,
			"Each account is kept as the file has it. Claude Code renews its sign-in the first time it uses it, and the file's copy then stops working: the tool it came from has to sign in again.",
			"The files are only read, never changed."}
	case id == "antigravity":
		say = []string{provider.AntigravityRisk, "Each account's sign-in is refreshed with Google before it is added."}
	default:
		if pp, ok := provider.PluginOf(id); ok && pp.ImportFrom != "" {
			say = []string{pp.ImportFrom}
		}
	}
	for _, s := range say {
		fmt.Println(bold.Render("!"), s)
	}
	if !yes {
		fmt.Print("Import? [y/N] ")
		var answer string
		fmt.Scanln(&answer)
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return fmt.Errorf("import canceled")
		}
	}
	ctx, stop := interruptContext()
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var res []provider.ImportedAccount
	var err error
	switch id {
	case "codex", "claude":
		if accessOnly {
			res, err = provider.ImportCodexAccessOnly(ctx, files)
			break
		}
		res, err = provider.ImportLogins(ctx, id, files)
	case "antigravity":
		res, err = provider.ImportGoogleAccounts(ctx, id, files)
	default:
		res, err = provider.ImportPluginFiles(ctx, id, files)
	}
	if err != nil {
		if len(paths) == 1 {
			return fmt.Errorf("%s: %w", paths[0], err)
		}
		return err
	}
	took := 0
	for _, r := range res {
		who := r.User
		if r.File > 0 && r.File <= len(paths) {
			who = paths[r.File-1]
		}
		switch r.Status {
		case "added":
			took++
			plan := ""
			if r.Plan != "" {
				plan = " · " + r.Plan
			}
			fmt.Println(green.Render("✓"), "added", who+plan)
		case "updated":
			took++
			fmt.Println(green.Render("✓"), who, muted.Render("· in magpie already, now with this sign-in"))
		case "exists":
			took++
			why := "in magpie already with this sign-in, left as it is"
			if r.Error != "" {
				why = r.Error
			}
			fmt.Println(muted.Render("="), who, muted.Render("· "+why))
		default:
			fmt.Println(bold.Render("✗"), who+":", r.Error)
		}
	}
	if took == 0 {
		return fmt.Errorf("nothing imported")
	}
	fmt.Println(faint.Render("  see them: magpie accounts " + id))
	return nil
}

// exportUsage is how `magpie accounts export` is run.
const exportUsage = "usage: magpie accounts export <codex|claude> [<email>...] [--format sub2api|cliproxyapi] [--to <folder>] [--yes]"

// exportAccounts: `magpie accounts export`, the accounts as a file magpie,
// Sub2API or CLIProxyAPI imports. The file is written 0600 into a folder
// (the current one unless --to), never over a file there; its tokens are
// never printed.
func exportAccounts(args []string) error {
	yes, format, to := false, provider.ExportSub2API, "."
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--yes" || a == "-y":
			yes = true
		case a == "--format" || a == "--to":
			if i+1 >= len(args) {
				return fmt.Errorf("%s needs a value\n%s", a, exportUsage)
			}
			i++
			if a == "--to" {
				to = args[i]
			} else {
				format = strings.ToLower(args[i])
			}
		case strings.HasPrefix(a, "--format="):
			format = strings.ToLower(strings.TrimPrefix(a, "--format="))
		case strings.HasPrefix(a, "--to="):
			to = strings.TrimPrefix(a, "--to=")
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("%s: no such flag\n%s", a, exportUsage)
		default:
			rest = append(rest, a)
		}
	}
	if format == "cpa" || format == "cliproxy" {
		format = provider.ExportCLIProxy
	}
	if len(rest) < 1 {
		return fmt.Errorf("%s", exportUsage)
	}
	var id string
	switch strings.ToLower(rest[0]) {
	case "codex", "chatgpt":
		id = "codex"
	case "claude", "cc":
		id = "claude"
	default:
		return fmt.Errorf("%q: accounts can be exported for codex and claude\n%s", rest[0], exportUsage)
	}
	if format != provider.ExportSub2API && format != provider.ExportCLIProxy {
		return fmt.Errorf("%q: the format is sub2api or cliproxyapi\n%s", format, exportUsage)
	}
	// the names are the accounts', as the other account commands take them
	var users []string
	for _, u := range rest[1:] {
		user, err := accountNamed(id, u)
		if err != nil {
			return err
		}
		users = append(users, user)
	}
	fmt.Println(bold.Render("!"), provider.ExportRisk)
	if !yes {
		fmt.Print("Export? [y/N] ")
		var answer string
		fmt.Scanln(&answer)
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return fmt.Errorf("export canceled")
		}
	}
	res, err := provider.ExportLogins(id, format, users)
	for _, s := range res.Skipped {
		fmt.Println(bold.Render("✗"), s.User+":", s.Error)
	}
	if err != nil {
		return err
	}
	paths, err := provider.WriteExport(res, to)
	for _, p := range paths {
		fmt.Println(green.Render("✓"), "wrote", p, muted.Render("(readable by you only)"))
	}
	if err != nil {
		return err
	}
	fmt.Println(faint.Render(fmt.Sprintf("  %d account(s): %s", len(res.Accounts), strings.Join(res.Accounts, ", "))))
	return nil
}

// openInBrowser opens url in the user's browser. It is a variable so the
// tests can have it go nowhere without starting a program (noBrowser).
var openInBrowser = openURLInBrowser

// appLink hands an app's import link over once something here opens it
// (agent.OpenAppLink); tests stand in for the system's answer.
var appLink = agent.OpenAppLink

func openURLInBrowser(url string) {
	// the link is printed beside it, to open by hand when this fails (#2043)
	if err := proc.OpenBrowser(url); err != nil {
		fmt.Fprintln(os.Stderr, "magpie couldn't open the browser:", err)
	}
}

// refreshAccounts: `magpie accounts refresh` renews the sign-in of every
// saved ChatGPT account, and every saved Claude account, on or off (Claude
// Code's /usage run in its own directory), the agent isn't signed in to
// now — what the gateway does every eight days, for a magpie that isn't
// left running (cron, launchd). A sign-in the vendor refuses is marked as
// needing a new one.
func refreshAccounts(asJSON bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rs := provider.RenewLogins(ctx, 0)
	if asJSON {
		b, _ := json.MarshalIndent(rs, "", "  ")
		fmt.Println(string(b))
	}
	failed := 0
	for _, r := range rs {
		if r.Err != "" {
			failed++
		}
		if asJSON {
			continue
		}
		switch {
		case r.Renewed:
			fmt.Println(green.Render("✓"), r.Agent, r.User, muted.Render("renewed"))
		default:
			fmt.Println(muted.Render("✗"), r.Agent, r.User, muted.Render(r.Err))
		}
	}
	if !asJSON && len(rs) == 0 {
		fmt.Println(muted.Render("no saved Claude or ChatGPT accounts besides the ones the agents are signed in to"))
	}
	if !asJSON {
		for _, a := range []string{"claude", "codex"} {
			for _, l := range provider.Logins(a) {
				if l.Active {
					fmt.Println(muted.Render("·"), a, l.User, muted.Render("signed in now · "+a+" renews it itself"))
				}
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d accounts couldn't be renewed", failed, len(rs))
	}
	return nil
}

// checkinWorkBuddy: `magpie accounts checkin` — WorkBuddy's daily check-in
// (签到) for each WorkBuddy (China) account not in yet today, and Trae CN's
// (每日签到) for each Trae CN account, and MiniMax Code's for each MiniMax
// Code account, and Qoder's daily credits for each Qoder account, and each
// plugin's own check-in (auth.checkin) for its accounts, now, and how each
// stands. The
// settings do it on their own once a day.
func checkinWorkBuddy(asJSON bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rs := checkinAll(ctx)
	if rs == nil {
		rs = []provider.WorkBuddyCheckin{}
	}
	if asJSON {
		b, _ := json.MarshalIndent(rs, "", "  ")
		fmt.Println(string(b))
	}
	failed := 0
	for _, r := range rs {
		if r.Outcome == provider.CheckinFailed {
			failed++
		}
		if asJSON {
			continue
		}
		switch r.By {
		case "trae":
			r.User = "Trae CN " + r.User
		case "minimax":
			r.User = "MiniMax Code " + r.User
		case "qoder":
			r.User = "Qoder " + r.User
		default:
			if r.Vendor != "" {
				r.User = strings.TrimSpace(r.Vendor + " " + r.User)
			}
		}
		// an answer that may be the vendor's day before: asked again
		// later (#3326)
		later := ""
		if r.Again {
			later = " · magpie asks again later"
			if r.Outcome == provider.CheckinDone {
				fmt.Println(muted.Render("·"), r.User, muted.Render("says already checked in · its day may not have turned"+later))
				continue
			}
		}
		switch r.Outcome {
		case provider.CheckinClaimed, provider.CheckinDone:
			line := fmt.Sprint(green.Render("✓"), " ", r.User, " checked in today")
			if r.Credit > 0 {
				line += fmt.Sprintf(" +%g", r.Credit)
			}
			if r.Streak > 0 {
				line += muted.Render(fmt.Sprintf(" · a %d-day streak", r.Streak))
			}
			if r.Outcome == provider.CheckinDone || !r.Asked {
				line += muted.Render(" · already")
			}
			fmt.Println(line)
		// why, in the vendor's or plugin's words, beside the label: a bare
		// "no check-in event now" left the user guessing (plugins#304)
		case provider.CheckinIneligible:
			fmt.Println(muted.Render("·"), r.User, muted.Render("not eligible for the check-in"+checkinWhy(r.Msg)+later))
		case provider.CheckinInactive:
			fmt.Println(muted.Render("·"), r.User, muted.Render("no check-in event now"+checkinWhy(r.Msg)+later))
		case provider.CheckinCaptcha:
			fmt.Println(muted.Render("·"), r.User, muted.Render(strings.TrimSpace("asks for a captcha: check in in its own app "+r.Msg)))
		case provider.CheckinOwnApp:
			// Trae CN's 9074 (#808): Trae CN refused the claim (#1920);
			// WorkBuddy's check-in answered 401 while the sign-in works
			if r.By == "trae" {
				fmt.Println(muted.Render("·"), r.User, muted.Render(provider.TraeRefusedWords))
				break
			}
			name := r.Vendor
			if r.By == "" {
				name = "WorkBuddy"
			}
			fmt.Println(muted.Render("·"), r.User, muted.Render(name+" only gives check-in credits to its own app; check in in the "+name+" app"))
		default:
			fmt.Println(muted.Render("✗"), r.User, muted.Render(r.Msg))
		}
	}
	if !asJSON && len(rs) == 0 {
		fmt.Println(muted.Render("no WorkBuddy (China), Trae CN, MiniMax Code or Qoder account is signed in"))
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d accounts couldn't check in", failed, len(rs))
	}
	return nil
}

// checkinAll checks in every account that has a daily check-in: the
// built-ins' and each plugin's; a test's seam.
var checkinAll = func(ctx context.Context) []provider.WorkBuddyCheckin {
	rs := append(provider.CheckInWorkBuddy(ctx), provider.CheckInTrae(ctx)...)
	rs = append(rs, provider.CheckInMiniMax(ctx)...)
	rs = append(rs, provider.CheckInQoder(ctx)...)
	return append(rs, provider.CheckInPlugins(ctx)...)
}

// checkinWhy is why a check-in had nothing to claim, in the vendor's or
// plugin's own words, set after its label (plugins#304); empty without.
func checkinWhy(msg string) string {
	if msg = strings.TrimSpace(msg); msg != "" {
		return " · " + msg
	}
	return ""
}

// accountNamed is the saved account of agent that user names: its name as
// magpie lists it ("a@b.com · Team"), the email alone when only one of the
// agent's accounts has it, or the name the user gave it (alias), when that
// is one account's and no other's email (provider.AccountByName).
func accountNamed(agent, user string) (string, error) {
	user = strings.TrimSpace(user)
	if agent == "copilot" {
		// "mona@github.com" is github.com's mona, named "mona"
		if login, host, ok := strings.Cut(user, "@"); ok {
			if h, err := provider.CopilotHost(host); err == nil {
				user = provider.CopilotAccountName(login, h)
			}
		}
	}
	ls := provider.Logins(agent)
	users := make([]string, 0, len(ls))
	for _, l := range ls {
		users = append(users, l.User)
	}
	if u, err := provider.AccountByName(agent, users, user); err != nil || u != "" {
		return u, err
	}
	var names, same []string
	for _, l := range ls {
		if strings.EqualFold(l.User, user) {
			return l.User, nil
		}
		names = append(names, l.User)
		if email, _, _ := strings.Cut(l.User, " · "); strings.EqualFold(strings.TrimSpace(email), user) {
			same = append(same, l.User)
		} else if login, _, _ := strings.Cut(l.User, "@"); agent == "copilot" && strings.EqualFold(login, user) {
			// a Copilot login alone names its one account on an
			// enterprise's host (#1220)
			same = append(same, l.User)
		}
	}
	switch {
	case len(same) == 1:
		return same[0], nil
	case len(same) > 1 && agent == "copilot":
		return "", fmt.Errorf("copilot has %d accounts of %s: %q — name one in full (<login>@<name>.ghe.com) or add --host <name>.ghe.com", len(same), user, same)
	case len(same) > 1:
		return "", fmt.Errorf("%s has %d accounts of %s: %q — name one in full", agent, len(same), user, same)
	case len(names) == 0:
		return "", fmt.Errorf("no saved %s accounts · add one: magpie accounts add %s", agent, agent)
	}
	return "", fmt.Errorf("no saved %s account %q · its accounts: %q", agent, user, names)
}

// accessOnlyCmd is `magpie accounts access-only codex <email> [on|off]`:
// a saved ChatGPT account kept access token only (EastonXvii on X), so
// magpie never renews its sign-in and another computer signed in to it
// stays signed in. With no on or off it says which it is.
func accessOnlyCmd(args []string) error {
	const usage = "usage: magpie accounts access-only codex <email> [on|off]"
	if len(args) < 2 || len(args) > 3 || !strings.EqualFold(args[0], "codex") {
		return fmt.Errorf("%s", usage)
	}
	user, err := accountNamed("codex", args[1])
	if err != nil {
		return err
	}
	if len(args) == 2 {
		for _, l := range provider.Logins("codex") {
			if l.User == user {
				if l.AccessOnly {
					fmt.Println("on · magpie uses its access token and never renews its sign-in")
				} else {
					fmt.Println("off · magpie renews its sign-in as it runs out")
				}
				return nil
			}
		}
		return fmt.Errorf("no saved codex account %q", user)
	}
	var on bool
	switch strings.ToLower(args[2]) {
	case "on":
		on = true
	case "off":
	default:
		return fmt.Errorf("%s", usage)
	}
	if err := provider.SetLoginAccessOnly("codex", user, on); err != nil {
		return err
	}
	if on {
		fmt.Println(green.Render("✓"), user, "is kept access token only: magpie never renews its sign-in, and stops using it once its access token runs out")
	} else {
		fmt.Println(green.Render("✓"), "magpie renews", user+"'s sign-in again; the place it came from is signed out of it at the next renewal")
	}
	return nil
}

// switchOnlyCmd is `magpie accounts switch-only <codex|claude> [on|off]`:
// magpie only switches the agent's own sign-in to the next account with
// room. Codex's requests are then never sent through the gateway for
// account failover (#1907); Claude's accounts serve no request through the
// gateway (provider.KeptToAgent, myduso on Discord). With no on or off it
// says which it is.
func switchOnlyCmd(args []string) error {
	const usage = "usage: magpie accounts switch-only <codex|claude> [on|off]"
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("%s", usage)
	}
	id := strings.ToLower(args[0])
	if id != "codex" && id != "claude" {
		return fmt.Errorf("%s", usage)
	}
	if len(args) == 1 {
		on := provider.SwitchOnly(id)
		switch {
		case id == "codex" && on:
			fmt.Println("on · magpie only switches Codex's sign-in; its requests go straight to OpenAI")
		case id == "codex":
			fmt.Println("off · with more of its accounts on, Codex's requests go through magpie to fail over between them")
		case on:
			fmt.Println("on · magpie only switches Claude Code's sign-in; it serves no requests on the Claude accounts")
		default:
			fmt.Println("off · magpie serves requests on the Claude accounts that are on")
		}
		return nil
	}
	var on bool
	switch strings.ToLower(args[1]) {
	case "on":
		on = true
	case "off":
	default:
		return fmt.Errorf("%s", usage)
	}
	if err := provider.SetSwitchOnly(id, on); err != nil {
		return err
	}
	// the agents' configs and model lists follow now, not at the next start
	agent.SyncCatalog()
	switch {
	case id == "codex" && on:
		fmt.Println(green.Render("✓"), "magpie only switches Codex's sign-in to an account with room; its requests go straight to OpenAI. A Codex app or session already open stays on its account until it is opened again.")
	case id == "codex":
		fmt.Println(green.Render("✓"), "with more of its accounts on, Codex's requests go through magpie to fail over between them again")
	case on:
		fmt.Println(green.Render("✓"), "magpie only switches Claude Code's sign-in to an account with room; it serves no requests on the Claude accounts. A Claude Code session already open stays on its account until it is started again.")
	default:
		fmt.Println(green.Render("✓"), "magpie serves requests on the Claude accounts that are on again")
	}
	return nil
}

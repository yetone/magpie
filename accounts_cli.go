package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/provider"
)

// accountsCmd: `magpie accounts [agent] [--json]`, `magpie accounts add <agent>`,
// `magpie accounts switch|forget <agent> <email>`,
// `magpie accounts project <gemini|antigravity> <email> <project>`
// — the subscriptions magpie remembers, how much of each one's allowance is
// used, and switching the agent between them.
func accountsCmd(args []string) error {
	const usage = "usage: magpie accounts [claude|codex|grok|copilot|gemini|antigravity|zed|factory|mimo|<plugin>] [--json] | magpie accounts add <claude|codex|gemini|antigravity|zed|factory|mimo|<plugin>> | magpie accounts refresh [--json] | magpie accounts checkin [--json] | magpie accounts switch|forget <claude|codex|gemini|antigravity|zed|factory|mimo|<plugin>> <email> | magpie accounts project <gemini|antigravity> <email> <gcp-project-id>"
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
		}
		// a plugin's subscription, by its provider's id or name, as a built-in's
		if pp, err := pluginProvider(context.Background(), s); err == nil {
			return provider.PluginID(pp.ID), nil
		}
		return "", fmt.Errorf("%q: only Claude Code, Codex, Gemini CLI, Antigravity, Zed, Factory, Xiaomi MiMo and plugins' accounts can be added and switched\n%s", s, usage)
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
	if len(args) > 1 && args[1] == "refresh" {
		return refreshAccounts(len(args) > 2 && args[2] == "--json")
	}
	if len(args) > 1 && args[1] == "checkin" {
		return checkinWorkBuddy(len(args) > 2 && args[2] == "--json")
	}
	if len(args) > 1 && args[1] == "add" {
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
		if len(args) != 4 {
			return fmt.Errorf("%s", usage)
		}
		id, err := agentID(args[2])
		if err != nil {
			return err
		}
		if args[3], err = accountNamed(id, args[3]); err != nil {
			return err
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
		if _, plug := provider.PluginOf(id); plug || id == "gemini" || id == "antigravity" {
			fmt.Println(green.Render("✓"), "magpie now uses", args[3], "for", id)
			return nil
		}
		fmt.Println(green.Render("✓"), id, "is now signed in as", args[3], muted.Render("· sessions already running keep their account until restarted"))
		if id == "codex" {
			if was := provider.CodexDaemonStale(); was != "" {
				fmt.Println(" ", "Codex's background service is still signed in as", was+"; restart it to use", args[3]+":", provider.CodexDaemonRestart)
				fmt.Println(" ", muted.Render("running Codex sessions will be interrupted"))
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
	width := 0
	for _, r := range rows {
		width = max(width, len(r.User))
	}
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
		line := fmt.Sprintf("%s%-7s %-*s %s", mark, r.Agent, width, r.User, muted.Render(fmt.Sprintf("%-8s", plan)))
		for _, w := range r.Windows {
			line += "  " + quotaCell(w)
		}
		if r.Resets != nil {
			line += "  " + resetsCell(r.Resets, provider.AutoResets(r.Agent, r.User))
		}
		if r.Lapsed != "" {
			line += "  " + muted.Render(r.Lapsed)
		} else if r.Error != "" {
			line += "  " + muted.Render(r.Error)
		}
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
	// Resets are a Codex account's rate-limit resets, when it holds any.
	Resets *provider.ResetCredits `json:"resets,omitempty"`
}

type quotaSpan = provider.QuotaSpan

// accountRows asks each agent's accounts for their allowance at once; what
// was asked less than a minute ago comes from magpie's cache.
func accountRows(ls []provider.Login, now time.Time) []accountRow {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	usage := map[string]map[string]provider.SubscriptionQuota{}
	provider.AskClaudeUsage()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, l := range ls {
		if _, ok := usage[l.Agent]; ok {
			continue
		}
		usage[l.Agent] = nil
		wg.Add(1)
		go func(agent string) {
			defer wg.Done()
			u := provider.LoginUsage(ctx, agent)
			mu.Lock()
			usage[agent] = u
			mu.Unlock()
		}(l.Agent)
	}
	wg.Wait()
	rows := []accountRow{}
	for _, l := range ls {
		r := accountRow{Agent: l.Agent, User: l.User, Plan: l.Plan, Active: l.Active, On: l.On, Windows: []quotaSpan{}, Lapsed: l.Lapsed}
		for user, q := range usage[l.Agent] {
			if !strings.EqualFold(user, l.User) {
				continue
			}
			if r.Plan == "" {
				r.Plan = q.Plan
			}
			r.Error, r.Resets = q.Error, q.Resets
			for _, w := range q.Windows {
				s := quotaSpan{Name: w.Name, Used: w.Used, Remaining: max(0, 100-w.Used), ResetsAt: w.ResetsAt, Display: w.Display}
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

// quotaCell is one window in a line: "5h 42% ↻2h13m".
func quotaCell(w quotaSpan) string {
	name := strings.NewReplacer(" hours", "h", " hour", "h", " days", "d", " day", "d", " · ", " ").Replace(w.Name)
	cell := fmt.Sprintf("%s %.0f%%", name, w.Used)
	if w.Display != "" {
		cell += " (" + w.Display + ")"
	}
	if w.ResetsAt != nil {
		cell += muted.Render(" ↻" + untilShort(time.Until(*w.ResetsAt)) + " " + provider.ResetClock(*w.ResetsAt, time.Now()))
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
	if risk := map[string]string{"antigravity": provider.AntigravityRisk, "claude": provider.ClaudeRisk}[agentID]; risk != "" {
		fmt.Println(bold.Render("!"), risk)
		fmt.Print("Sign in anyway? [y/N] ")
		var yes string
		fmt.Scanln(&yes)
		if !strings.EqualFold(strings.TrimSpace(yes), "y") && !strings.EqualFold(strings.TrimSpace(yes), "yes") {
			return fmt.Errorf("sign-in canceled")
		}
	}
	if _, plug := provider.PluginOf(agentID); plug || provider.Moved(agentID) {
		return pluginLogin(context.Background(), agentID, "")
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
	fmt.Println("Finish signing in in your browser. If it didn't open, go to:")
	fmt.Println(faint.Render(st.URL))
	if st.Code != "" {
		fmt.Println("and confirm the code", st.Code)
	}
	openInBrowser(st.URL)
	ctx, stop := interruptContext()
	defer stop()
	if st.PasteCallback || st.PasteCode {
		if st.PasteCode {
			fmt.Println("Paste the code the page shows here and press Enter:")
		} else {
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
		if st.Again {
			fmt.Println(green.Render("✓"), st.User, "is already listed — its sign-in was renewed")
		} else if st.Using {
			fmt.Println(green.Render("✓"), agentID, "is signed in as", st.User)
		} else {
			fmt.Println(green.Render("✓"), "added", st.User, muted.Render("· use it: magpie accounts switch "+agentID+" "+st.User))
		}
		return nil
	case "failed":
		return fmt.Errorf("sign-in didn't finish: %s", st.Error)
	}
	return fmt.Errorf("sign-in canceled")
}

func openInBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = proc.Command("open", url)
	case "windows":
		cmd = proc.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = proc.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// refreshAccounts: `magpie accounts refresh` renews the sign-in of every
// saved Claude and ChatGPT account the agent isn't signed in to now — what
// the gateway does once a day, for a magpie that isn't left running (cron,
// launchd). A sign-in the vendor refuses is marked as needing a new one.
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
// (签到) for each WorkBuddy (China) account not in yet today, now, and how
// each stands. The setting does it on its own once a day.
func checkinWorkBuddy(asJSON bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rs := provider.CheckInWorkBuddy(ctx)
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
		case provider.CheckinIneligible:
			fmt.Println(muted.Render("·"), r.User, muted.Render("not eligible for the check-in"))
		case provider.CheckinInactive:
			fmt.Println(muted.Render("·"), r.User, muted.Render("no check-in event now"))
		default:
			fmt.Println(muted.Render("✗"), r.User, muted.Render(r.Msg))
		}
	}
	if !asJSON && len(rs) == 0 {
		fmt.Println(muted.Render("no WorkBuddy (China) account is signed in"))
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d WorkBuddy accounts couldn't check in", failed, len(rs))
	}
	return nil
}

// accountNamed is the saved account of agent that user names: its name as
// magpie lists it ("a@b.com · Team"), or the email alone when only one of
// the agent's accounts has it.
func accountNamed(agent, user string) (string, error) {
	user = strings.TrimSpace(user)
	ls := provider.Logins(agent)
	var names, same []string
	for _, l := range ls {
		if strings.EqualFold(l.User, user) {
			return l.User, nil
		}
		names = append(names, l.User)
		if email, _, _ := strings.Cut(l.User, " · "); strings.EqualFold(strings.TrimSpace(email), user) {
			same = append(same, l.User)
		}
	}
	switch {
	case len(same) == 1:
		return same[0], nil
	case len(same) > 1:
		return "", fmt.Errorf("%s has %d accounts of %s: %q — name one in full", agent, len(same), user, same)
	case len(names) == 0:
		return "", fmt.Errorf("no saved %s accounts · add one: magpie accounts add %s", agent, agent)
	}
	return "", fmt.Errorf("no saved %s account %q · its accounts: %q", agent, user, names)
}

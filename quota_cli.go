package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

const quotaUsage = `usage: magpie quota [<provider>…] [--json]
       magpie quota reset [<codex account>] [--yes]
       magpie quota auto-reset [<codex account>] [on|off]
       magpie quota credits [<codex account>] [on|off]
       magpie quota alert [<percent>|off] [--balance <amount>|off]
       magpie quota wait <provider|account> [--timeout <duration>] [--quiet]
       magpie quota history [<provider|account>…] [--days <n>] [--json]
  what is left of every subscription, plan and key magpie has: each window's use and
  when it starts again, and each key's balance, asked of the vendors now (or less than
  a minute ago). --json is for scripts and agents, each entry with lastServedAt, when it
  last answered through the gateway, and last: true on the latest; the gateway answers the same at
  GET http://127.0.0.1:3425/v1/magpie/quotas (on Settings' Gateway port), and to another machine only while magpie is
  shared on the local network, with its key as the API key (Authorization: Bearer or x-api-key)
  A Codex account that holds rate-limit resets says how many; quota reset spends one,
  starting the account's current windows again (the one Codex is signed in to unless
  named). It can't be undone, so it asks first; --yes doesn't. quota auto-reset on lets
  the account spend one by itself when its weekly window is used up and no other
  account can take a request, one a week at most (the five hours running out never
  does), and spend the one about to expire about half an hour before it does, if
  the account's windows have been used, so what they have left can be used until
  then and it isn't lost; at once when the account is held up until after then.
  It is the account's standing setting, held resets or not; off stops it, and alone
  it says which accounts have it on. It is off until turned on.
  quota credits off keeps a Codex account from spending its credits: once one of
  its windows is used up, by magpie's latest reading (refreshed about every
  minute), routing holds it as used up until that window renews and the request
  goes to the other accounts, groups and fallbacks, or is refused with why (with
  auto-reset on, its week used up and no one else left, it spends a reset first).
  on, the default, lets the vendor answer on its credits then, so the task goes
  on; alone it says which accounts don't spend theirs.
  quota alert 80 has the magpie app notify when any window of a subscription or plan
  reaches 80% used, once each time the window runs (not windows set aside, such as
  on-demand spending); --balance 5 when a balance falls to 5 or under, in its own
  currency or credits, once until topped up past it. off turns either off, and alone
  it says what is set. Both are off until set.
  quota wait codex returns once any Codex account magpie has on has allowance again
  (a window that stops it no longer used up), and an account's email or login, or
  <provider>/<account>, waits for that one alone: it reads the vendors itself, gateway
  running or not, again shortly after the soonest reset (every 1 to 10 minutes), and
  says on stderr what it waits for. For a script to go on with work it stopped:
  until codex exec …; do magpie quota wait codex || break; done. Exit 0 once there
  is allowance, 1 when --timeout (30m, 6h) passes first, 2 for a name it doesn't
  know, 130 on Ctrl+C.
  quota history is what magpie has read of each subscription's and plan's windows
  over time, kept 45 days (UTC times, a reading only when it changed, each window's
  runs apart by when they reset): the latest readings of each window, or with --json
  every one since --days days ago, as GET /v1/magpie/quotas/history?days=&provider=&user=
  answers. A provider, an account's email or login, or <provider>/<account> narrows it.
  A reading is kept at the time it was read: a Claude account's, which Claude Code
  tells only as it answers or runs /usage, isn't written again each time it is shown.`

// quotaCmd: magpie quota [<provider>…] [--json]
func quotaCmd(args []string) error {
	if len(args) > 1 && args[1] == "reset" {
		return quotaResetCmd(args[2:])
	}
	if len(args) > 1 && args[1] == "auto-reset" {
		return quotaAutoResetCmd(args[2:])
	}
	if len(args) > 1 && args[1] == "credits" {
		return quotaCreditsCmd(args[2:])
	}
	if len(args) > 1 && args[1] == "alert" {
		return quotaAlertCmd(args[2:])
	}
	if len(args) > 1 && args[1] == "wait" {
		return quotaWaitCmd(args[2:])
	}
	if len(args) > 1 && args[1] == "history" {
		return quotaHistoryCmd(args[2:])
	}
	asJSON := false
	var only []string
	for _, a := range args[1:] {
		switch a {
		case "--json", "-j":
			asJSON = true
		case "help", "-h", "--help":
			fmt.Println(quotaUsage)
			return nil
		default:
			only = append(only, strings.ToLower(a))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	qs := []provider.Quota{}
	provider.AskClaudeUsage()
	for _, q := range provider.QuotaReport(ctx, time.Now()) {
		if len(only) == 0 || quotaMatches(q, only) {
			qs = append(qs, q)
		}
	}
	qs, err := withUntold(qs, only)
	if err != nil {
		return err
	}
	if asJSON {
		b, _ := json.MarshalIndent(qs, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(qs) == 0 {
		if len(only) > 0 {
			return fmt.Errorf("no subscription, plan or key balance of %s", strings.Join(only, ", "))
		}
		fmt.Println(muted.Render("nothing to tell ·"), "a signed-in subscription, a coding plan or a key whose vendor tells its balance shows up here")
		return nil
	}
	width := 0
	for _, q := range qs {
		width = max(width, len([]rune(quotaTitle(q))))
	}
	now := time.Now() // one instant for every window's reset time
	for _, q := range qs {
		line := fmt.Sprintf("%-*s  %s", width, quotaTitle(q), muted.Render(fmt.Sprintf("%-12s", q.Kind)))
		for _, w := range q.Windows {
			line += "  " + quotaCell(w, now)
		}
		if q.Balance != "" {
			line += "  " + balanceCell(q.Balance, q.Provider, q.User)
		}
		if q.Resets != nil {
			line += "  " + resetsCell(q.Resets, provider.AutoResets(q.Provider, q.User))
		}
		if q.Error != "" {
			line += "  " + muted.Render(q.Error)
		}
		line += quotaReadingCell(q.AsOf, q.ReadAt, q.Windows)
		fmt.Println(line)
	}
	fmt.Println(faint.Render("  % is how much of a window is used · ↻ when it starts again · --json for scripts, or GET /v1/magpie/quotas on the gateway"))
	return nil
}

// balanceCell is what an account holds in a line: "1.2K credits left",
// and "· not spent" for a Codex account set not to spend its credits.
func balanceCell(balance, id, user string) string {
	cell := bold.Render(balance) + muted.Render(" left")
	if id == "codex" && user != "" && !provider.CodexCredits(user) {
		cell += muted.Render(" · not spent")
	}
	return cell
}

// resetsCell is a Codex account's rate-limit resets in a line: "↺ 2
// resets until Oct 3 14:30", no date when they don't run out, and "· auto"
// when one is spent by itself once the week is used up.
func resetsCell(r *provider.ResetCredits, auto bool) string {
	cell := "↺ " + r.Words()
	if r.Until != nil {
		cell += muted.Render(" until " + provider.ResetClock(*r.Until, time.Now()))
	}
	if auto {
		cell += muted.Render(" · auto")
	}
	return cell
}

// quotaResetCmd: magpie quota reset [<codex account>] [--yes] — spends one
// of the account's rate-limit resets, once the user has said so.
func quotaResetCmd(args []string) error {
	yes, user := false, ""
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			yes = true
		case "help", "-h", "--help":
			fmt.Println(quotaUsage)
			return nil
		default:
			if user != "" {
				return fmt.Errorf("one account at a time: %s or %s", user, a)
			}
			user = a
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// what the account holds, and whose it is when none was named
	var held *provider.SubscriptionQuota
	for _, q := range provider.SubscriptionUsage(ctx) {
		if q.Provider == "codex" && (user == "" || strings.EqualFold(q.User, user)) {
			held = &q
			break
		}
	}
	if held == nil {
		if user != "" {
			return fmt.Errorf("no Codex account %s", user)
		}
		return fmt.Errorf("Codex isn't signed in")
	}
	who := cmp.Or(held.User, "the Codex account")
	if held.Resets == nil && held.Error == "" {
		return fmt.Errorf("%s holds no rate-limit reset", who)
	}
	if !yes {
		n := "one of its resets"
		if held.Resets != nil {
			n = "one of its " + plural(held.Resets.Count, "reset")
		}
		if held.Resets != nil && held.Resets.Until != nil {
			n += " (the one that runs out first, " + provider.ResetClock(*held.Resets.Until, time.Now()) + ")"
		}
		fmt.Printf("Spend %s on %s, starting its current windows again? It can't be undone. [y/N] ", n, who)
		line, _ := stdin.ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return fmt.Errorf("nothing spent")
		}
	}
	out, err := provider.UseCodexReset(ctx, held.User)
	if err != nil {
		return err
	}
	if out.Code != "reset" {
		return fmt.Errorf("%s: %s", who, out.Text())
	}
	fmt.Println(green.Render("✓"), who+":", out.Text())
	return nil
}

// quotaAutoResetCmd: magpie quota auto-reset [<codex account>] [on|off] —
// whether the account spends a reset by itself once its week is used up.
func quotaAutoResetCmd(args []string) error {
	user, set, on := "", false, false
	for _, a := range args {
		switch strings.ToLower(a) {
		case "on", "off":
			set, on = true, strings.EqualFold(a, "on")
		case "help", "-h", "--help":
			fmt.Println(quotaUsage)
			return nil
		default:
			if user != "" {
				return fmt.Errorf("one account at a time: %s or %s", user, a)
			}
			user = a
		}
	}
	if !set && user == "" {
		on := settings.Load().CodexAutoReset
		if len(on) == 0 {
			fmt.Println(muted.Render("no Codex account uses a reset by itself ·"), "magpie quota auto-reset <account> on")
			return nil
		}
		for _, u := range on {
			fmt.Println(green.Render("●"), u, muted.Render("uses a reset by itself when its week runs out, or before one expires"))
		}
		return nil
	}
	user, err := codexAccountNamed(user)
	if err != nil {
		return err
	}
	if !set {
		if provider.CodexAutoReset(user) {
			fmt.Println(user, "uses a reset by itself when its week runs out, or before one expires")
		} else {
			fmt.Println(user, "uses its resets only when told to")
		}
		return nil
	}
	if err := provider.SetCodexAutoReset(user, on); err != nil {
		return err
	}
	if on {
		fmt.Println(green.Render("✓"), user+":", "uses a reset by itself once its week is used up and no other account can answer, one a week at most, and the one about to expire half an hour before it does, or at once when held up past then")
	} else {
		fmt.Println(green.Render("✓"), user+":", "no longer uses a reset by itself")
	}
	return nil
}

// quotaCreditsCmd: magpie quota credits [<codex account>] [on|off] —
// whether the account spends its credits once its allowance is used up.
func quotaCreditsCmd(args []string) error {
	user, set, on := "", false, false
	for _, a := range args {
		switch strings.ToLower(a) {
		case "on", "off":
			set, on = true, strings.EqualFold(a, "on")
		case "help", "-h", "--help":
			fmt.Println(quotaUsage)
			return nil
		default:
			if user != "" {
				return fmt.Errorf("one account at a time: %s or %s", user, a)
			}
			user = a
		}
	}
	if !set && user == "" {
		off := settings.Load().CodexNoCredits
		if len(off) == 0 {
			fmt.Println(muted.Render("every Codex account spends its credits once its allowance is used up ·"), "magpie quota credits <account> off")
			return nil
		}
		for _, u := range off {
			fmt.Println(muted.Render("○"), u, muted.Render("doesn't spend its credits: held once magpie reads a window used up"))
		}
		return nil
	}
	user, err := codexAccountNamed(user)
	if err != nil {
		return err
	}
	if !set {
		if provider.CodexCredits(user) {
			fmt.Println(user, "spends its credits once its allowance is used up")
		} else {
			fmt.Println(user, "doesn't spend its credits: once magpie reads a window used up it is held till it renews")
		}
		return nil
	}
	if err := provider.SetCodexCredits(user, on); err != nil {
		return err
	}
	if on {
		fmt.Println(green.Render("✓"), user+":", "spends its credits once its allowance is used up, so a task goes on")
	} else {
		fmt.Println(green.Render("✓"), user+":", "no longer spends its credits: once magpie reads a window used up, requests go to the other accounts till it renews")
	}
	return nil
}

// codexAccountNamed is the Codex account user names, the one Codex is
// signed in to when "".
func codexAccountNamed(user string) (string, error) {
	if user == "" {
		live, ok := provider.CodexSignedIn()
		if !ok {
			return "", fmt.Errorf("Codex isn't signed in: name the account")
		}
		return live, nil
	}
	if !slices.ContainsFunc(provider.Logins("codex"), func(l provider.Login) bool { return strings.EqualFold(l.User, user) }) {
		if live, ok := provider.CodexSignedIn(); !ok || !strings.EqualFold(live, user) {
			return "", fmt.Errorf("no Codex account %s", user)
		}
	}
	return user, nil
}

// quotaAlertCmd: magpie quota alert [<percent>|off] [--balance <amount>|off]
// — the usage alerts Settings sets (#368), the notifications the app shows.
func quotaAlertCmd(args []string) error {
	s := settings.Load()
	changed := false
	for i := 0; i < len(args); i++ {
		a := strings.ToLower(strings.TrimSpace(args[i]))
		switch {
		case a == "help" || a == "-h" || a == "--help":
			fmt.Println(quotaUsage)
			return nil
		case a == "--balance" || strings.HasPrefix(a, "--balance="):
			v, ok := strings.CutPrefix(a, "--balance=")
			if !ok {
				if i+1 >= len(args) {
					return fmt.Errorf("--balance needs an amount, or off")
				}
				i++
				v = strings.ToLower(strings.TrimSpace(args[i]))
			}
			if v == "off" {
				s.BalanceAlert = 0
			} else {
				n, err := strconv.ParseFloat(v, 64)
				if err != nil || n <= 0 {
					return fmt.Errorf("a balance alert is at an amount over 0, or off, not %q", v)
				}
				s.BalanceAlert = n
			}
			changed = true
		case a == "off":
			s.UsageAlert = 0
			changed = true
		default:
			n, err := strconv.Atoi(strings.TrimSuffix(a, "%"))
			if err != nil || n < 1 || n > 100 {
				return fmt.Errorf("a usage alert is at a percentage from 1 to 100, or off, not %q", args[i])
			}
			s.UsageAlert = n
			changed = true
		}
	}
	if changed {
		if err := settings.Save(s); err != nil {
			return err
		}
		fmt.Print(green.Render("✓") + " ")
	}
	if s.UsageAlert > 0 {
		fmt.Printf("notifies when a window reaches %d%% used, once each time it runs", s.UsageAlert)
	} else {
		fmt.Print("no usage alert")
	}
	if s.BalanceAlert > 0 {
		fmt.Printf("; and when a balance falls to %s or under, once until topped up\n", strconv.FormatFloat(s.BalanceAlert, 'f', -1, 64))
	} else {
		fmt.Println("; no balance alert")
	}
	if s.UsageAlert > 0 || s.BalanceAlert > 0 {
		fmt.Println(muted.Render("  the notifications come from the magpie app, while it runs"))
	}
	return nil
}

// quotaTitle is a quota's line head: its provider, plan and account.
func quotaTitle(q provider.Quota) string {
	t := q.Provider
	if q.Plan != "" {
		t += " · " + q.Plan
	}
	if s := provider.PlanTerm(q.Until, q.Renew); s != "" {
		t += " · " + s
	}
	if q.User != "" {
		t += " · " + q.User
	}
	return t
}

func quotaMatches(q provider.Quota, only []string) bool {
	for _, o := range only {
		if strings.EqualFold(q.Provider, o) || strings.EqualFold(q.Name, o) || strings.EqualFold(q.Kind, o) {
			return true
		}
		// a remote magpie's card: by the remote ("office", or its name)
		// or by the provider there ("codex" is office/codex too)
		if rid, there, ok := strings.Cut(q.Provider, "/"); ok && q.From != "" &&
			(strings.EqualFold(rid, o) || strings.EqualFold(q.From, o) || strings.EqualFold(there, o)) {
			return true
		}
	}
	return false
}

// withUntold adds a line for each provider named that told nothing, saying
// why, rather than leaving it out as if it weren't there; a name that is no
// provider at all is an error.
func withUntold(qs []provider.Quota, only []string) ([]provider.Quota, error) {
	for _, o := range only {
		if o == "subscription" || o == "plan" || o == "balance" || slices.ContainsFunc(qs, func(q provider.Quota) bool { return quotaMatches(q, []string{o}) }) {
			continue
		}
		p, err := provider.Find(o)
		if err != nil {
			return nil, err
		}
		qs = append(qs, provider.Quota{Provider: p.ID, Name: p.Name, Kind: "balance", Windows: []provider.QuotaSpan{},
			Error: "not configured · no balance endpoint known for it; set Balance URL and Balance field in its editor"})
	}
	return qs, nil
}

// quotaHistoryCmd: magpie quota history [<provider|account>…] [--days <n>] [--json]
func quotaHistoryCmd(args []string) error {
	asJSON, days := false, ""
	var only []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--json" || a == "-j":
			asJSON = true
		case a == "--days" && i+1 < len(args):
			i++
			days = args[i]
		case strings.HasPrefix(a, "--days="):
			days = strings.TrimPrefix(a, "--days=")
		case a == "help" || a == "-h" || a == "--help":
			fmt.Println(quotaUsage)
			return nil
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %s\n%s", a, quotaUsage)
		default:
			only = append(only, strings.ToLower(a))
		}
	}
	if days != "" {
		if n, err := strconv.ParseFloat(days, 64); err != nil || n <= 0 {
			return fmt.Errorf("--days takes a number of days, such as 1 or 7, not %q", days)
		}
	}
	now := time.Now()
	hs := []provider.QuotaHistory{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	all := provider.QuotaHistories(provider.QuotaHistorySince(days, now), "", "")
	// and each remote magpie's, as it keeps them (office/codex)
	all = append(all, provider.RemoteQuotaHistories(ctx, days)...)
	for _, h := range all {
		rid, there, _ := strings.Cut(h.Provider, "/")
		if len(only) == 0 || slices.ContainsFunc(only, func(o string) bool {
			return o == h.Provider || o == h.User || o == h.Provider+"/"+h.User || there != "" && (o == rid || o == there || o == there+"/"+h.User)
		}) {
			hs = append(hs, h)
		}
	}
	if asJSON {
		b, _ := json.MarshalIndent(hs, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	if len(hs) == 0 {
		if len(only) > 0 {
			return fmt.Errorf("no readings kept of %s", strings.Join(only, ", "))
		}
		fmt.Println(muted.Render("nothing kept yet ·"), "a subscription's or plan's windows are kept here as magpie reads them")
		return nil
	}
	for _, h := range hs {
		title := h.Provider
		if h.User != "" {
			title += " · " + h.User
		}
		fmt.Println(bold.Render(title))
		for _, l := range h.Lines {
			fmt.Println("  " + l.Name + "  " + quotaPointsCell(l.Points, 8))
		}
	}
	fmt.Println(faint.Render("  % is how much was left · ↻ a new run of the window · --json for every reading, or GET /v1/magpie/quotas/history on the gateway"))
	return nil
}

// quotaPointsCell is a window's last n readings, oldest first: "Oct 4
// 09:12 80% · 10:40 61% · ↻ 14:02 100%", a day only where it changes.
func quotaPointsCell(pts []provider.QuotaPoint, n int) string {
	if len(pts) > n {
		pts = pts[len(pts)-n:]
	}
	cells := []string{}
	day := ""
	for i, p := range pts {
		at := p.At.Local()
		cell := ""
		if i > 0 && quotaNewRun(pts[i-1], p) {
			cell = "↻ "
		}
		if d := at.Format("Jan 2"); d != day {
			day = d
			cell += muted.Render(d+" ") + at.Format("15:04")
		} else {
			cell += at.Format("15:04")
		}
		cells = append(cells, cell+" "+bold.Render(strconv.FormatFloat(p.Left, 'f', -1, 64)+"%"))
	}
	return strings.Join(cells, muted.Render(" · "))
}

// quotaNewRun is whether b is in a run of its window after a's: it resets
// at another time, or has more left with no reset to go by.
func quotaNewRun(a, b provider.QuotaPoint) bool {
	if a.ResetsAt != nil && b.ResetsAt != nil {
		d := b.ResetsAt.Sub(*a.ResetsAt)
		return d > 10*time.Minute || d < -10*time.Minute
	}
	return b.Left > a.Left+5
}

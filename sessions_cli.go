package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/sessions"
	stats "github.com/yetone/magpie/internal/usage"
)

const sessionsUsage = "usage: magpie sessions [--days N|today|all] [--model <model>] [--folder <folder>] [--limit N] [--json]"

// sessionsCmd: `magpie sessions` — the latest sessions of Claude Code and
// Codex; with --days, what they spent day by day, as the app's Sessions
// view shows it.
func sessionsCmd(args []string) error {
	return sessionsTo(os.Stdout, args[1:], time.Now())
}

// sessionsOpts is what the command was asked.
type sessionsOpts struct {
	days          int // -1 for the list
	model, folder string
	limit         int
	json          bool
}

func parseSessionsArgs(args []string) (sessionsOpts, error) {
	o := sessionsOpts{days: -1, limit: 20}
	value := func(i *int) (string, bool) {
		if *i+1 < len(args) && !strings.HasPrefix(args[*i+1], "--") {
			*i++
			return args[*i], true
		}
		return "", false
	}
	for i := 0; i < len(args); i++ {
		a, v, eq := strings.Cut(args[i], "=")
		has := eq
		switch a {
		case "--days", "-d", "stats":
			if !has && a != "stats" {
				v, has = value(&i)
			}
			o.days = 7
			if !has {
				continue
			}
			switch strings.ToLower(v) {
			case "today", "1d":
				o.days = 1
			case "all", "0":
				o.days = 0
			default:
				n, err := strconv.Atoi(strings.TrimSuffix(strings.ToLower(v), "d"))
				if err != nil || n < 0 {
					return o, fmt.Errorf("--days takes a number of days, today or all\n%s", sessionsUsage)
				}
				o.days = n
			}
		case "--model", "-m", "--folder", "-f", "--limit", "-n":
			if !has {
				v, has = value(&i)
			}
			if !has {
				return o, fmt.Errorf("%s takes a value\n%s", a, sessionsUsage)
			}
			switch a {
			case "--model", "-m":
				o.model = v
			case "--folder", "-f":
				o.folder = v
			default:
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 {
					return o, fmt.Errorf("--limit takes a number\n%s", sessionsUsage)
				}
				o.limit = n
			}
		case "--json":
			o.json = true
		case "-h", "--help", "help":
			return o, fmt.Errorf("%s", sessionsUsage)
		default:
			return o, fmt.Errorf("unknown argument %q\n%s", args[i], sessionsUsage)
		}
	}
	return o, nil
}

func sessionsTo(w io.Writer, args []string, now time.Time) error {
	loadCostCurrency()
	o, err := parseSessionsArgs(args)
	if err != nil {
		return err
	}
	if o.days >= 0 {
		return sessionStats(w, o, sessions.StatsAt(o.days, now), now)
	}
	return sessionList(w, o, sessions.List(0), now)
}

// resolve finds what was typed among what there is: the same, then the
// same but for case, then the one whose base name or part is it.
func resolve(kind, typed string, have []string) (string, error) {
	if typed == "" {
		return "", nil
	}
	if kind == "folder" {
		if strings.HasPrefix(typed, "~") {
			if home, err := os.UserHomeDir(); err == nil {
				typed = filepath.Join(home, typed[1:])
			}
		}
		if abs, err := filepath.Abs(typed); err == nil && (strings.ContainsRune(typed, filepath.Separator) || typed == ".") {
			typed = abs
		}
	}
	for _, h := range have {
		if h == typed {
			return h, nil
		}
	}
	for _, match := range []func(h string) bool{
		func(h string) bool { return strings.EqualFold(h, typed) },
		func(h string) bool { return strings.EqualFold(filepath.Base(h), typed) },
		func(h string) bool { return strings.Contains(strings.ToLower(h), strings.ToLower(typed)) },
	} {
		var found []string
		for _, h := range have {
			if match(h) {
				found = append(found, h)
			}
		}
		if len(found) == 1 {
			return found[0], nil
		}
		if len(found) > 1 {
			return "", fmt.Errorf("%q is more than one %s: %s", typed, kind, strings.Join(found, ", "))
		}
	}
	if len(have) == 0 {
		return "", fmt.Errorf("no %s %q: nothing here", kind, typed)
	}
	return "", fmt.Errorf("no %s %q; there are: %s", kind, typed, strings.Join(have, ", "))
}

func agentNames() map[string]string {
	names := map[string]string{}
	for _, a := range agent.All() {
		names[a.ID] = a.Name
	}
	return names
}

// sessionList prints the latest sessions under the filters.
func sessionList(w io.Writer, o sessionsOpts, all []sessions.Session, now time.Time) error {
	var models, folders []string
	seenM, seenF := map[string]bool{}, map[string]bool{}
	for _, s := range all {
		if s.Cwd != "" && !seenF[s.Cwd] {
			seenF[s.Cwd] = true
			folders = append(folders, s.Cwd)
		}
		for _, m := range s.Models {
			if !seenM[m.Model] {
				seenM[m.Model] = true
				models = append(models, m.Model)
			}
		}
	}
	model, err := resolve("model", o.model, models)
	if err != nil {
		return err
	}
	folder, err := resolve("folder", o.folder, folders)
	if err != nil {
		return err
	}
	out := []sessions.Session{}
	for _, s := range all {
		if folder != "" && s.Cwd != folder {
			continue
		}
		if model != "" && !hasModel(s, model) {
			continue
		}
		out = append(out, s)
	}
	more := max(0, len(out)-o.limit)
	out = out[:min(len(out), o.limit)]
	if o.json {
		for i := range out {
			out[i].Path = tildePath(out[i].Path)
		}
		return writeJSON(w, out)
	}
	if len(out) == 0 {
		if len(all) == 0 {
			fmt.Fprintln(w, muted.Render("no sessions yet ·"), "Claude Code's, Codex's, OpenCode's and Pi's sessions on this computer show up here")
			fmt.Fprintln(w, faint.Render("  "+dirList(" · ")))
		} else {
			fmt.Fprintln(w, muted.Render("no session matches"))
		}
		return nil
	}
	names := agentNames()
	type row struct{ when, who, where, title, tokens, cost string }
	rows := make([]row, len(out))
	var wn, ww, wf, wt int
	for i, s := range out {
		c := cost(stats.Totals{Cost: s.Cost, Unpriced: s.Unpriced})
		if s.Cost == 0 && s.Unpriced == 0 && s.Spent() == 0 {
			c = faint.Render("—")
		}
		rows[i] = row{agoAt(s.Last, now), nameOf(names, s.Agent), filepath.Base(s.Cwd), trunc(oneLine(s.Title), 48), fmtTokens(s.Spent()), c}
		wn, ww, wf, wt = max(wn, len(rows[i].when)), max(ww, len(rows[i].who)), max(wf, len(rows[i].where)), max(wt, len(rows[i].tokens))
	}
	for _, r := range rows {
		fmt.Fprintln(w, " ", muted.Render(pad(r.when, wn)), pad(r.who, ww), muted.Render(pad(r.where, wf)), pad(r.title, 48), pad(r.tokens, wt), r.cost)
	}
	head := fmt.Sprintf("latest %d", len(out))
	if more > 0 {
		head += fmt.Sprintf(" · %d more with --limit", more)
	}
	fmt.Fprintln(w, faint.Render("  "+head+" · --days 7 for what they spent by day · --json for the resume commands"))
	return nil
}

func hasModel(s sessions.Session, model string) bool {
	for _, m := range s.Models {
		if m.Model == model {
			return true
		}
	}
	return false
}

// sessionStats prints a range's totals, its days, and its top models and
// folders, under the filters.
func sessionStats(w io.Writer, o sessionsOpts, st sessions.Stats, now time.Time) error {
	every := st.Sum("", "")
	var models, folders []string
	for _, s := range every.Models {
		models = append(models, s.Name)
	}
	for _, s := range every.Folders {
		folders = append(folders, s.Name)
	}
	model, err := resolve("model", o.model, models)
	if err != nil {
		return err
	}
	folder, err := resolve("folder", o.folder, folders)
	if err != nil {
		return err
	}
	r := st.Sum(model, folder)
	if o.json {
		return writeJSON(w, r)
	}
	title := rangeName(o.days)
	if model != "" {
		title += " · " + model
	}
	if folder != "" {
		title += " · " + tildePath(folder)
	}
	if r.Spent() == 0 && r.CacheRead == 0 {
		fmt.Fprintln(w, muted.Render("nothing "+title))
		return nil
	}
	fmt.Fprintln(w, bold.Render(fmtTokens(r.Spent())+" tokens"), muted.Render(title+" ·"), rollupCost(r.Cost, r.Unpriced),
		muted.Render("· cache read")+" "+fmtTokens(r.CacheRead)+hitRate(r.Tokens), muted.Render("· active"), sessions.Duration(r.Active), muted.Render("on "+plural(r.DaysUsed, "day")))
	fmt.Fprintln(w, muted.Render("  in "+fmtTokens(r.Input)+"  out "+fmtTokens(r.Output)+"  cache write "+fmtTokens(r.CacheWrite)+"  "+r.From+" – "+r.To))
	if len(r.Unpriced) > 0 {
		fmt.Fprintln(w, faint.Render("  not priced: "+strings.Join(r.Unpriced, ", ")))
	}

	// the days: every one of a short range, those with anything of a long one
	days := r.Days
	if len(days) > 31 {
		days = nil
		for _, d := range r.Days {
			if d.Spent() > 0 || d.CacheRead > 0 || d.Active > 0 {
				days = append(days, d)
			}
		}
	}
	top := 0
	for _, d := range days {
		top = max(top, d.Spent())
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, faint.Render("  "+pad("date", 14)+pad("tokens", 9)+pad("cost", 10)+pad("cache read", 12)+pad("active", 9)))
	for _, d := range days {
		day, _ := time.ParseInLocation(time.DateOnly, d.Date, time.Local)
		c := faint.Render(pad("—", 9))
		if d.Cost > 0 {
			c = pad(cost(stats.Totals{Cost: d.Cost}), 9)
		}
		fmt.Fprintln(w, " ", muted.Render(pad(day.Format("Mon Jan 2"), 13)), pad(fmtTokens(d.Spent()), 8), c, pad(fmtTokens(d.CacheRead), 11), pad(sessions.Duration(d.Active), 8), green.Render(hbar(d.Spent(), top, 24)))
	}
	if len(days) < len(r.Days) {
		fmt.Fprintln(w, faint.Render(fmt.Sprintf("  %d days without any left out", len(r.Days)-len(days))))
	}

	table := func(head string, ss []sessions.Share, name func(string) string) {
		if len(ss) == 0 {
			return
		}
		fmt.Fprintln(w)
		shown := ss[:min(len(ss), 8)]
		if len(ss) > len(shown) {
			head += fmt.Sprintf(" · top %d of %d", len(shown), len(ss))
		}
		fmt.Fprintln(w, faint.Render("  "+head))
		nw := 0
		for _, s := range shown {
			nw = max(nw, len(name(s.Name)))
		}
		total := 0
		for _, s := range ss {
			total += s.Spent()
		}
		for _, s := range shown {
			fmt.Fprintln(w, " ", pad(name(s.Name), nw), muted.Render(fmt.Sprintf("%3.0f%%", 100*float64(s.Spent())/float64(max(1, total)))), pad(fmtTokens(s.Spent()), 7), rollupCost(s.Cost, nil))
		}
	}
	if model == "" {
		table("models", r.Models, func(s string) string { return s })
	}
	if folder == "" {
		table("folders", r.Folders, func(s string) string {
			if s == "" {
				return "(no folder)"
			}
			return tildePath(s)
		})
	}
	fmt.Fprintln(w, faint.Render("  every session in "+dirList(", ")))
	return nil
}

// dirList is the folders the sessions are read from, as the user would
// type them.
func dirList(sep string) string {
	var out []string
	for _, d := range sessions.Dirs() {
		out = append(out, tildePath(d))
	}
	return strings.Join(out, sep)
}

func rangeName(days int) string {
	switch days {
	case 0:
		return "all time"
	case 1:
		return "today"
	}
	return fmt.Sprintf("last %d days", days)
}

// rollupCost is a list-price cost, + when some models have no price.
func rollupCost(c float64, unpriced []string) string {
	if c == 0 {
		return faint.Render("no price")
	}
	return cost(stats.Totals{Cost: c, Unpriced: len(unpriced)})
}

// hitRate is how much of the prompts came from the cache: of all they
// came to, what was read, written to the cache and neither (Input, which
// leaves out both).
func hitRate(t sessions.Tokens) string {
	if p := t.Input + t.CacheRead + t.CacheWrite; t.CacheRead > 0 && p > 0 {
		return muted.Render(fmt.Sprintf(" (%d%% hit)", 100*t.CacheRead/p))
	}
	return ""
}

// hbar is n of top as a bar of block characters, width wide at most.
func hbar(n, top, width int) string {
	if n <= 0 || top <= 0 {
		return ""
	}
	eighths := max(1, n*width*8/top)
	s := strings.Repeat("█", eighths/8)
	if r := eighths % 8; r > 0 {
		s += string([]rune(" ▏▎▍▌▋▊▉")[r])
	}
	return s
}

// agoAt is how long before now, roughly: 5m ago, 3h ago, 2d ago.
func agoAt(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.In(time.Local).Format("2006-01-02")
}

func nameOf(names map[string]string, id string) string {
	if n := names[id]; n != "" {
		return n
	}
	return id
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

func writeJSON(w io.Writer, v any) error {
	e := json.NewEncoder(w)
	e.SetIndent("", "  ")
	return e.Encode(v)
}

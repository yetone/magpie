package tui

// The sessions page: the agents' sessions (Claude Code's, Codex's,
// OpenCode's, Pi's), as the app's Sessions view shows them — a range, a
// model and a folder to narrow them to; the sessions themselves, the latest
// first, each one resumed with enter; or, with s, what they spent: the
// totals and a chart of the days.

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/usage"
)

// sessRange is a range the page offers: its name and days (0 for all).
type sessRange struct {
	name string
	days int
}

var sessRanges = []sessRange{{"today", 1}, {"7 days", 7}, {"30 days", 30}, {"90 days", 90}, {"all", 0}}

const allModels, allFolders = "all models", "all folders"

// sessFilterMsg is a model or folder picked, "" for every one.
type sessFilterMsg struct {
	folder bool
	value  string
}

func (m *model) reloadSessions() {
	m.sstats = sessions.StatsFor(sessRanges[m.srange].days)
	// the whole set, as the page's count beside it is taken from: the list
	// head says how many there are, and the range filter narrows what is
	// shown of them. sessions.Limit is the window gateway attribution is
	// judged over, not a listing's length, so this asks for sessions.All
	m.slist = sessions.List(sessions.All)
	m.ssel = clamp(m.ssel, len(m.sessShown()))
}

// sessShown is the sessions listed: those active in the range, under the
// model and folder picked.
func (m model) sessShown() []sessions.Session {
	return sessFilter(m.slist, sessRanges[m.srange].days, m.smodel, m.sfolder, time.Now())
}

func sessFilter(all []sessions.Session, days int, model, folder string, now time.Time) []sessions.Session {
	var since time.Time
	if days > 0 {
		now = now.In(time.Local)
		since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 1-days)
	}
	var out []sessions.Session
	for _, s := range all {
		if s.Last.Before(since) || folder != "" && s.Cwd != folder {
			continue
		}
		if model != "" && !slices.ContainsFunc(s.Models, func(u sessions.Model) bool { return u.Model == model }) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func (m model) updateSessions(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(sessRanges)
	if !m.sstat {
		shown := m.sessShown()
		switch msg.String() {
		case "j", "down":
			m.ssel = min(m.ssel+1, max(0, len(shown)-1))
			return m, nil
		case "k", "up":
			m.ssel = max(0, m.ssel-1)
			return m, nil
		case "enter":
			if m.ssel < len(shown) {
				return m, resumeSession(shown[m.ssel])
			}
			return m, nil
		}
	}
	switch msg.String() {
	case "s", "tab":
		m.sstat = !m.sstat
		return m, nil
	case "l", "right":
		m.srange = (m.srange + 1) % n
	case "h", "left":
		m.srange = (m.srange + n - 1) % n
	case "t":
		m.srange = 0
	case "w":
		m.srange = 1
	case "m":
		m.srange = 2
	case "A":
		m.srange = n - 1
	case "c":
		m.scost = !m.scost
		return m, nil
	case "M":
		m.openSessPick(false)
		return m, nil
	case "f":
		m.openSessPick(true)
		return m, nil
	case "x":
		m.smodel, m.sfolder, m.ssel = "", "", 0
		return m, nil
	default:
		return m, nil
	}
	m.ssel = 0
	m.reloadSessions()
	return m, nil
}

// resumeSession runs a session's resume command in this terminal, the
// page back when the agent quits.
func resumeSession(s sessions.Session) tea.Cmd {
	if s.Resume == "" {
		return func() tea.Msg { return flashMsg{text: "this session can't be resumed from here"} }
	}
	// the shell's own "no such directory" is gone with the screen it was on
	if fi, err := os.Stat(s.Cwd); s.Cwd != "" && (err != nil || !fi.IsDir()) {
		return func() tea.Msg { return flashMsg{text: "its folder " + tildePath(s.Cwd) + " isn't there any more"} }
	}
	c := proc.Command("sh", "-c", s.Resume)
	if runtime.GOOS == "windows" {
		c = proc.Command("powershell", "-NoProfile", "-Command", s.Resume)
	}
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return flashMsg{text: "resume: " + err.Error()}
		}
		return flashMsg{text: "back from the session", ok: true}
	})
}

// openSessPick picks a model, or a folder, of those the range has under
// the other filter.
func (m *model) openSessPick(folder bool) {
	r := m.sstats.Sum(m.smodel, m.sfolder)
	all, shares, crumb := allModels, r.Models, "model"
	if folder {
		all, shares, crumb = allFolders, r.Folders, "folder"
	}
	items := []agent.Option{{Value: all}}
	for _, s := range shares {
		if s.Name != "" {
			items = append(items, agent.Option{Value: s.Name, Note: fmtTokens(s.Spent()) + " tokens"})
		}
	}
	m.pk = picker{
		crumbs: []string{"sessions", crumb},
		input:  newInput("filter"),
		items:  items,
		onPick: func(v string) tea.Cmd {
			if v == all {
				v = ""
			}
			return func() tea.Msg { return sessFilterMsg{folder, v} }
		},
	}
	m.pk.refilter()
	m.mode = modePick
	m.back = modeList
}

func (m model) viewSessions() string {
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n\n")
	if m.sstat {
		b.WriteString(strings.Join(sessLines(m.sstats, m.srange, m.smodel, m.sfolder, m.scost, m.w-len(pad)-2, m.h), "\n"))
	} else {
		b.WriteString(strings.Join(sessListLines(m.sessShown(), m.agents, m.srange, m.smodel, m.sfolder, m.ssel, m.w-len(pad)-2, m.h, time.Now()), "\n"))
	}
	return strings.TrimRight(b.String(), "\n")
}

// sessBar is the page's first line: the ranges, the model and the folder
// picked, and which of the two views this is.
func sessBar(rng int, model, folder string, stats bool) string {
	var tabs []string
	for i, r := range sessRanges {
		if i == rng {
			tabs = append(tabs, sPill.Render(r.name))
		} else {
			tabs = append(tabs, sMuted.Render(" "+r.name+" "))
		}
	}
	filter := func(v, all string) string {
		if v == "" {
			return sMuted.Render(all)
		}
		return sNameOn.Render(v)
	}
	view := sNameOn.Render("sessions") + sFaint.Render(" · stats")
	if stats {
		view = sFaint.Render("sessions · ") + sNameOn.Render("stats")
	}
	return strings.Join(tabs, " ") + "    " + filter(model, allModels) + sFaint.Render(" · ") + filter(tildePath(folder), allFolders) + "    " + view
}

// sessListLines is the sessions view below the header: the bar, then the
// sessions in the range, the latest first, as many as fit around the one
// picked.
func sessListLines(list []sessions.Session, agents []*agent.Agent, rng int, model, folder string, sel, width, height int, now time.Time) []string {
	var out []string
	add := func(s string) { out = append(out, pad+"  "+s) }
	add(sessBar(rng, model, folder, false))
	add("")
	if len(list) == 0 {
		if model != "" || folder != "" {
			add(sMuted.Render("no session matches · x clears the filters"))
		} else {
			add(sMuted.Render("nothing in this range · Claude Code's, Codex's, OpenCode's and Pi's sessions on this computer show up here"))
		}
		return out
	}
	names := map[string]string{}
	for _, a := range agents {
		names[a.ID] = a.Name
	}
	type row struct{ when, who, where, tokens, cost, title string }
	rows := make([]row, len(list))
	var wn, ww, wf, wt int
	for i, s := range list {
		c := sFaint.Render("—")
		if s.Cost > 0 {
			c = sOK.Render(fmtCost(usage.Totals{Cost: s.Cost, Unpriced: s.Unpriced}))
		} else if s.Spent() > 0 {
			c = sFaint.Render("no price")
		}
		where := filepath.Base(s.Cwd)
		if s.Cwd == "" {
			where = "—"
		}
		title := strings.Join(strings.Fields(s.Title), " ")
		if title == "" {
			title = "(untitled)"
		}
		rows[i] = row{sessAgo(s.Last, now), cmp.Or(names[s.Agent], s.Agent), trunc(where, 24), fmtTokens(s.Spent()), c, title}
		wn, ww, wf, wt = max(wn, len(rows[i].when)), max(ww, lipgloss.Width(rows[i].who)), max(wf, lipgloss.Width(rows[i].where)), max(wt, len(rows[i].tokens))
	}
	visible := max(3, height-11) // the header, the bar, the arrows, the command and the footer around them
	start := 0
	if sel >= visible-1 {
		start = sel - visible + 2
	}
	start = max(0, min(start, len(list)-visible))
	if start > 0 {
		add(sFaint.Render(fmt.Sprintf("↑ %d more", start)))
	}
	for i := start; i < len(list) && i < start+visible; i++ {
		r := rows[i]
		marker, who := "  ", sText.Render(padRight(r.who, ww))
		if i == sel {
			marker, who = sCursor.Render("▸ "), sNameOn.Render(padRight(r.who, ww))
		}
		line := marker + sMuted.Render(padRight(r.when, wn)) + "  " + who + "  " + sText.Render(padRight(r.where, wf)) + "  " + sText.Render(padRight(r.tokens, wt)) + "  " + r.cost
		if room := width - lipgloss.Width(line) - 2; room > 8 {
			line += "  " + sMuted.Render(trunc(r.title, room))
		}
		out = append(out, pad+line)
	}
	if rest := len(list) - start - visible; rest > 0 {
		add(sFaint.Render(fmt.Sprintf("↓ %d more", rest)))
	}
	if sel < len(list) && list[sel].Resume != "" {
		add("")
		add(sFaint.Render(trunc("↵ runs "+list[sel].Resume, max(20, width-2))))
	}
	return out
}

// sessAgo is how long before now, roughly: 5m ago, 3h ago, 2d ago.
func sessAgo(t, now time.Time) string {
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

// sessLines is the stats view below the header: the bar, the totals, the
// chart and the top models and folders, each line indented.
func sessLines(st sessions.Stats, rng int, model, folder string, byCost bool, width, height int) []string {
	var out []string
	add := func(s string) { out = append(out, pad+"  "+s) }
	add(sessBar(rng, model, folder, true))
	add("")

	r := st.Sum(model, folder)
	if r.Spent() == 0 && r.CacheRead == 0 {
		if model != "" || folder != "" {
			add(sMuted.Render("no session matches · x clears the filters"))
		} else {
			add(sMuted.Render("nothing in this range · Claude Code's, Codex's, OpenCode's and Pi's sessions on this computer show up here"))
		}
		return out
	}
	c := sFaint.Render("no price")
	if r.Cost > 0 {
		c = sOK.Render(fmtCost(usage.Totals{Cost: r.Cost, Unpriced: len(r.Unpriced)}))
	}
	hit := ""
	// of all the prompts came to: Input leaves out what was read from the
	// cache and what was written to it
	if p := r.Input + r.CacheRead + r.CacheWrite; r.CacheRead > 0 && p > 0 {
		hit = sMuted.Render(fmt.Sprintf(" (%d%% hit)", 100*r.CacheRead/p))
	}
	active := sessions.Duration(r.Active)
	if r.Active >= 0 {
		active += sMuted.Render(fmt.Sprintf(" on %d day%s", r.DaysUsed, plural(r.DaysUsed)))
	} else {
		active += sMuted.Render(" not kept by model")
	}
	add(sName.Render(fmtTokens(r.Spent())+" tokens") + sMuted.Render(" · ") + c + sMuted.Render(" · cache read ") + sText.Render(fmtTokens(r.CacheRead)) + hit + sMuted.Render(" · active ") + sText.Render(active))
	add(sMuted.Render("in " + fmtTokens(r.Input) + "  out " + fmtTokens(r.Output) + "  cache write " + fmtTokens(r.CacheWrite)))
	add("")

	chart := sessChart(r.Days, byCost, width, 6)
	for _, l := range chart {
		add(l)
	}
	if len(chart) > 0 {
		add("")
	}

	room := max(2, (height-18-len(chart))/2)
	table := func(head string, ss []sessions.Share, name func(string) string) {
		if len(ss) == 0 {
			return
		}
		add(sFaint.Render(head))
		w := 0
		for _, s := range ss[:min(len(ss), room)] {
			w = max(w, lipgloss.Width(name(s.Name)))
		}
		total := 0
		for _, s := range ss {
			total += s.Spent()
		}
		for i, s := range ss {
			if i == room {
				add(sFaint.Render(fmt.Sprintf("and %d more · f and M pick one", len(ss)-room)))
				break
			}
			sc := sFaint.Render("no price")
			if s.Cost > 0 {
				sc = sOK.Render(fmtCost(usage.Totals{Cost: s.Cost}))
			}
			add(sText.Render(padRight(name(s.Name), w)) + "  " + sMuted.Render(fmt.Sprintf("%3.0f%%", 100*float64(s.Spent())/float64(max(1, total)))) + "  " + sText.Render(padRight(fmtTokens(s.Spent()), 7)) + "  " + sc)
		}
		add("")
	}
	if model == "" {
		table("models", r.Models, func(s string) string { return s })
	}
	if folder == "" {
		table("folders", r.Folders, func(s string) string {
			if s == "" {
				return "(no folder)"
			}
			return trunc(tildePath(s), 48)
		})
	}
	return out
}

// sessChart is the days as a bar chart of block characters, height rows
// tall and at most width wide: a column a day, or a week (or more) when
// the days don't fit; its peak on the right, its first and last day under
// it. Nothing for a range of one day.
func sessChart(days []sessions.DayTotal, byCost bool, width, height int) []string {
	if len(days) < 2 || height < 1 {
		return nil
	}
	const label = 10 // room for the peak on the right
	cols := max(8, width-label)
	step := 1
	for (len(days)+step-1)/step > cols {
		step++
	}
	if step > 1 && step < 7 && (len(days)+6)/7 <= cols {
		step = 7
	}
	type bucket struct {
		date  string
		value float64
	}
	var bs []bucket
	for i := 0; i < len(days); i += step {
		b := bucket{date: days[i].Date}
		for _, d := range days[i:min(len(days), i+step)] {
			if byCost {
				b.value += d.Cost
			} else {
				b.value += float64(d.Spent())
			}
		}
		bs = append(bs, b)
	}
	peak := 0.0
	for _, b := range bs {
		peak = max(peak, b.value)
	}
	if peak == 0 {
		return nil
	}
	// a gap between the columns while there is room for it
	gap := ""
	if 2*len(bs) <= cols {
		gap = " "
	}
	fill := []rune(" ▁▂▃▄▅▆▇█")
	lines := make([]string, height)
	for row := range height {
		level := height - 1 - row
		var sb strings.Builder
		for i, b := range bs {
			if i > 0 {
				sb.WriteString(gap)
			}
			eighths := int(b.value * float64(height*8) / peak)
			if b.value > 0 {
				eighths = max(1, eighths)
			}
			sb.WriteRune(fill[min(8, max(0, eighths-level*8))])
		}
		lines[row] = sCursor.Render(sb.String())
	}
	top := fmtTokens(int(peak))
	if byCost {
		top = fmtCost(usage.Totals{Cost: peak})
	}
	lines[0] += "  " + sFaint.Render(top)
	unit := "a day"
	if step == 7 {
		unit = "a week"
	} else if step > 1 {
		unit = fmt.Sprintf("%d days", step)
	}
	lines[height-1] += "  " + sFaint.Render(map[bool]string{false: "tokens", true: "cost"}[byCost]+" · "+unit)
	span := len(bs) + (len(bs)-1)*len(gap)
	first, last := shortDate(bs[0].date), shortDate(bs[len(bs)-1].date)
	under := first
	if n := span - len(first) - len(last); n > 0 {
		under += strings.Repeat(" ", n) + last
	}
	return append(lines, sFaint.Render(under))
}

func shortDate(d string) string {
	t, err := time.ParseInLocation(time.DateOnly, d, time.Local)
	if err != nil {
		return d
	}
	return t.Format("Jan 2")
}

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

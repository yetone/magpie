package tui

// The pages beside the agents: providers (keys, models, balances) and
// usage — what the app's Providers and Usage views do, in the terminal.
// Routing is in routing.go, sessions in sessions.go, the library in
// library.go.

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/fx"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
	"github.com/yetone/magpie/internal/usage"
)

type page int

const (
	pageAgents page = iota
	pageProviders
	pageGroups
	pageUsage
	pageSessions
	pageLibrary
)

var pageNames = []string{"agents", "providers", "routing", "usage", "sessions", "library"}

// ask is a line to type: a key, a family, a group's name.
type ask struct {
	crumbs  []string
	hint    string
	input   textinput.Model
	empty   bool // enter with nothing typed is an answer too
	onEnter func(string) tea.Cmd
}

// askMsg opens a line to type, from a step that comes before it.
type askMsg struct{ a ask }

// balanceMsg is what is left on each provider's key, by id.
type balanceMsg map[string]string

// ---- providers --------------------------------------------------------------

func (m *model) reloadProviders() {
	m.provs = provider.All()
	m.provsErr = provider.FileError()
	m.prow = clamp(m.prow, len(m.provs))
}

// balancesCmd asks every vendor it can what is left on its key.
func balancesCmd(ps []provider.Provider) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out := balanceMsg{}
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, p := range ps {
			wg.Add(1)
			go func() {
				defer wg.Done()
				amount, ok, err := provider.Balance(ctx, p)
				if !ok {
					return
				}
				v := amount
				if err != nil {
					v = "balance: " + err.Error()
					if r := []rune(v); len(r) > 60 {
						v = string(r[:60]) + "…"
					}
				}
				mu.Lock()
				out[p.ID] = v
				mu.Unlock()
			}()
		}
		wg.Wait()
		return out
	}
}

func (m model) updateProviders(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.provs)
	key := msg.String()
	if key != "d" {
		m.confirm = ""
	}
	switch key {
	case "j", "down":
		if n > 0 {
			m.prow = (m.prow + 1) % n
		}
		return m, nil
	case "k", "up":
		if n > 0 {
			m.prow = (m.prow + n - 1) % n
		}
		return m, nil
	case "a":
		m.openPresets()
		return m, nil
	case "b":
		m.flash, m.flashOK = "asking the vendors for balances…", true
		return m, balancesCmd(m.provs)
	}
	if n == 0 {
		return m, nil
	}
	p := m.provs[m.prow]
	switch key {
	case "enter", " ":
		m.openProviderModels(p.ID)
	case "e":
		if p.Account != nil {
			m.flash, m.flashOK = p.Name+" is a signed-in account: its key is the agent's sign-in", false
			return m, nil
		}
		in := newInput("the API key")
		in.EchoMode = textinput.EchoPassword
		in.EchoCharacter = '•'
		m.openAsk(ask{crumbs: []string{"providers", p.Name, "key"}, input: in,
			hint: "now " + dash(provider.Mask(p.Key)) + " · the key is kept in magpie's providers file",
			onEnter: func(v string) tea.Cmd {
				return saveProvider(p.ID, func(p *provider.Provider) { p.Key = v; provider.ForgetBalances() }, p.Name+" key "+provider.Mask(v))
			}})
	case "f":
		in := newInput("a tag, e.g. relay")
		in.SetValue(p.Family)
		m.openAsk(ask{crumbs: []string{"providers", p.Name, "family"}, input: in, empty: true,
			hint: "agents can be shown only some families: magpie visible <agent> <family>",
			onEnter: func(v string) tea.Cmd {
				return saveProvider(p.ID, func(p *provider.Provider) { p.Family = v }, p.Name+" family "+dash(v))
			}})
	case "u":
		on := !p.Unlisted
		what := "its models are listed"
		if on {
			what = "serves only through routing groups"
		}
		return m, saveProvider(p.ID, func(p *provider.Provider) { p.Unlisted = on }, p.Name+" "+what)
	case "o":
		off := !p.Off
		what := "switched on"
		if off {
			what = "switched off: agents are given none of its models"
		}
		id := p.ID
		return m, reseatCmd(func() error { return provider.SetOff(id, off) }, p.Name+" "+what)
	case "t":
		m.flash, m.flashOK = "testing "+p.Name+"…", true
		return m, testCmd(p)
	case "d":
		if m.confirm != "provider/"+p.ID {
			m.confirm = "provider/" + p.ID
			m.flash, m.flashOK = "press d again to remove "+p.Name, false
			return m, nil
		}
		m.confirm = ""
		id := p.ID
		return m, reseatCmd(func() error { return provider.Delete(id) }, "removed "+p.Name)
	}
	return m, nil
}

// reseatCmd makes a change that may take models away from the agents on
// them, and says which it moved to others (agent.Reseat).
func reseatCmd(change func() error, done string) tea.Cmd {
	return func() tea.Msg {
		moved, err := agent.Reseat(change)
		if err != nil {
			return flashMsg{text: err.Error()}
		}
		for _, mv := range moved {
			if mv.Error != "" {
				done += "; " + mv.String()
				continue
			}
			done += "; moved " + mv.String()
		}
		return flashMsg{text: done, ok: true}
	}
}

// saveProvider changes one provider and saves it.
func saveProvider(id string, change func(*provider.Provider), done string) tea.Cmd {
	return func() tea.Msg {
		p, err := provider.Find(id)
		if err != nil {
			return flashMsg{text: err.Error()}
		}
		change(p)
		if err := provider.Save(*p); err != nil {
			return flashMsg{text: err.Error()}
		}
		return flashMsg{text: done, ok: true}
	}
}

func testCmd(p provider.Provider) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var parts []string
		ok := true
		for _, r := range p.Test(ctx) {
			if r.OK {
				parts = append(parts, fmt.Sprintf("%s ✓ %d ms", r.Protocol, r.Millis))
				continue
			}
			ok = false
			e := r.Error
			if r.Status != 0 {
				e = fmt.Sprintf("%d %s", r.Status, r.Error)
			}
			if rs := []rune(e); len(rs) > 80 {
				e = string(rs[:80]) + "…"
			}
			parts = append(parts, fmt.Sprintf("%s ✗ %s", r.Protocol, e))
		}
		if len(parts) == 0 {
			return flashMsg{text: p.Name + ": nothing to test"}
		}
		return flashMsg{text: p.Name + ": " + strings.Join(parts, " · "), ok: ok}
	}
}

// modelOptions are a provider's models, those agents are shown marked.
func modelOptions(id string) []agent.Option {
	p, err := provider.Find(id)
	if err != nil {
		return nil
	}
	on := map[string]bool{}
	for _, x := range p.Exposed() {
		on[x.ID] = true
	}
	var out []agent.Option
	seen := map[string]bool{}
	names := p.ModelNames()
	add := func(id, name string) {
		if seen[id] {
			return
		}
		seen[id] = true
		note := "○ off"
		if on[id] {
			note = "● on"
		}
		if n, ok := names[id]; ok {
			name = n // the user's (magpie model name)
		}
		if name != "" && name != id {
			note += " · " + name
		}
		out = append(out, agent.Option{Value: id, Note: note})
	}
	for _, x := range p.Exposed() {
		add(x.ID, x.Name)
	}
	for _, x := range p.Available() {
		add(x.ID, x.Name)
	}
	return out
}

// openProviderModels lists a provider's models; enter turns one on or off
// for agents, and the list stays open.
func (m *model) openProviderModels(id string) {
	p := m.provs[m.prow]
	m.pk = picker{
		crumbs: []string{"providers", p.Name, "models"},
		input:  newInput("filter models"),
		items:  modelOptions(id),
		empty:  "no models known yet · t tests it, which asks the vendor for them",
		toggle: func(model string) ([]agent.Option, string, bool) {
			p, err := provider.Find(id)
			if err != nil {
				return nil, err.Error(), false
			}
			ids := p.Models
			if len(ids) == 0 {
				for _, x := range p.Exposed() {
					ids = append(ids, x.ID)
				}
			}
			verb := "on"
			if i := slices.Index(ids, model); i >= 0 {
				if len(ids) == 1 {
					return nil, "one model has to stay on", false
				}
				ids = slices.Delete(slices.Clone(ids), i, i+1)
				verb = "off"
			} else {
				ids = append(slices.Clone(ids), model)
			}
			p.Models = ids
			if err := provider.Save(*p); err != nil {
				return nil, err.Error(), false
			}
			items := modelOptions(id)
			if !slices.ContainsFunc(items, func(o agent.Option) bool { return o.Value == model }) {
				// a vendor whose list wasn't fetched knows only those on:
				// the one just turned off stays to be turned on again
				items = append(items, agent.Option{Value: model, Note: "○ off"})
			}
			return items, model + " " + verb, true
		},
	}
	m.pk.refilter()
	m.mode = modePick
}

// openPresets picks a vendor magpie knows, then asks for its key.
func (m *model) openPresets() {
	var items []agent.Option
	for _, d := range provider.Presets() {
		items = append(items, agent.Option{Value: d.ID, Note: d.Name + " · " + string(d.Kind)})
	}
	m.pk = picker{
		crumbs: []string{"providers", "add"},
		input:  newInput("a vendor magpie knows (magpie provider add <name> url=… for another)"),
		items:  items,
		onPick: func(id string) tea.Cmd {
			return func() tea.Msg {
				p, err := provider.FromPreset(id)
				if err != nil {
					return flashMsg{text: err.Error()}
				}
				in := newInput("the API key")
				in.EchoMode = textinput.EchoPassword
				in.EchoCharacter = '•'
				hint := "the key is kept in magpie's providers file"
				if p.KeysURL != "" {
					hint = "keys: " + p.KeysURL
				}
				return askMsg{ask{crumbs: []string{"providers", "add", p.Name}, input: in, hint: hint, empty: true,
					onEnter: func(key string) tea.Cmd {
						return func() tea.Msg {
							p.Key = key
							id, err := provider.Add(p)
							if err != nil {
								return flashMsg{text: err.Error()}
							}
							ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
							defer cancel()
							text := "added " + p.Name
							if saved, err := provider.Find(id); err == nil {
								if ms, err := saved.Fetch(ctx); err == nil {
									text += fmt.Sprintf(" · %d models", len(ms))
								}
							}
							return flashMsg{text: text, ok: true}
						}
					}}}
			}
		},
	}
	m.pk.refilter()
	m.mode = modePick
}

func (m model) viewProviders() string {
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n\n")
	if m.provsErr != nil {
		b.WriteString(pad + "  " + sBad.Render("! "+m.provsErr.Error()) + "\n\n")
	}
	if len(m.provs) == 0 {
		if m.provsErr == nil {
			b.WriteString(pad + "  " + sMuted.Render("no providers yet · a adds one"))
		}
		return b.String()
	}
	type row struct{ name, id, key, models, note string }
	var rows []row
	var w [4]int
	for _, p := range m.provs {
		r := row{name: p.Name, id: p.ID}
		switch {
		case p.Off:
			r.key = "○ switched off"
		case p.Account != nil:
			r.key = "● " + p.Account.User
		case p.Key != "":
			r.key = "● " + provider.Mask(p.Key)
		case p.Ready():
			r.key = "● no key needed"
		default:
			r.key = "○ no key"
		}
		r.models = fmt.Sprintf("%d models", len(p.Exposed()))
		var notes []string
		if v, ok := m.bal[p.ID]; ok {
			notes = append(notes, v)
		}
		if p.Family != "" {
			notes = append(notes, "family "+p.Family)
		}
		if p.Unlisted {
			notes = append(notes, "groups only")
		}
		r.note = strings.Join(notes, " · ")
		for i, s := range []string{r.name, r.id, r.key, r.models} {
			w[i] = max(w[i], lipgloss.Width(s))
		}
		rows = append(rows, r)
	}
	visible := max(3, m.h-7)
	start := 0
	if m.prow >= visible {
		start = m.prow - visible + 1
	}
	for i := start; i < min(len(rows), start+visible); i++ {
		r := rows[i]
		marker, name := "  ", sName.Render(padRight(r.name, w[0]))
		if i == m.prow {
			marker, name = sCursor.Render("▸ "), sNameOn.Render(padRight(r.name, w[0]))
		}
		key := sMuted.Render(padRight(r.key, w[2]))
		if strings.HasPrefix(r.key, "○") {
			key = sBad.Render(padRight(r.key, w[2]))
		}
		b.WriteString(pad + marker + name + "  " + sFaint.Render(padRight(r.id, w[1])) + "  " + key + "  " + sText.Render(padRight(r.models, w[3])) + "  " + sMuted.Render(r.note) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---- usage ------------------------------------------------------------------

var periods = []usage.Period{usage.Today, usage.Week, usage.Month, usage.All}

var periodNames = map[usage.Period]string{usage.Today: "today", usage.Week: "7 days", usage.Month: "30 days", usage.All: "all time"}

// quotaMsg is what is left of every subscription, plan bought with a key
// and key balance, as the app's Usage page shows them.
type quotaMsg []provider.SubscriptionQuota

func quotasCmd() tea.Msg {
	provider.AskClaudeUsage() // the page opened, or r pressed
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	return quotaMsg(provider.Quotas(ctx))
}

func (m model) updateUsage(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	i := slices.Index(periods, m.period)
	switch msg.String() {
	case "u":
		m.qleft = !m.qleft
		return m, nil
	case "l", "right":
		m.period = periods[(i+1)%len(periods)]
	case "h", "left":
		m.period = periods[(i+len(periods)-1)%len(periods)]
	case "t":
		m.period = usage.Today
	case "w":
		m.period = usage.Week
	case "m":
		m.period = usage.Month
	case "A":
		m.period = usage.All
	default:
		return m, nil
	}
	m.sum = usage.Summarize(m.period)
	return m, nil
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

// costCurrency is Settings' currency choice and, for cny, the CNY-per-USD
// rate to show costs at (internal/fx), refreshed at most once a second so
// a page redrawn on every keypress doesn't reread the settings file or
// touch the rate's own cache for each row fmtCost renders.
var (
	costMu      sync.Mutex
	costAt      time.Time
	costCncy    string
	costRateVal float64
)

func costCurrency() (string, float64) {
	costMu.Lock()
	defer costMu.Unlock()
	if time.Since(costAt) < time.Second {
		return costCncy, costRateVal
	}
	costCncy = settings.Load().Currency
	costRateVal = 0
	if costCncy == "cny" {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		costRateVal = fx.Get(ctx).CNYPerUSD
		cancel()
	}
	costAt = time.Now()
	return costCncy, costRateVal
}

func fmtCost(t usage.Totals) string {
	if t.Cost == 0 && t.Unpriced > 0 {
		return "no price"
	}
	cncy, rate := costCurrency()
	s := usage.FormatCost(t.Cost, cncy, rate)
	if t.Unpriced > 0 {
		s += "+"
	}
	return "≈" + s
}

var bars = []rune(" ▁▂▃▄▅▆▇█")

func (m model) viewUsage() string {
	s := m.sum
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n\n")
	var tabs []string
	for _, p := range periods {
		if p == s.Period {
			tabs = append(tabs, sPill.Render(periodNames[p]))
		} else {
			tabs = append(tabs, sMuted.Render(" "+periodNames[p]+" "))
		}
	}
	b.WriteString(pad + "  " + strings.Join(tabs, " ") + "\n\n")
	ql := quotaLines(m.quotas, m.qasked, m.qleft, m.w-len(pad)-2, time.Now())
	for _, l := range ql {
		b.WriteString(pad + "  " + l + "\n")
	}
	if len(ql) > 0 {
		b.WriteString("\n")
	}
	if s.Calls == 0 {
		b.WriteString(pad + "  " + sMuted.Render("no calls in this time · route an agent through magpie and its usage shows up here"))
		return b.String()
	}
	line := sName.Render(fmtTokens(s.Tokens())+" tokens") + sMuted.Render(fmt.Sprintf(" · %d call%s · ", s.Calls, plural(s.Calls))) + sOK.Render(fmtCost(s.Totals))
	if s.Errors > 0 {
		line += sMuted.Render(" · ") + sBad.Render(fmt.Sprintf("%d error%s", s.Errors, plural(s.Errors)))
	}
	b.WriteString(pad + "  " + line + "\n")
	b.WriteString(pad + "  " + sMuted.Render("in "+fmtTokens(s.Input)+"  out "+fmtTokens(s.Output)+"  cache read "+fmtTokens(s.CacheRead)+"  cache write "+fmtTokens(s.CacheWrite)+"  reasoning "+fmtTokens(s.Reasoning)) + "\n\n")

	// the timeline, one bar a bucket
	top := 0
	for _, p := range s.Series {
		top = max(top, p.Tokens())
	}
	if top > 0 && len(s.Series) > 0 {
		var sb strings.Builder
		for _, p := range s.Series {
			i := 0
			if p.Tokens() > 0 {
				i = max(1, p.Tokens()*(len(bars)-1)/top)
			}
			sb.WriteRune(bars[i])
		}
		first, last := s.Series[0].Label, s.Series[len(s.Series)-1].Label
		b.WriteString(pad + "  " + sCursor.Render(sb.String()) + "  " + sFaint.Render(first+" – "+last) + "\n\n")
	}

	names := map[string]string{}
	for _, a := range agent.All() {
		names[a.ID] = a.Name
	}
	room := max(2, (m.h-16-len(ql))/2)
	table := func(head string, gs []usage.Group, name func(usage.Group) string) {
		b.WriteString(pad + "  " + sFaint.Render(head) + "\n")
		w := 0
		for _, g := range gs {
			w = max(w, lipgloss.Width(name(g)))
		}
		for i, g := range gs {
			if i == room {
				b.WriteString(pad + "  " + sFaint.Render(fmt.Sprintf("and %d more · magpie usage", len(gs)-room)) + "\n")
				break
			}
			share := fmt.Sprintf("%3.0f%%", 100*float64(g.Tokens())/float64(max(1, s.Tokens())))
			b.WriteString(pad + "  " + sText.Render(padRight(name(g), w)) + "  " + sMuted.Render(share) + "  " + sText.Render(padRight(fmtTokens(g.Tokens()), 7)) + "  " + sFaint.Render(padRight(fmt.Sprintf("%d call%s", g.Calls, plural(g.Calls)), 10)) + "  " + sOK.Render(fmtCost(g.Totals)) + "\n")
		}
		b.WriteString("\n")
	}
	table("agents", s.Agents, func(g usage.Group) string {
		if n := names[g.ID]; n != "" {
			return n
		}
		return g.ID
	})
	table("models", s.Models, func(g usage.Group) string { return g.ID })
	return strings.TrimRight(b.String(), "\n")
}

// quotaLines is the accounts' allowances, a line each as the app's cards
// are: the subscriptions' and plans' windows as small meters — how much is
// used, or left, with the vendor's own count before it where it gives one
// — and the keys' balances; nil when there is nothing to tell.
func quotaLines(qs []provider.SubscriptionQuota, asked, left bool, width int, now time.Time) []string {
	if qs == nil {
		if !asked {
			return nil
		}
		return []string{sFaint.Render("accounts"), sMuted.Render("asking the vendors what is left…")}
	}
	if len(qs) == 0 {
		return nil
	}
	title := func(q provider.SubscriptionQuota) (plain, styled string) {
		plain, styled = q.Name, sName.Render(q.Name)
		if q.Plan != "" {
			plain += " · " + q.Plan
			styled += sMuted.Render(" · " + q.Plan)
		}
		if t := provider.PlanTerm(q.Until, q.Renew); t != "" {
			plain += " · " + t
			styled += sFaint.Render(" · " + t)
		}
		if q.User != "" {
			plain += " · " + q.User
			styled += sFaint.Render(" · " + q.User)
		}
		return
	}
	tw := 0
	for _, q := range qs {
		p, _ := title(q)
		tw = max(tw, lipgloss.Width(p))
	}
	tw = min(tw, 44)
	head := "accounts · % is how much is used"
	if left {
		head = "accounts · % is how much is left"
	}
	out := []string{sFaint.Render(head)}
	for _, q := range qs {
		p, t := title(q)
		if lipgloss.Width(p) > tw {
			t = sName.Render(trunc(p, tw))
		}
		line := padRight(t, tw)
		switch {
		case q.Balance != "":
			out = append(out, line+"  "+sText.Render(q.Balance)+sMuted.Render(" left"))
			continue
		case q.Error != "":
			out = append(out, line+"  "+sMuted.Render(trunc(q.Error, max(20, width-tw-2))))
			continue
		case len(q.Windows) == 0:
			out = append(out, line+"  "+sMuted.Render("no usage reported"))
			continue
		}
		// the windows follow the name, those that don't fit on lines below
		// it, and a Codex account's resets after them
		var cells []string
		for _, w := range provider.PooledWindows(q.Windows) {
			cells = append(cells, quotaCell(w, left, now))
		}
		if r := q.Resets; r != nil {
			c := sText.Render("↺ " + r.Words())
			if r.Until != nil {
				c += sFaint.Render(" until " + provider.ResetClock(*r.Until, now))
			}
			if provider.AutoResets(q.Provider, q.User) {
				c += sFaint.Render(" · auto") // spent by itself once the week is used up
			}
			cells = append(cells, c)
		}
		at := tw
		for _, c := range cells {
			cw := lipgloss.Width(c)
			if at > tw && width > 0 && at+3+cw > width {
				out = append(out, line)
				line, at = strings.Repeat(" ", tw), tw
			}
			line += "   " + c
			at += 3 + cw
		}
		out = append(out, line)
	}
	return out
}

// quotaCell is one window: its name, a meter, how much is used or left,
// and when it starts again: on the clock, and how long until then.
func quotaCell(w provider.QuotaWindow, left bool, now time.Time) string {
	if w.Unlimited {
		return sMuted.Render(w.Name) + " " + sText.Render("Unlimited")
	}
	used := int(math.Round(math.Max(0, math.Min(100, w.Used))))
	n, word := used, "used"
	if left {
		n, word = 100-used, "left"
	}
	const cells = 8
	on := (n*cells + 50) / 100
	if n > 0 {
		on = max(1, on)
	}
	fill := sCursor
	if used >= 90 {
		fill = sBad
	}
	pct := fmt.Sprintf("%d%% %s", n, word)
	if w.Display != "" {
		pct = w.Display + " · " + pct
	}
	c := sMuted.Render(w.Name) + " " + fill.Render(strings.Repeat("█", on)) + sFaint.Render(strings.Repeat("░", cells-on)) + " " + sText.Render(pct)
	var at time.Time
	if w.ResetsAt != nil {
		at = *w.ResetsAt
	} else if w.ResetSecs > 0 {
		at = now.Add(time.Duration(w.ResetSecs) * time.Second)
	}
	if !at.IsZero() {
		c += sFaint.Render(" ↻ " + provider.ResetClock(at, now) + " (" + until(at.Sub(now)) + ")")
	}
	return c
}

// until is how long until then, roughly: 40m, 5h, 3d.
func until(d time.Duration) string {
	mins := max(1, int(math.Round(d.Minutes())))
	h := int(math.Round(float64(mins) / 60))
	switch {
	case mins < 60:
		return fmt.Sprintf("%dm", mins)
	case h < 48:
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dd", int(math.Round(float64(h)/24)))
}

// ---- a line to type ---------------------------------------------------------

func (m *model) openAsk(a ask) {
	m.ask = a
	m.mode = modeAsk
	m.back = modeList
}

func (m model) updateAsk(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = m.back
		return m, nil
	case "enter":
		v := strings.TrimSpace(m.ask.input.Value())
		if v == "" && !m.ask.empty {
			return m, nil
		}
		m.mode = m.back
		return m, m.ask.onEnter(v)
	}
	var cmd tea.Cmd
	m.ask.input, cmd = m.ask.input.Update(msg)
	return m, cmd
}

func (m model) viewAsk() string {
	var b strings.Builder
	b.WriteString(m.header(m.ask.crumbs...))
	b.WriteString("\n\n")
	b.WriteString(pad + sCursor.Render("❯ ") + m.ask.input.View())
	if m.ask.hint != "" {
		b.WriteString("\n")
		for _, line := range strings.Split(m.ask.hint, "\n") {
			b.WriteString("\n" + pad + "  " + sMuted.Render(line))
		}
	}
	return b.String()
}

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
	case "w":
		pr := provider.Preset(p.Preset)
		if pr == nil || pr.Endpoint == "" {
			m.flash, m.flashOK = p.Name+" is asked at its vendor's address · magpie provider set "+p.ID+" url=… changes a custom one's", false
			return m, nil
		}
		now := p.Chat
		if now == "" {
			now = p.Responses
		}
		if p.IsRemoteMagpie() {
			now = p.Anthropic // the address as typed, without /v1
		}
		id, name := p.ID, p.Name
		m.openAsk(endpointAsk(*pr, []string{"providers", name, "address"}, now, func(v string) tea.Cmd {
			return saveProvider(id, func(p *provider.Provider) {
				p.Chat, p.Responses = v, v
				if p.IsRemoteMagpie() {
					p.Anthropic = "" // put again from the address typed
				}
				provider.ForgetBalances()
			}, name+" address "+v)
		}))
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
	case "m":
		// the vendor's list asked again, as the app editor's Refresh does
		m.flash, m.flashOK = "asking "+p.Name+" for its models…", true
		return m, refetchCmd(p.ID)
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

// refetchCmd asks the provider's vendor for its model list again and says
// how many it has and how many agents are offered, or why there is none:
// the app editor's Refresh, which the TUI had no way to do (akic404 on
// Discord: a provider added here had 0 models and nothing to fetch them).
func refetchCmd(id string) tea.Cmd {
	return func() tea.Msg {
		p, err := provider.Find(id)
		if err != nil {
			return flashMsg{text: err.Error()}
		}
		if p.IsPlugin() {
			return flashMsg{text: p.Name + "'s models are what its plugin lists"}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ms, dropped, err := p.Refetch(ctx)
		if err != nil {
			return flashMsg{text: p.Name + ": " + fetchNote(err)}
		}
		text := p.Name + ": " + modelsNote(id, len(ms))
		if len(dropped) > 0 {
			text += " · gone from its list, so no longer picked: " + strings.Join(dropped, ", ")
		}
		return flashMsg{text: text, ok: len(ms) > 0}
	}
}

// modelsNote says how many models the vendor's list had and how many of
// them agents are offered.
func modelsNote(id string, fetched int) string {
	text := fmt.Sprintf("%d models from its list", fetched)
	if p, err := provider.Find(id); err == nil {
		text += fmt.Sprintf(" · %d for agents (↵ picks)", len(p.Exposed()))
	}
	return text
}

// fetchNote is why a model list wasn't had, short enough for the status
// line: the first URL's answer.
func fetchNote(err error) string {
	e, _, _ := strings.Cut(err.Error(), "; ")
	if r := []rune(e); len(r) > 140 {
		e = string(r[:140]) + "…"
	}
	return "no model list · " + strings.TrimPrefix(e, "no model list: ") + " · m asks again"
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
		empty:  "no models known yet · m on the providers list asks the vendor for them",
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
	var shown []string
	for _, d := range provider.Partners() {
		items = append(items, agent.Option{Value: d.ID, Note: d.Name + " · partner (sponsor)"})
		shown = append(shown, d.ID)
	}
	go func() {
		provider.CountPartner(provider.PartnerShown, shown...)
		provider.NoticePartners(shown...)
	}()
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
				// a vendor reached at the user's own address (a remote
				// magpie, Azure OpenAI) has none of the preset's: it is
				// asked for first, as the app's editor asks it
				if pr := provider.Preset(id); pr != nil && pr.Endpoint != "" {
					return askMsg{endpointAsk(*pr, []string{"providers", "add", p.Name, "address"}, "", func(addr string) tea.Cmd {
						return func() tea.Msg {
							p.Chat, p.Responses = addr, addr
							return askMsg{addKeyAsk(p)}
						}
					})}
				}
				return askMsg{addKeyAsk(p)}
			}
		},
	}
	m.pk.refilter()
	m.mode = modePick
}

// addKeyAsk asks for the key of p, a preset's provider, and adds it.
func addKeyAsk(p provider.Provider) ask {
	in := newInput("the API key")
	in.EchoMode = textinput.EchoPassword
	in.EchoCharacter = '•'
	hint := "the key is kept in magpie's providers file"
	if p.KeysURL != "" {
		hint = "keys: " + p.KeysURL
	}
	return ask{crumbs: []string{"providers", "add", p.Name}, input: in, hint: hint, empty: true,
		onEnter: func(key string) tea.Cmd {
			return func() tea.Msg {
				p.Key = key
				id, err := provider.Add(p)
				if err != nil {
					return flashMsg{text: err.Error()}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				saved, err := provider.Find(id)
				if err != nil {
					return flashMsg{text: err.Error()}
				}
				// the vendor's list, and what came of asking it: a list
				// that failed said so, not an "added" over 0 models
				// (akic404 on Discord)
				text := "added " + saved.Name
				ms, err := saved.Fetch(ctx)
				if err != nil && saved.DecideOnly() {
					// a System One API: its list isn't what it is for
					return flashMsg{text: text, ok: true}
				}
				if err != nil {
					return flashMsg{text: text + " · " + fetchNote(err)}
				}
				return flashMsg{text: text + " · " + modelsNote(id, len(ms)), ok: len(ms) > 0}
			}
		}}
}

// endpointAsk asks for the address of a vendor reached at the user's own
// (pr.Endpoint is its example): a remote magpie's, as the other
// computer's magpie shows it in Settings, under Share on local network.
// Nothing typed is said to be needed, as the app's editor says it.
func endpointAsk(pr provider.PresetDef, crumbs []string, now string, then func(string) tea.Cmd) ask {
	in := newInput(pr.Endpoint)
	in.SetValue(now)
	hint := pr.EndpointHint
	if hint == "" {
		hint = "the address it is reached at, e.g. " + pr.Endpoint
	}
	return ask{crumbs: crumbs, input: in, hint: hint, empty: true,
		onEnter: func(v string) tea.Cmd {
			if v == "" {
				need := pr.EndpointNeeded
				if need == "" {
					need = "Your resource's endpoint is needed"
				}
				return func() tea.Msg { return flashMsg{text: need} }
			}
			return then(v)
		}}
}

// listError is provider.Provider.ListError; tests stand in for it.
var listError = provider.Provider.ListError

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
	type row struct{ name, id, key, models, note, warn string }
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
		// a plugin's account showing its defaults alone says why, as the
		// app's editor does (gnayiab on X: Cursor in WSL's TUI had Auto
		// alone and nothing said)
		if e := listError(p); e != "" {
			r.warn = "couldn't list its models: " + e
		}
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
		line := pad + marker + name + "  " + sFaint.Render(padRight(r.id, w[1])) + "  " + key + "  " + sText.Render(padRight(r.models, w[3])) + "  " + sMuted.Render(r.note)
		if r.warn != "" {
			if r.note != "" {
				line += sMuted.Render(" · ")
			}
			line += sBad.Render(r.warn)
		}
		b.WriteString(line + "\n")
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
	// a WorkBuddy (China) account's line says how its daily check-in went,
	// as its card in the app does
	return quotaMsg(provider.WithCheckins(provider.Quotas(ctx)))
}

// checkinMsg is what came of c, WorkBuddy's daily check-in pressed now.
type checkinMsg struct {
	text string
	ok   bool
}

// The check-in; vars so tests can stand in for WorkBuddy and Trae CN.
var (
	checkinHere  = provider.CheckInWorkBuddy
	hasWorkBuddy = provider.HasWorkBuddy
	checkinTrae  = provider.CheckInTrae
	hasTrae      = provider.HasTrae
	checkinMM    = provider.CheckInMiniMax
	hasMiniMax   = provider.HasMiniMax
	checkinQd    = provider.CheckInQoder
	hasQoder     = provider.HasQoder
	checkinPl    = func(ctx context.Context) []provider.WorkBuddyCheckin { return provider.CheckInPlugins(ctx) }
	hasPlugin    = provider.HasPluginCheckin
)

// checkinCmd presses WorkBuddy's daily check-in (签到) now for every
// WorkBuddy (China) account signed in here not in yet today, all at once,
// as the app's Usage card's "Check in now" and magpie accounts checkin do:
// the built-in's or the plugin's (akic404 on Discord: the TUI had no way
// to); and Trae CN's (每日签到) for each Trae CN account (#694), and
// MiniMax Code's for each MiniMax Code account (#811), and Qoder's daily
// credits for each Qoder account, and each plugin's own check-in
// (auth.checkin) for its accounts. It says
// how each account stands: the credits and streak, in
// already today, or why not. A shared magpie's accounts are checked in
// on that magpie, from its own app, TUI or CLI.
func checkinCmd() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var parts []string
	failed := 0
	if hasWorkBuddy() {
		for _, r := range checkinHere(ctx) {
			if r.Outcome == provider.CheckinFailed {
				failed++
			}
			parts = append(parts, checkinWords(r))
		}
	}
	if hasTrae() {
		for _, r := range checkinTrae(ctx) {
			if r.Outcome == provider.CheckinFailed {
				failed++
			}
			parts = append(parts, checkinWords(r))
		}
	}
	if hasMiniMax() {
		for _, r := range checkinMM(ctx) {
			if r.Outcome == provider.CheckinFailed {
				failed++
			}
			parts = append(parts, checkinWords(r))
		}
	}
	if hasQoder() {
		for _, r := range checkinQd(ctx) {
			if r.Outcome == provider.CheckinFailed {
				failed++
			}
			parts = append(parts, checkinWords(r))
		}
	}
	if hasPlugin() {
		for _, r := range checkinPl(ctx) {
			if r.Outcome == provider.CheckinFailed {
				failed++
			}
			parts = append(parts, checkinWords(r))
		}
	}
	if len(parts) == 0 {
		return checkinMsg{text: "no WorkBuddy (China), Trae CN, MiniMax Code, Qoder or check-in plugin account is signed in · only they have the daily check-in"}
	}
	return checkinMsg{text: strings.Join(parts, "; "), ok: failed == 0}
}

// checkinWords is how an account's check-in stands, in a few words.
func checkinWords(r provider.WorkBuddyCheckin) string {
	who := r.User
	switch {
	case r.By == "trae" && who == "":
		who = "Trae CN"
	case r.By == "trae":
		who = "Trae CN " + who
	case r.By == "minimax" && who == "":
		who = "MiniMax Code"
	case r.By == "minimax":
		who = "MiniMax Code " + who
	case r.By == "qoder" && who == "":
		who = "Qoder"
	case r.By == "qoder":
		who = "Qoder " + who
	case r.Vendor != "":
		who = strings.TrimSpace(r.Vendor + " " + who)
	case who == "":
		who = "WorkBuddy"
	}
	switch r.Outcome {
	case provider.CheckinClaimed, provider.CheckinDone:
		s := who + " checked in today"
		if r.Credit > 0 {
			s += fmt.Sprintf(" +%g", r.Credit)
		}
		if r.Streak > 0 {
			s += fmt.Sprintf(" · a %d-day streak", r.Streak)
		}
		if r.Outcome == provider.CheckinDone || !r.Asked {
			s += " · already"
		}
		return s
	case provider.CheckinIneligible:
		return who + " isn't eligible for the check-in"
	case provider.CheckinInactive:
		return who + ": no check-in event now"
	case provider.CheckinCaptcha:
		return who + " asks for a captcha: check in in its own app"
	}
	msg := r.Msg
	if msg == "" {
		msg = "no answer"
	}
	return who + " couldn't check in: " + msg
}

// checkinCell is a WorkBuddy (China) or Trae CN account's check-in on its
// line: today's
// done, with the credits, or not yet; empty for an account without one.
func checkinCell(q provider.SubscriptionQuota, now time.Time) string {
	if !q.Checkins {
		return ""
	}
	r := q.Checkin
	if r == nil || r.Day != provider.CheckinDay(now) {
		return sMuted.Render("签到 not yet today · c")
	}
	switch r.Outcome {
	case provider.CheckinClaimed, provider.CheckinDone:
		s := sOK.Render("签到 ✓")
		if r.Credit > 0 {
			s += sText.Render(fmt.Sprintf(" +%g", r.Credit))
		}
		if r.Streak > 0 {
			s += sFaint.Render(fmt.Sprintf(" · %d-day streak", r.Streak))
		}
		return s
	case provider.CheckinIneligible:
		return sMuted.Render("签到 not eligible")
	case provider.CheckinInactive:
		return sMuted.Render("签到 no event now")
	case provider.CheckinCaptcha:
		return sMuted.Render("签到 needs a captcha · check in in its app")
	}
	return sBad.Render("签到 failed") + sMuted.Render(" · c tries again")
}

func (m model) updateUsage(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	i := slices.Index(periods, m.period)
	switch msg.String() {
	case "u":
		m.qleft = !m.qleft
		return m, nil
	case "c":
		// WorkBuddy's daily check-in, as the app's "Check in now"
		m.flash, m.flashOK = "checking in…", true
		return m, checkinCmd
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
	m.sum, m.direct = usage.Summaries(m.period)
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
	d := m.direct
	if s.Calls == 0 && d.Calls == 0 {
		b.WriteString(pad + "  " + sMuted.Render("no calls in this time · route an agent through magpie and its usage shows up here"))
		return b.String()
	}
	names := map[string]string{}
	for _, a := range agent.All() {
		names[a.ID] = a.Name
	}
	room := max(2, (m.h-16-len(ql))/2)
	if s.Calls > 0 && d.Calls > 0 {
		room = max(2, room/2)
	}
	if s.Calls == 0 {
		b.WriteString(pad + "  " + sMuted.Render("no calls through magpie in this time") + "\n\n")
	} else {
		m.usageBlock(&b, s, room, names)
	}
	// the calls the agents made on their own, as the app's Requests tab
	// counts them beside the gateway's (Kumo31 on Discord)
	if d.Calls > 0 {
		b.WriteString(pad + "  " + sFaint.Render("not through magpie · the agents' own requests, read from their session files") + "\n")
		m.usageBlock(&b, d, room, names)
	}
	return strings.TrimRight(b.String(), "\n")
}

// usageBlock is one summary's totals, timeline and agents' and models'
// tables, the gateway's or the agents' own calls.
func (m model) usageBlock(b *strings.Builder, s usage.Summary, room int, names map[string]string) {
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
	table("models", s.Models, func(g usage.Group) string {
		if g.Provider == usage.UnknownProvider {
			return g.Model // a session file's call whose provider isn't known
		}
		return g.ID
	})
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
		// a WorkBuddy (China) account's check-in, after whatever else
		ci := checkinCell(q, now)
		if ci != "" {
			ci = "   " + ci
		}
		switch {
		case q.Balance != "" && len(q.Windows) == 0:
			out = append(out, line+"  "+sText.Render(q.Balance)+sMuted.Render(" left")+ci)
			continue
		case q.Error != "":
			out = append(out, line+"  "+sMuted.Render(trunc(q.Error, max(20, width-tw-2-lipgloss.Width(ci))))+ci)
			continue
		case len(q.Windows) == 0:
			out = append(out, line+"  "+sMuted.Render("no usage reported")+ci)
			continue
		}
		// the windows follow the name, those that don't fit on lines below
		// it, then the credits a ChatGPT account holds beside them and a
		// Codex account's resets
		var cells []string
		for _, w := range provider.PooledWindows(q.Windows) {
			cells = append(cells, quotaCell(w, left, now))
		}
		if q.Balance != "" {
			c := sText.Render(q.Balance) + sMuted.Render(" left")
			if q.Provider == "codex" && q.User != "" && !provider.CodexCredits(q.User) {
				c += sFaint.Render(" · not spent") // held once a window is used up
			}
			cells = append(cells, c)
		}
		if ci != "" {
			cells = append(cells, ci[3:])
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
	if c := w.Count(left); c != "" {
		pct = c + " · " + pct
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

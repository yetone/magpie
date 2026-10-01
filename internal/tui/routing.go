package tui

// The routing page: the routing groups — their models and the order they
// go in, how requests spread over them and how long a conversation stays,
// and the rules that send a turn to one of them first, with the classifier
// that tells a message's intent. What the app's Routing view does, in the
// terminal; the rules are typed as magpie group rule add takes them.

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

func (m *model) reloadGroups() {
	m.groups = m.groups[:0]
	var removed []provider.Group
	for _, g := range provider.Groups() {
		if g.Hidden {
			removed = append(removed, g)
		} else {
			m.groups = append(m.groups, g)
		}
	}
	// the found groups removed come last, to be brought back
	m.shown = len(m.groups)
	m.groups = append(m.groups, removed...)
	m.grow = clamp(m.grow, len(m.groups))
}

var routings = []string{"", provider.Ordered, provider.Rotate, provider.LeastUsed, provider.Manual}

func routingName(v string) string {
	switch v {
	case provider.Ordered:
		return "in order"
	case provider.Rotate:
		return "rotate"
	case provider.LeastUsed:
		return "least used"
	case provider.Manual:
		return "manual"
	}
	return "smart"
}

// groupRouting is how a group routes: a manual one names its pick.
func groupRouting(g provider.Group) string {
	if g.Routing == provider.Manual {
		return "manual → " + g.Picked()
	}
	return routingName(g.Routing)
}

func staysName(v string) string {
	switch v {
	case provider.AffinitySession:
		return "stays the session"
	case provider.AffinityTurn:
		return "stays a turn"
	case provider.AffinityOff:
		return "never stays"
	}
	return "stays auto"
}

// saveGroup changes one group and saves it: one magpie found becomes the
// user's.
func saveGroup(id string, change func(*provider.Group) error, done string) tea.Cmd {
	return func() tea.Msg {
		g, ok := findGroup(id)
		if !ok {
			return flashMsg{text: "no group " + id}
		}
		if err := change(&g); err != nil {
			return flashMsg{text: err.Error()}
		}
		if err := provider.SaveGroup(g); err != nil {
			return flashMsg{text: err.Error()}
		}
		return flashMsg{text: done, ok: true}
	}
}

// findGroup is a group by its id, without group/ in front.
func findGroup(id string) (provider.Group, bool) {
	for _, g := range provider.Groups() {
		if g.ID == id && !g.Hidden {
			return g, true
		}
	}
	return provider.Group{}, false
}

func nextRouting(g *provider.Group) error {
	i := max(0, slices.Index(routings, g.Routing))
	g.Routing = routings[(i+1)%len(routings)]
	return nil
}

func nextStays(g *provider.Group) error {
	i := max(0, slices.Index(provider.Affinities, g.Affinity))
	g.Affinity = provider.Affinities[(i+1)%len(provider.Affinities)]
	return nil
}

func (m model) updateGroups(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.groups)
	key := msg.String()
	if key != "d" {
		m.confirm = ""
	}
	switch key {
	case "j", "down":
		if n > 0 {
			m.grow = (m.grow + 1) % n
		}
		return m, nil
	case "k", "up":
		if n > 0 {
			m.grow = (m.grow + n - 1) % n
		}
		return m, nil
	case "n":
		m.openAsk(ask{crumbs: []string{"routing", "new"}, input: newInput("the group's name, e.g. opus-anywhere"),
			hint: "then pick its first model; agents choose it as group/<id>",
			onEnter: func(name string) tea.Cmd {
				return func() tea.Msg { return pickMsg{newGroup: name} }
			}})
		return m, nil
	}
	if n == 0 {
		return m, nil
	}
	g := m.groups[m.grow]
	if g.Hidden {
		if key == "u" {
			return m, func() tea.Msg {
				if err := provider.ShowGroup(g.ID); err != nil {
					return flashMsg{text: err.Error()}
				}
				return flashMsg{text: "brought " + g.ID + " back", ok: true}
			}
		}
		if key != "r" {
			m.flash, m.flashOK = g.ID+" was removed · u brings it back", false
		}
		return m, nil
	}
	switch key {
	case "enter", " ":
		m.gid, m.gsel = g.ID, 0
		m.mode = modeGroup
	case "o":
		return m, saveGroup(g.ID, nextRouting, g.Name+" routing changed")
	case "d":
		if m.confirm != "group/"+g.ID {
			m.confirm = "group/" + g.ID
			m.flash, m.flashOK = "press d again to remove "+g.Name, false
			return m, nil
		}
		m.confirm = ""
		return m, func() tea.Msg {
			if err := provider.DeleteGroup(g.ID); err != nil {
				return flashMsg{text: err.Error()}
			}
			return flashMsg{text: "removed " + g.Name, ok: true}
		}
	}
	return m, nil
}

// pickMsg opens the model picker for a group: to add to one, or to make
// one with its first model.
type pickMsg struct{ group, newGroup string }

// memberOptions is every model and group magpie has, but those in skip.
func memberOptions(skip []string, self string) []agent.Option {
	var out []agent.Option
	for _, e := range provider.Catalog() {
		if slices.Contains(skip, e.ID) || e.ID == provider.GroupPrefix+self {
			continue
		}
		note := e.Name
		if e.Group != "" {
			note = "group · " + e.Name
		}
		out = append(out, agent.Option{Value: e.ID, Note: note})
	}
	return out
}

func (m *model) openMemberPicker(p pickMsg) {
	var skip []string
	crumbs := []string{"routing", p.newGroup, "first model"}
	empty := "magpie has no models yet · add a provider first"
	if p.group != "" {
		g, _ := findGroup(p.group)
		skip, crumbs = g.Members, []string{"routing", g.Name, "add a model"}
		empty = "every model magpie has is in it"
	}
	m.pk = picker{
		crumbs: crumbs,
		input:  newInput("filter models and groups"),
		items:  memberOptions(skip, p.group),
		empty:  empty,
		onPick: func(id string) tea.Cmd {
			if p.group != "" {
				return saveGroup(p.group, func(g *provider.Group) error {
					g.Members = append(g.Members, id)
					return nil
				}, id+" added")
			}
			return func() tea.Msg {
				g := provider.Group{Name: p.newGroup, Members: []string{id}}
				if err := provider.SaveGroup(g); err != nil {
					return flashMsg{text: err.Error()}
				}
				return flashMsg{text: "made group " + p.newGroup + " · ↵ on it adds more models", ok: true}
			}
		},
	}
	m.pk.refilter()
	m.mode = modePick
	if p.group != "" {
		m.back = modeGroup
	}
}

// openClassifier picks the model that tells which intent a message is:
// a model, or another group whose models are tried in turn.
func (m *model) openClassifier(g provider.Group) {
	var items []agent.Option
	for _, e := range provider.Catalog() {
		if e.Group == g.ID {
			continue // it can't classify for itself
		}
		note := e.Name
		if e.ID == g.Classifier {
			note = "● now · " + note
		}
		items = append(items, agent.Option{Value: e.ID, Note: note})
	}
	m.pk = picker{
		crumbs: []string{"routing", g.Name, "classifier"},
		input:  newInput("filter models · best a small fast one without reasoning"),
		items:  items,
		empty:  "magpie has no models yet · add a provider first",
		onPick: func(id string) tea.Cmd {
			return saveGroup(g.ID, func(g *provider.Group) error { g.Classifier = id; return nil }, g.Name+" classifies with "+id)
		},
	}
	m.pk.refilter()
	m.mode = modePick
	m.back = modeGroup
}

// ruleHint is what a rule is typed as.
const ruleHint = `use=<model>, and any of: tokens=200k · images · effort=on|low|medium|high|xhigh|max · agents=codex,claude
intent="a quick question" (the group's classifier=<model> tells it) · compact
time=09:00-18:00 (local; 22:00-08:00 runs past midnight) · days=mon-fri · at=<n> for its place`

// openRule asks for a rule: a new one, or rule i (from 0) typed again.
func (m *model) openRule(g provider.Group, i int) {
	in := newInput("use=<model> tokens=200k …")
	in.CharLimit = 400
	crumbs := []string{"routing", g.Name, "new rule"}
	if i >= 0 {
		in.SetValue(g.Rules[i].Line())
		in.CursorEnd()
		crumbs[2] = fmt.Sprintf("rule %d", i+1)
	}
	m.openAsk(ask{crumbs: crumbs, input: in, hint: ruleHint + "\nits models: " + strings.Join(g.Members, ", "),
		onEnter: func(line string) tea.Cmd {
			done := "rule added"
			if i >= 0 {
				done = fmt.Sprintf("rule %d changed", i+1)
			}
			return saveGroup(g.ID, func(g *provider.Group) error { return putRule(g, i, line) }, done)
		}})
	m.back = modeGroup
}

// putRule reads a typed rule into the group: in place of rule i, or
// added when i is -1.
func putRule(g *provider.Group, i int, line string) error {
	r, at, classifier, err := provider.ParseRule(*g, provider.RuleWords(line))
	if err != nil {
		return err
	}
	if classifier != "" {
		g.Classifier = classifier
	}
	if i >= 0 && i < len(g.Rules) {
		g.Rules = slices.Delete(g.Rules, i, i+1)
		if at == 0 {
			at = i + 1
		}
	}
	if at == 0 || at > len(g.Rules) {
		g.Rules = append(g.Rules, r)
	} else {
		g.Rules = slices.Insert(g.Rules, at-1, r)
	}
	return nil
}

func (m model) group() (provider.Group, bool) {
	for _, g := range m.groups {
		if g.ID == m.gid && !g.Hidden {
			return g, true
		}
	}
	return provider.Group{}, false
}

// updateGroup is an open group: its models, then its rules, one list.
func (m model) updateGroup(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	g, ok := m.group()
	if !ok {
		m.mode = modeList
		return m, nil
	}
	n, nr := len(g.Members), len(g.Rules)
	all := n + nr
	onRule := m.gsel >= n
	key := msg.String()
	if key != "d" {
		m.confirm = ""
	}
	move := func(by int) (tea.Model, tea.Cmd) {
		to := m.gsel + by
		if to < 0 || to >= all || (to >= n) != onRule {
			return m, nil
		}
		from := m.gsel
		m.gsel = to
		if onRule {
			return m, saveGroup(g.ID, func(g *provider.Group) error {
				g.Rules[from-n], g.Rules[to-n] = g.Rules[to-n], g.Rules[from-n]
				return nil
			}, fmt.Sprintf("rule %d moved to %d", from-n+1, to-n+1))
		}
		return m, saveGroup(g.ID, func(g *provider.Group) error {
			g.Members[from], g.Members[to] = g.Members[to], g.Members[from]
			return nil
		}, g.Members[from]+" moved")
	}
	switch key {
	case "esc", "q":
		m.mode = modeList
	case "j", "down":
		if all > 0 {
			m.gsel = (m.gsel + 1) % all
		}
	case "k", "up":
		if all > 0 {
			m.gsel = (m.gsel + all - 1) % all
		}
	case "J", "shift+down":
		return move(1)
	case "K", "shift+up":
		return move(-1)
	case "a":
		m.openMemberPicker(pickMsg{group: g.ID})
	case "n":
		m.openRule(g, -1)
	case "enter", "e":
		if onRule {
			m.openRule(g, m.gsel-n)
		}
	case "c":
		if !slices.ContainsFunc(g.Rules, func(r provider.Rule) bool { return r.Intent != "" }) {
			m.flash, m.flashOK = g.Name+" has no rule with an intent to classify for · n adds one", false
			return m, nil
		}
		m.openClassifier(g)
	case "E", "F":
		// the model's own effort and fast mode, whatever the agent asks
		if onRule || n == 0 || strings.HasPrefix(g.Members[m.gsel], provider.GroupPrefix) {
			return m, nil
		}
		id := g.Members[m.gsel]
		if key == "F" {
			fast := !g.IsFast(id)
			said := " not fast"
			if fast {
				said = " fast"
			}
			return m, saveGroup(g.ID, func(g *provider.Group) error {
				g.SetMemberFast(id, fast)
				return nil
			}, id+said)
		}
		model, effort := provider.MemberEffort(id)
		to := provider.WithMemberEffort(model, nextMemberEffort(effort))
		return m, saveGroup(g.ID, func(g *provider.Group) error {
			g.RenameMember(id, to)
			return nil
		}, to+" set")
	case "o":
		return m, saveGroup(g.ID, nextRouting, g.Name+" routing changed")
	case "s":
		return m, saveGroup(g.ID, nextStays, g.Name+" affinity changed")
	case "f":
		in := newInput("a tag, e.g. relay")
		in.SetValue(g.Family)
		m.openAsk(ask{crumbs: []string{"routing", g.Name, "family"}, input: in, empty: true,
			hint: "agents can be shown only some families: magpie visible <agent> <family>",
			onEnter: func(v string) tea.Cmd {
				return saveGroup(g.ID, func(g *provider.Group) error { g.Family = v; return nil }, g.Name+" family "+dash(v))
			}})
		m.back = modeGroup
	case "x":
		in := newInput("tokens, e.g. 200k · empty for its models' own")
		if g.Context > 0 {
			in.SetValue(fmt.Sprint(g.Context))
		}
		m.openAsk(ask{crumbs: []string{"routing", g.Name, "context"}, input: in, empty: true,
			hint: "how long a request agents are told the group takes, rather than its shortest model's",
			onEnter: func(v string) tea.Cmd {
				return saveGroup(g.ID, func(g *provider.Group) error {
					if v == "" {
						g.Context = 0
						return nil
					}
					n, err := provider.ParseTokens(v)
					g.Context = n
					return err
				}, g.Name+" context "+dash(v))
			}})
		m.back = modeGroup
	case "l":
		in := newInput("e.g. low,medium,high,xhigh · empty for those its models share")
		in.SetValue(strings.Join(g.Levels, ","))
		m.openAsk(ask{crumbs: []string{"routing", g.Name, "levels"}, input: in, empty: true,
			hint: "the reasoning levels agents are offered: " + strings.Join(provider.Levels, ", ") + "; a model without the one asked is sent its nearest",
			onEnter: func(v string) tea.Cmd {
				return saveGroup(g.ID, func(g *provider.Group) error {
					var err error
					g.Levels, err = provider.CleanLevels(strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' }))
					return err
				}, g.Name+" levels "+dash(v))
			}})
		m.back = modeGroup
	case "R":
		in := newInput("the group's new id")
		in.SetValue(g.ID)
		in.CursorEnd()
		m.openAsk(ask{crumbs: []string{"routing", g.Name, "rename"}, input: in,
			hint: "agents pick it as group/<id>: those on it, and the groups that have it in them, follow",
			onEnter: func(to string) tea.Cmd {
				return func() tea.Msg {
					if err := provider.RenameGroup(g.ID, to); err != nil {
						return flashMsg{text: err.Error()}
					}
					return renamedMsg{from: g.ID, to: strings.ToLower(to)}
				}
			}})
		m.back = modeGroup
	case "d":
		if onRule {
			i := m.gsel - n
			if m.confirm != fmt.Sprintf("rule/%d", i) {
				m.confirm = fmt.Sprintf("rule/%d", i)
				m.flash, m.flashOK = fmt.Sprintf("press d again to remove rule %d", i+1), false
				return m, nil
			}
			m.confirm = ""
			m.gsel = clamp(m.gsel, all-1)
			return m, saveGroup(g.ID, func(g *provider.Group) error {
				g.Rules = slices.Delete(g.Rules, i, i+1)
				return nil
			}, fmt.Sprintf("rule %d removed", i+1))
		}
		if n == 0 {
			return m, nil
		}
		id := g.Members[m.gsel]
		if n == 1 {
			m.flash, m.flashOK = "a group needs a model in it · d on the routing list removes the group", false
			return m, nil
		}
		if m.confirm != "member/"+id {
			m.confirm = "member/" + id
			m.flash, m.flashOK = "press d again to take "+id+" out", false
			if slices.ContainsFunc(g.Rules, func(r provider.Rule) bool { return r.Use == id }) {
				m.flash += " and its rules"
			}
			return m, nil
		}
		m.confirm = ""
		m.gsel = clamp(m.gsel, all-1)
		return m, saveGroup(g.ID, func(g *provider.Group) error {
			g.Members = slices.DeleteFunc(g.Members, func(x string) bool { return x == id })
			g.Rules = slices.DeleteFunc(g.Rules, func(r provider.Rule) bool { return r.Use == id })
			return nil
		}, id+" taken out")
	}
	return m, nil
}

// nextMemberEffort is the effort E sets a member at after effort: the
// agent's (""), then low to max, then the agent's again.
func nextMemberEffort(effort string) string {
	cycle := []string{"", "low", "medium", "high", "xhigh", "max"}
	return cycle[(slices.Index(cycle, effort)+1)%len(cycle)]
}

// renamedMsg is a group given another id: the page follows it.
type renamedMsg struct{ from, to string }

func (m model) viewGroups() string {
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n\n")
	if len(m.groups) == 0 {
		b.WriteString(pad + "  " + sMuted.Render("no routing groups yet · n makes one"))
		return b.String()
	}
	nameW, idW := 0, 0
	for _, g := range m.groups {
		nameW, idW = max(nameW, lipgloss.Width(dash(g.Name))), max(idW, lipgloss.Width(provider.GroupPrefix+g.ID))
	}
	visible := max(3, m.h-7)
	start := 0
	if m.grow >= visible {
		start = m.grow - visible + 1
	}
	for i := start; i < min(len(m.groups), start+visible); i++ {
		g := m.groups[i]
		if i == m.shown {
			b.WriteString("\n" + pad + "  " + sFaint.Render("removed · found by magpie, taken away: u brings one back") + "\n")
		}
		marker, name := "  ", sName.Render(padRight(dash(g.Name), nameW))
		if g.Hidden {
			name = sFaint.Render(padRight(dash(g.Name), nameW))
		}
		if i == m.grow {
			marker, name = sCursor.Render("▸ "), sNameOn.Render(padRight(dash(g.Name), nameW))
		}
		line := pad + marker + name + "  " + sFaint.Render(padRight(provider.GroupPrefix+g.ID, idW))
		if g.Hidden {
			b.WriteString(line + "\n")
			continue
		}
		notes := []string{fmt.Sprintf("%d model%s", len(g.Members), plural(len(g.Members))), groupRouting(g)}
		if g.Affinity != "" {
			notes = append(notes, staysName(g.Affinity))
		}
		if len(g.Rules) > 0 {
			notes = append(notes, fmt.Sprintf("%d rule%s", len(g.Rules), plural(len(g.Rules))))
		}
		if g.Family != "" {
			notes = append(notes, "family "+g.Family)
		}
		if g.Auto {
			notes = append(notes, "found")
		}
		members := strings.Join(g.Members, " → ")
		line += "  " + sText.Render(strings.Join(notes, " · "))
		if room := m.w - lipgloss.Width(line) - 4; room > 10 {
			line += "  " + sMuted.Render(trunc(members, room))
		}
		b.WriteString(line + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m model) viewGroup() string {
	g, _ := m.group()
	var b strings.Builder
	b.WriteString(m.header("routing", g.Name))
	b.WriteString("\n\n")
	head := []string{provider.GroupPrefix + g.ID, groupRouting(g), staysName(g.Affinity)}
	if g.Context > 0 {
		head = append(head, "context "+fmtTokens(g.Context))
	}
	if len(g.Levels) > 0 {
		head = append(head, "levels "+strings.Join(g.Levels, "/"))
	}
	if g.Family != "" {
		head = append(head, "family "+g.Family)
	}
	if g.Auto {
		head = append(head, "found by magpie: a change makes it yours")
	}
	b.WriteString(pad + "  " + sMuted.Render(strings.Join(head, " · ")) + "\n\n")
	b.WriteString(pad + "  " + sFaint.Render("models · the first that can take a request gets it") + "\n")
	for i, id := range g.Members {
		marker, name := "  ", sText.Render(id)
		if i == m.gsel {
			marker, name = sCursor.Render("▸ "), sNameOn.Render(id)
		}
		if g.IsFast(id) {
			name += sMuted.Render(" · fast")
		}
		b.WriteString(pad + marker + sFaint.Render(fmt.Sprintf("%d  ", i+1)) + name + "\n")
	}
	b.WriteString("\n" + pad + "  " + sFaint.Render("rules · as a turn begins, the first that matches sends it to its model first") + "\n")
	if len(g.Rules) == 0 {
		b.WriteString(pad + "  " + sMuted.Render("none · n adds one") + "\n")
	}
	for i, r := range g.Rules {
		marker, use := "  ", sText.Render(r.Use)
		if len(g.Members)+i == m.gsel {
			marker, use = sCursor.Render("▸ "), sNameOn.Render(r.Use)
		}
		b.WriteString(pad + marker + sFaint.Render(fmt.Sprintf("%d  ", i+1)) + sMuted.Render(strings.Join(r.Conditions(), " · ")+" → ") + use + "\n")
	}
	if slices.ContainsFunc(g.Rules, func(r provider.Rule) bool { return r.Intent != "" }) {
		b.WriteString("\n" + pad + "  " + sFaint.Render("classifier ") + sText.Render(dash(g.Classifier)) + sFaint.Render(" · tells which intent a message is") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

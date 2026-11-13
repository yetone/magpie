package tui

// The library page: the instructions, MCP servers and skills magpie gives
// the agents, which agents get each, what the agents have that the library
// doesn't, and RTK's hook in each — what the app's Library view does, in
// the terminal.

import (
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/proc"
)

// libRow is one line of the page.
type libRow struct {
	kind  string // instructions, mcp, skill, found-mcp, found-skill, rtk
	name  string
	about string   // what it runs, or what it is
	on    []string // the agents that get it (or have it, when found)
	own   bool     // a server the agent's app puts in itself: nothing to bring in
	bad   int      // agents it couldn't be given to
}

// libKinds are what the library can give, by row kind.
var libKinds = map[string]string{"instructions": "instructions", "mcp": "mcp", "skill": "skills"}

func (m *model) reloadLibrary() {
	m.lib, m.libAgents, m.libErr = nil, nil, ""
	v, err := library.Read(nil)
	if err != nil {
		m.libErr = err.Error()
		return
	}
	m.libView = v
	m.libAgents = v.Agents
	ins := libRow{kind: "instructions", name: "shared instructions"}
	if lines := strings.TrimRight(v.Instructions.Shared, "\n"); strings.TrimSpace(lines) == "" {
		ins.about = "none yet · e writes them"
	} else {
		ins.about = fmt.Sprintf("%d line%s", strings.Count(lines, "\n")+1, plural(strings.Count(lines, "\n")+1))
	}
	for _, a := range v.Instructions.Agents {
		if a.On {
			ins.on = append(ins.on, a.Agent)
		}
		if a.Edited {
			ins.bad++
		}
	}
	m.lib = append(m.lib, ins)
	for _, s := range v.Servers {
		what := s.URL
		if s.Command != "" {
			what = strings.TrimSpace(s.Command + " " + strings.Join(s.Args, " "))
		}
		if s.Local {
			what = "not synced · " + what
		}
		m.lib = append(m.lib, libRow{kind: "mcp", name: s.Name, about: what, on: s.Agents, bad: len(s.Problems)})
	}
	for _, s := range v.Skills {
		r := libRow{kind: "skill", name: s.Name, about: s.Description, on: s.Agents, bad: len(s.Problems)}
		if s.Missing {
			r.about, r.bad = "its folder is gone", r.bad+1
		} else if s.Gone != "" {
			r.about, r.bad = "removed from "+s.Source+" · d takes it out", r.bad+1
		}
		if s.OffHere {
			// ticked agents are other computers' too; none gets it here
			r.about = "off on this computer · magpie library skill on-here " + s.Name
		}
		m.lib = append(m.lib, r)
	}
	for _, f := range v.FoundServers {
		m.lib = append(m.lib, libRow{kind: "found-mcp", name: f.Server.Name, on: f.Server.Agents, own: f.Own})
	}
	for _, f := range v.FoundSkills {
		m.lib = append(m.lib, libRow{kind: "found-skill", name: f.Name, about: f.Description, on: f.Agents})
	}
	if rv := library.ReadRTK(); rv.Path != "" || len(rv.Agents) > 0 {
		r := libRow{kind: "rtk", name: "RTK"}
		switch {
		case rv.Path == "":
			r.about = "not installed · " + rv.URL
		case rv.Gain != nil:
			r.about = fmt.Sprintf("%s · %s tokens saved", rv.Version, fmtTokens(int(rv.Gain.Saved)))
		default:
			r.about = rv.Version
		}
		for _, a := range rv.Agents {
			if a.On {
				r.on = append(r.on, a.ID)
			}
		}
		m.lib = append(m.lib, r)
	}
	m.lrow = clamp(m.lrow, len(m.lib))
}

// libName is an agent's name, by its id.
func (m model) libName(id string) string {
	for _, a := range m.libAgents {
		if a.ID == id {
			return a.Name
		}
	}
	return id
}

func (m model) updateLibrary(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.lib)
	key := msg.String()
	if key != "d" {
		m.confirm = ""
	}
	switch key {
	case "j", "down":
		if n > 0 {
			m.lrow = (m.lrow + 1) % n
		}
		return m, nil
	case "k", "up":
		if n > 0 {
			m.lrow = (m.lrow + n - 1) % n
		}
		return m, nil
	case "a":
		m.openLibAdd()
		return m, nil
	case "s":
		m.flash, m.flashOK = "writing the library into the agents…", true
		return m, libCmd(library.Sync, "synced")
	}
	if n == 0 {
		return m, nil
	}
	r := m.lib[m.lrow]
	switch key {
	case "enter", " ":
		switch r.kind {
		case "instructions", "mcp", "skill", "rtk":
			m.openLibAgents(r)
		case "found-mcp", "found-skill":
			m.flash, m.flashOK = "i brings "+r.name+" into the library, for magpie to give it to other agents too", true
			if !r.own {
				m.flash += "; d takes it out of every agent that has it"
			}
		}
	case "e":
		switch r.kind {
		case "instructions":
			return m, editInstructions()
		case "mcp":
			m.openServer(r.name)
		}
	case "u":
		if r.kind != "skill" {
			return m, nil
		}
		m.flash, m.flashOK = "fetching "+r.name+" from GitHub again…", true
		return m, libCmd(func() (*library.Result, error) {
			res, err := library.UpdateSkill(r.name, false)
			var gone *library.GoneError
			if errors.As(err, &gone) {
				// removed from its source: kept till the user takes it out
				err = fmt.Errorf("%w · d takes it out", err)
			}
			return res, err
		}, "updated "+r.name)
	case "i":
		if r.own {
			m.flash, m.flashOK = r.name+" is put there by the agent's app itself, each time it starts: it stays as it is", false
			return m, nil
		}
		var do func(string) (*library.Result, error)
		switch r.kind {
		case "found-mcp":
			do = library.ImportServer
		case "found-skill":
			do = library.ImportSkill
		default:
			return m, nil
		}
		return m, libCmd(func() (*library.Result, error) { return do(r.name) }, "brought "+r.name+" into the library")
	case "d":
		var do func(string) (*library.Result, error)
		switch r.kind {
		case "mcp":
			do = library.RemoveServer
		case "skill":
			do = library.RemoveSkill
		case "found-skill":
			do = library.RemoveFoundSkill
		case "found-mcp":
			if r.own {
				m.flash, m.flashOK = r.name+" is put there by the agent's app itself, each time it starts: it stays as it is", false
				return m, nil
			}
			do = library.RemoveFoundServer
		default:
			return m, nil
		}
		if m.confirm != "library/"+r.kind+"/"+r.name {
			m.confirm = "library/" + r.kind + "/" + r.name
			m.flash, m.flashOK = "press d again to take "+r.name+" out of the library and every agent", false
			switch r.kind {
			case "found-skill":
				m.flash = "press d again to take " + r.name + " out of every agent that has it, kept with the backups"
			case "found-mcp":
				m.flash = "press d again to take " + r.name + " out of every agent that has it, their files kept with the backups"
			}
			return m, nil
		}
		m.confirm = ""
		return m, libCmd(func() (*library.Result, error) { return do(r.name) }, "removed "+r.name)
	}
	return m, nil
}

// libCmd runs a library change, saying what it did to the agents.
func libCmd(do func() (*library.Result, error), done string) tea.Cmd {
	return func() tea.Msg {
		res, err := do()
		if err != nil {
			return flashMsg{text: err.Error()}
		}
		text, ok := libSaid(res, done)
		return flashMsg{text: text, ok: ok}
	}
}

// libSaid is a change's result in a line.
func libSaid(res *library.Result, done string) (string, bool) {
	// an install of skills the library had already added none
	if len(res.Installed) == 0 && len(res.Had) > 0 {
		done = strings.Join(res.Had, ", ") + " in the library already"
	}
	// a skill from a folder, linked to the one installed (skill_link.go)
	if len(res.Installed) == 0 && len(res.Linked) > 0 {
		done = strings.Join(res.Linked, ", ") + " updates from GitHub now"
	}
	if len(res.LinkedKept) > 0 {
		done += " · files of their own kept with the backups: " + strings.Join(res.LinkedKept, ", ")
	}
	for _, p := range res.Skipped {
		done += " · " + strings.TrimPrefix(p.What, "skill:") + " skipped: " + p.Error
	}
	if len(res.Changed) > 0 {
		done += " · written into " + strings.Join(res.Changed, ", ")
	}
	for _, p := range res.Problems {
		done += " · " + p.Agent + ": " + p.Error
	}
	return done, len(res.Problems) == 0 && len(res.Skipped) == 0
}

// libOptions are the agents that can get a row's kind, those that get it
// marked.
func (m model) libOptions(r libRow) []agent.Option {
	var out []agent.Option
	add := func(id, name string) {
		note := "○ off"
		if slices.Contains(r.on, id) {
			note = "● on"
		}
		out = append(out, agent.Option{Value: id, Note: note + "  " + name})
	}
	if r.kind == "rtk" {
		for _, a := range library.ReadRTK().Agents {
			add(a.ID, a.Name)
		}
		return out
	}
	for _, a := range m.libAgents {
		has := map[string]bool{"instructions": a.Instructions != "", "mcp": a.MCP != "", "skill": a.Skills != ""}[r.kind]
		if has {
			add(a.ID, a.Name)
		}
	}
	return out
}

// openLibAgents lists the agents a row can go to; enter gives it to one or
// takes it away, and the list stays open.
func (m *model) openLibAgents(r libRow) {
	crumbs := []string{"library", r.name, "agents"}
	m.pk = picker{
		crumbs: crumbs,
		input:  newInput("filter agents"),
		items:  m.libOptions(r),
		empty:  "no agent here has a place for it",
		toggle: func(id string) ([]agent.Option, string, bool) {
			on := !slices.Contains(r.on, id)
			var (
				text string
				ok   = true
			)
			if r.kind == "rtk" {
				v, err := library.SetRTK(id, on)
				if err != nil {
					return nil, err.Error(), false
				}
				r.on = nil
				for _, a := range v.Agents {
					if a.On {
						r.on = append(r.on, a.ID)
					}
				}
				text = "RTK " + map[bool]string{true: "on", false: "off"}[on] + " for " + m.libName(id)
				if len(v.Restart) > 0 {
					text += " · restart " + strings.Join(v.Restart, ", ") + " for it to take effect"
				}
			} else {
				agents := slices.Clone(r.on)
				if on {
					agents = append(agents, id)
				} else {
					agents = slices.DeleteFunc(agents, func(a string) bool { return a == id })
				}
				var (
					res *library.Result
					err error
				)
				switch r.kind {
				case "instructions":
					res, err = library.SaveInstructions(library.InstructionsChange{Agents: agents})
				case "mcp":
					res, err = library.ServerAgents(r.name, agents)
				case "skill":
					res, err = library.SkillAgents(r.name, agents)
				}
				if err != nil {
					return nil, err.Error(), false
				}
				r.on = agents
				verb := "given to "
				if !on {
					verb = "taken from "
				}
				text, ok = libSaid(&library.Result{Problems: res.Problems}, r.name+" "+verb+m.libName(id))
			}
			return m.libOptions(r), text, ok
		},
	}
	m.pk.refilter()
	m.mode = modePick
	m.back = modeList
}

// openLibAdd picks what to add: an MCP server, or skills.
func (m *model) openLibAdd() {
	m.pk = picker{
		crumbs: []string{"library", "add"},
		input:  newInput("filter"),
		items: []agent.Option{
			{Value: "mcp server", Note: "a URL, or a command it runs"},
			{Value: "skills", Note: "from GitHub, GitLab or any git repository, or a folder on this machine"},
		},
		onPick: func(v string) tea.Cmd {
			return func() tea.Msg {
				if v == "skills" {
					in := newInput("owner/repo, a GitHub or GitLab link, a git address (git@host:group/repo.git), or a folder's full path")
					in.CharLimit = 400
					return askMsg{ask{crumbs: []string{"library", "add", "skills"}, input: in,
						hint:    "magpie looks there for skills, then you pick one",
						onEnter: probeCmd}}
				}
				in := newInput("<name> <url | command args…>")
				in.CharLimit = 400
				return askMsg{ask{crumbs: []string{"library", "add", "mcp server"}, input: in,
					hint:    "e.g. context7 https://mcp.context7.com/mcp · fs npx -y @modelcontextprotocol/server-filesystem ~/code\nthen ↵ on it gives it to agents",
					onEnter: func(line string) tea.Cmd { return saveServer(nil, line) }}}
			}
		},
	}
	m.pk.refilter()
	m.mode = modePick
	m.back = modeList
}

// openServer types a library server again: its name, then its URL or
// command; what else it has (env, headers, agents) stays.
func (m *model) openServer(name string) {
	var old *library.Server
	for _, s := range m.libView.Servers {
		if s.Name == name {
			c := *s.Server
			old = &c
		}
	}
	if old == nil {
		return
	}
	line := old.Name + " " + old.URL
	if old.Command != "" {
		line = strings.Join(append([]string{old.Name, old.Command}, old.Args...), " ")
	}
	in := newInput("<name> <url | command args…>")
	in.CharLimit = 400
	in.SetValue(line)
	in.CursorEnd()
	m.openAsk(ask{crumbs: []string{"library", name, "edit"}, input: in,
		hint:    "its env, headers and agents stay as they are",
		onEnter: func(line string) tea.Cmd { return saveServer(old, line) }})
}

// saveServer makes a server of a typed line, in place of old when it is
// one being changed.
func saveServer(old *library.Server, line string) tea.Cmd {
	return func() tea.Msg {
		words := strings.Fields(line)
		if len(words) < 2 {
			return flashMsg{text: "a name, then a URL or a command"}
		}
		s, err := library.ServerOf(words[0], words[1:])
		if err != nil {
			return flashMsg{text: err.Error()}
		}
		was, done := "", "added "+s.Name+" · ↵ on it gives it to agents"
		if old != nil {
			was, done = old.Name, "saved "+s.Name
			// and its OAuth app and startup timeout, which only the GUI edits
			// and whether it is kept to this computer, and out of the hub,
			// and the user's note on it
			s.Agents, s.Env, s.Headers, s.OAuth, s.StartupTimeout, s.Local, s.Hub, s.Note = old.Agents, old.Env, old.Headers, old.OAuth, old.StartupTimeout, old.Local, old.Hub, old.Note
			// and the synced command, when this computer runs its own
			// (the line typed is this computer's then; check drops it
			// for a URL)
			s.Shared = old.Shared
		}
		return libCmd(func() (*library.Result, error) { return library.SaveServer(was, s) }, done)()
	}
}

// probedMsg is what was found at a source typed for skills.
type probedMsg struct{ p *library.Probe }

func probeCmd(source string) tea.Cmd {
	return func() tea.Msg {
		p, err := library.ProbeSkills(source)
		if err != nil {
			return flashMsg{text: err.Error()}
		}
		return probedMsg{p}
	}
}

// openProbe lists the skills found at a source; enter adds one to the
// library.
func (m *model) openProbe(p *library.Probe) {
	var items []agent.Option
	for _, c := range p.Candidates {
		note := c.Description
		switch {
		case c.Have && c.As != "":
			note = "in the library as " + c.As + " · " + note
		case c.Have:
			note = "in the library already · " + note
		case c.Links:
			// the library's by its name came from a folder: this one
			// takes its place, and it updates from GitHub from then on
			note = "links the library's to GitHub · " + note
		case c.As != "":
			// another skill from elsewhere has its name
			note = "adds as " + c.As + " · " + note
		}
		v := c.Path
		if v == "" {
			v = "."
		}
		items = append(items, agent.Option{Value: v, Note: c.Name + "  " + trunc(note, 80)})
	}
	m.pk = picker{
		crumbs: []string{"library", "add", "skills", p.Source},
		input:  newInput("filter skills"),
		items:  items,
		empty:  "no skills there: a skill is a folder with a SKILL.md",
		onPick: func(path string) tea.Cmd {
			if path == "." {
				path = ""
			}
			return libCmd(func() (*library.Result, error) {
				return library.InstallSkills(p.Source, []string{path}, []string{})
			}, "added the skill · ↵ on it gives it to agents")
		},
	}
	m.pk.refilter()
	m.mode = modePick
	m.back = modeList
	// skills too big to install on their own, not in the list (#2062)
	var left []string
	for _, l := range p.Left {
		name := p.Source
		if l.Path != "" {
			name = path.Base(l.Path)
		}
		left = append(left, fmt.Sprintf("%s (%d MB)", name, l.Size>>20))
	}
	if len(left) > 0 {
		m.flash = "too big to install, over 200 MB a skill: " + strings.Join(left, ", ")
	}
}

// editedMsg is the shared instructions as the editor left them.
type editedMsg struct {
	text string
	err  error
}

// editInstructions opens the shared instructions in $VISUAL or $EDITOR
// and saves what it leaves, which writes it into the agents that get it.
func editInstructions() tea.Cmd {
	v, err := library.ReadInstructions()
	if err != nil {
		return func() tea.Msg { return flashMsg{text: err.Error()} }
	}
	f, err := os.CreateTemp("", "magpie-instructions-*.md")
	if err != nil {
		return func() tea.Msg { return flashMsg{text: err.Error()} }
	}
	f.WriteString(v.Shared)
	f.Close()
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		ed = "vi"
		if runtime.GOOS == "windows" {
			ed = "notepad"
		}
	}
	// the editor may come with flags: code -w
	words := strings.Fields(ed)
	c := proc.Command(words[0], append(words[1:], f.Name())...)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		defer os.Remove(f.Name())
		if err != nil {
			return editedMsg{err: err}
		}
		b, err := os.ReadFile(f.Name())
		if err != nil {
			return editedMsg{err: err}
		}
		if string(b) == v.Shared {
			return flashMsg{text: "the instructions are as they were", ok: true}
		}
		return editedMsg{text: string(b)}
	})
}

func saveInstructions(text string) tea.Cmd {
	return libCmd(func() (*library.Result, error) {
		return library.SaveInstructions(library.InstructionsChange{Shared: &text})
	}, "instructions saved")
}

func (m model) viewLibrary() string {
	var b strings.Builder
	b.WriteString(m.header())
	b.WriteString("\n\n")
	if m.libErr != "" {
		b.WriteString(pad + "  " + sBad.Render(m.libErr))
		return b.String()
	}
	nameW := 0
	for _, r := range m.lib {
		nameW = max(nameW, len([]rune(r.name)))
	}
	nameW = min(nameW, 32)
	heads := map[string]string{
		"instructions": "instructions · what every agent reads before a conversation",
		"mcp":          "mcp servers · a adds one, e changes it",
		"skill":        "skills · a adds some from GitHub or a folder, u fetches one from GitHub again",
		"found-mcp":    "mcp servers in your agents, not in the library",
		"found-skill":  "skills in your agents, not in the library",
		"rtk":          "rtk · its hook in each agent, by RTK's own installer",
	}
	// the rows around the cursor, as many as fit
	visible := max(4, m.h-6)
	// where the window starts is counted in the lines it draws — counted in
	// rows alone, a list of several sections drew heads the budget never
	// paid for, and the row picked fell off the bottom of the page
	spend := func(from int) int {
		lines := 0
		for i := from; i <= m.lrow && lines <= visible; i++ {
			lines++
			if i == from || m.lib[i].kind != m.lib[i-1].kind {
				lines += 2 // the head, and the blank before it
				if i == 0 {
					lines-- // the list's first section has no blank above it
				}
			}
		}
		return lines
	}
	start := m.lrow
	for start > 0 && spend(start-1) <= visible {
		start--
	}
	last, lines := "", 0
	for i := start; i < len(m.lib) && lines < visible; i++ {
		r := m.lib[i]
		if r.kind != last {
			if last != "" || start > 0 {
				b.WriteString("\n")
				lines++
			}
			b.WriteString(pad + "  " + sFaint.Render(heads[r.kind]) + "\n")
			lines++
			last = r.kind
		}
		marker, name := "  ", sText.Render(padRight(trunc(r.name, nameW), nameW))
		if i == m.lrow {
			marker, name = sCursor.Render("▸ "), sNameOn.Render(padRight(trunc(r.name, nameW), nameW))
		}
		var agents []string
		for _, id := range r.on {
			agents = append(agents, m.libName(id))
		}
		on := sMuted.Render(strings.Join(agents, ", "))
		switch {
		case r.own:
			on = sFaint.Render(strings.Join(agents, ", ") + "'s own")
		case len(agents) == 0 && strings.HasPrefix(r.kind, "found-"):
		case len(agents) == 0:
			on = sFaint.Render("no agent")
		}
		line := pad + marker + name + "  " + on
		if r.bad > 0 {
			line += "  " + sBad.Render(fmt.Sprintf("! %d", r.bad))
		}
		if r.about != "" {
			if room := m.w - lipgloss.Width(line) - 4; room > 8 {
				line += "  " + sFaint.Render(trunc(r.about, room))
			}
		}
		b.WriteString(line + "\n")
		lines++
	}
	b.WriteString("\n" + pad + "  " + sFaint.Render("kept in "+tilde(m.libView.Dir)))
	return b.String()
}

// trunc cuts s to n runes, with an ellipsis.
func trunc(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:max(1, n-1)]) + "…"
	}
	return s
}

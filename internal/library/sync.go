package library

import (
	"fmt"
	"slices"
)

// Result is what a change did to the agents.
type Result struct {
	Changed  []string  `json:"changed"`  // agents whose files were written
	Problems []Problem `json:"problems"` // what couldn't be written, and why
	Backup   string    `json:"backup,omitempty"`
	// Missing are the servers and skills (mcp:<name>, skill:<name>) a
	// profile named that the library no longer has
	Missing []string `json:"missing,omitempty"`
	// Updated and Unupdated are, for an update of every skill, the ones
	// fetched again and the ones that couldn't be (What is skill:<name>)
	Updated   []string  `json:"updated,omitempty"`
	Unupdated []Problem `json:"unupdated,omitempty"`
	// Unimported are, for skills brought in together, the ones that
	// couldn't be (What is skill:<name>)
	Unimported []Problem `json:"unimported,omitempty"`
	// Unremoved are, for skills taken out together, the ones that couldn't
	// be (What is skill:<name>)
	Unremoved []Problem `json:"unremoved,omitempty"`
}

// Problem is one thing that couldn't be given to an agent.
type Problem struct {
	Agent string `json:"agent"`
	What  string `json:"what"` // instructions, mcp:<name>, skill:<name>
	Error string `json:"error"`
	// Own is a skill the agent has a folder of its own for, not the
	// library's: the page offers to use the library's or keep the agent's
	Own bool `json:"own,omitempty"`
}

func (r *Result) changed(agent string) {
	if !slices.Contains(r.Changed, agent) {
		r.Changed = append(r.Changed, agent)
	}
}

func (r *Result) fail(agent, what string, err error) {
	r.Problems = append(r.Problems, Problem{Agent: agent, What: what, Error: err.Error()})
}

// sync writes the library into every agent on this machine.
func (l *Library) sync() *Result {
	res := &Result{Changed: []string{}, Problems: []Problem{}}
	b := l.kept
	if b == nil {
		b = newBackups()
	}
	all := Targets()
	for _, t := range all {
		l.syncInstructions(t, b, res)
		l.syncMCP(t, b, res)
	}
	// ~/.agents/skills first: an agent that reads it too gets no second
	// link to what is there already
	shared := realDir(sharedSkillsDir())
	for _, first := range []bool{true, false} {
		for _, t := range all {
			if (t.Skills != "" && realDir(t.Skills) == shared) == first {
				l.syncSkills(t, res, all)
			}
		}
	}
	l.syncProjects(res)
	res.Backup = b.dir
	if b.dir != "" {
		pruneBackups()
	}
	return res
}

// Sync writes the library into the agents again: after an agent is
// installed, or a profile brings another library in.
func Sync() (*Result, error) {
	return change(func(*Library) error { return nil })
}

func (l *Library) syncMCP(t *Target, b *backups, res *Result) {
	if t.MCP == nil {
		return
	}
	id := t.Agent.ID
	a := l.applied(id)
	// an agent given no server, with none of magpie's in it, isn't read:
	// a file of its that can't be read is nothing the library did
	if len(a.MCP) == 0 && !slices.ContainsFunc(l.MCP, func(s *Server) bool { return slices.Contains(s.Agents, id) }) {
		return
	}
	entries, err := t.MCP.entries()
	if err != nil {
		res.fail(id, "mcp", err)
		return
	}
	write := func(what string, f func() error) bool {
		for _, p := range t.MCP.files() {
			if err := b.keep(id, p); err != nil {
				res.fail(id, what, err)
				return false
			}
		}
		if err := f(); err != nil {
			res.fail(id, what, err)
			return false
		}
		res.changed(id)
		return true
	}
	var mine []string
	for _, name := range a.MCP {
		if s := l.server(name); s != nil && slices.Contains(s.Agents, id) && t.MCP.supports(s) == nil {
			continue
		}
		if _, ok := entries[name]; (ok || t.MCP.holds(name)) && !write("mcp:"+name, func() error { return t.MCP.del(name) }) {
			mine = append(mine, name)
		}
	}
	for _, s := range l.MCP {
		if !slices.Contains(s.Agents, id) {
			continue
		}
		if err := t.MCP.supports(s); err != nil {
			res.fail(id, "mcp:"+s.Name, err)
			continue
		}
		old := entries[s.Name]
		if old != nil {
			if cur, ok := t.MCP.decode(s.Name, old); ok && cur.same(s) && t.MCP.has(s) && !t.MCP.behind(s, old) {
				mine = append(mine, s.Name)
				continue
			}
		}
		if write("mcp:"+s.Name, func() error { return t.MCP.put(s, old) }) {
			mine = append(mine, s.Name)
		}
	}
	a.MCP = mine
}

// ---- the page -------------------------------------------------------------

// AgentView is an agent as the page lists it, with where it keeps each.
type AgentView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Icon         string `json:"icon"`
	Instructions string `json:"instructions,omitempty"`
	MCP          string `json:"mcp,omitempty"`
	Skills       string `json:"skills,omitempty"`
	// ProjectSkills is the folder in a project it reads skills from
	ProjectSkills string   `json:"projectSkills,omitempty"`
	SkillsAlso    []string `json:"skillsAlso,omitempty"`
	Note          string   `json:"note,omitempty"`
	NoSSE         bool     `json:"noSSE,omitempty"`
	NoRemote      bool     `json:"noRemote,omitempty"`
	MCPVia        string   `json:"mcpVia,omitempty"`
}

// ServerView is a library server, and what each agent it's on made of it.
type ServerView struct {
	*Server
	Icon string `json:"icon,omitempty"`
	// Problems are the agents it couldn't be given to, and why
	Problems map[string]string `json:"problems,omitempty"`
}

// SkillView is a library skill as the page shows it.
type SkillView struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Source      string            `json:"source,omitempty"`
	Kind        string            `json:"kind"`             // github, folder, or "" for one kept in the library
	Origin      string            `json:"origin,omitempty"` // on GitHub, as CC Switch installed it: it can be updated from there
	Icon        string            `json:"icon,omitempty"`
	Agents      []string          `json:"agents"`
	Missing     bool              `json:"missing,omitempty"` // its folder is gone
	Problems    map[string]string `json:"problems,omitempty"`
	Check       *SkillCheck       `json:"check,omitempty"` // what the last check for updates found
}

// View is the Library page.
type View struct {
	Agents       []AgentView       `json:"agents"`
	Servers      []ServerView      `json:"servers"`
	FoundServers []Found           `json:"foundServers"`
	Skills       []SkillView       `json:"skills"`
	FoundSkills  []FoundSkill      `json:"foundSkills"`
	Projects     []ProjectView     `json:"projects"`
	Instructions *InstructionsView `json:"instructions"`
	Dir          string            `json:"dir"`
	Backups      string            `json:"backups"`
}

// Read is the whole page: the library, and what's found in the agents.
// problems are those of the last change, for the page to keep showing.
func Read(problems []Problem) (*View, error) {
	iv, err := ReadInstructions()
	if err != nil {
		return nil, err
	}
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return nil, err
	}
	v := &View{Agents: []AgentView{}, Servers: []ServerView{}, Skills: []SkillView{}, Instructions: iv, Dir: Dir(), Backups: BackupDir()}
	targets := Targets()
	for _, t := range targets {
		av := AgentView{ID: t.Agent.ID, Name: t.Agent.Name, Icon: t.Agent.Icon, Instructions: t.Instructions, Skills: t.Skills,
			SkillsAlso: t.SkillsAlso, Note: t.Note, MCPVia: t.MCPVia, ProjectSkills: ProjectSkillsDir(t.Agent.ID)}
		if t.MCP != nil {
			av.MCP = t.MCP.Path
			av.NoSSE = t.MCP.supports(&Server{Transport: "sse"}) != nil
			av.NoRemote = t.MCP.supports(&Server{Transport: "http"}) != nil
		}
		v.Agents = append(v.Agents, av)
	}
	of := func(what string) map[string]string {
		m := map[string]string{}
		for _, p := range problems {
			if p.What == what {
				m[p.Agent] = p.Error
			}
		}
		if len(m) == 0 {
			return nil
		}
		return m
	}
	for _, s := range l.MCP {
		// one on no agent yet has none, not null, for the page to look in
		if s.Agents == nil {
			c := *s
			c.Agents = []string{}
			s = &c
		}
		sv := ServerView{Server: s, Icon: serverIcon(l, s), Problems: of("mcp:" + s.Name)}
		for _, t := range targets {
			if t.MCP != nil && slices.Contains(s.Agents, t.Agent.ID) {
				if err := t.MCP.supports(s); err != nil {
					if sv.Problems == nil {
						sv.Problems = map[string]string{}
					}
					sv.Problems[t.Agent.ID] = err.Error()
				}
			}
		}
		v.Servers = append(v.Servers, sv)
	}
	for _, s := range l.Skills {
		sv := SkillView{Name: s.Name, Agents: append([]string{}, s.Agents...), Icon: skillIcon(s), Problems: of("skill:" + s.Name)}
		if s.Source != nil {
			sv.Source, sv.Kind = s.Source.String(), s.Source.Kind
		}
		if o, ok := ccSwitchOrigin(s); ok {
			o.Path = ""
			sv.Origin = o.String()
		}
		if m, ok := readMeta(skillDir(s.Name)); ok {
			sv.Description = m.Description
		} else {
			sv.Missing = true
		}
		sv.Check = lastCheck(s.Name)
		v.Skills = append(v.Skills, sv)
	}
	v.FoundServers = foundServers(l)
	for i := range v.FoundServers {
		v.FoundServers[i].Icon = serverIcon(l, v.FoundServers[i].Server)
	}
	v.FoundSkills = foundSkills(l)
	v.Projects = projectViews(l, problems)
	return v, nil
}

// ---- servers --------------------------------------------------------------

// SaveServer adds a server, or replaces the one called old (renaming it).
func SaveServer(old string, s Server) (*Result, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	return change(func(l *Library) error {
		if s.Name != old && l.server(s.Name) != nil {
			return fmt.Errorf("the library already has a server called %s", s.Name)
		}
		if old != "" {
			i := slices.IndexFunc(l.MCP, func(x *Server) bool { return x.Name == old })
			if i < 0 {
				return fmt.Errorf("no server called %s", old)
			}
			l.MCP = slices.Delete(l.MCP, i, i+1)
		}
		s.Agents = slices.Sorted(slices.Values(s.Agents))
		l.MCP = append(l.MCP, &s)
		return nil
	})
}

// ServerAgents sets which agents get a server.
func ServerAgents(name string, agents []string) (*Result, error) {
	return change(func(l *Library) error {
		s := l.server(name)
		if s == nil {
			return fmt.Errorf("no server called %s", name)
		}
		s.Agents = slices.Sorted(slices.Values(agents))
		return nil
	})
}

// EveryServerAgents gives every server in the library to the agents named,
// or takes every one from them, in one write rather than one for each
// server (#475). An agent not named keeps what it has, as with a server's
// All chip; on gives an agent only the servers it can reach (no SSE for
// Codex, no remote one for Claude Desktop), as its chips can't be lit for
// the others.
func EveryServerAgents(agents []string, on bool) (*Result, error) {
	if len(agents) == 0 {
		return nil, fmt.Errorf("no agents to give the servers to")
	}
	return change(func(l *Library) error {
		mcp := map[string]*mcpFile{}
		if on {
			for _, t := range Targets() {
				if t.MCP != nil {
					mcp[t.Agent.ID] = t.MCP
				}
			}
		}
		for _, s := range l.MCP {
			kept := slices.DeleteFunc(slices.Clone(s.Agents), func(a string) bool { return slices.Contains(agents, a) })
			for _, a := range agents {
				if on && mcp[a] != nil && mcp[a].supports(s) == nil {
					kept = append(kept, a)
				} else if on && slices.Contains(s.Agents, a) {
					kept = append(kept, a) // one it has already stays, whatever it says of it
				}
			}
			s.Agents = slices.Sorted(slices.Values(kept))
		}
		return nil
	})
}

// RemoveServer takes a server out of the library and out of every agent
// magpie gave it to.
func RemoveServer(name string) (*Result, error) {
	return change(func(l *Library) error {
		i := slices.IndexFunc(l.MCP, func(x *Server) bool { return x.Name == name })
		if i < 0 {
			return fmt.Errorf("no server called %s", name)
		}
		k := serverKey(l.MCP[i])
		l.MCP = slices.Delete(l.MCP, i, i+1)
		// its icon goes with it, unless another server runs the same thing
		if !slices.ContainsFunc(l.MCP, func(x *Server) bool { return serverKey(x) == k }) {
			delete(l.Icons, k)
		}
		return nil
	})
}

// ImportServer takes a server the agents have into the library: the agents
// that have it as it is get the library's from then on, the same entry.
func ImportServer(name string) (*Result, error) {
	return change(func(l *Library) error {
		for _, f := range foundServers(l) {
			if f.Server.Name != name {
				continue
			}
			s := f.Server
			s.Agents = slices.Sorted(slices.Values(s.Agents))
			l.MCP = append(l.MCP, s)
			for _, id := range s.Agents {
				a := l.applied(id)
				if !slices.Contains(a.MCP, name) {
					a.MCP = append(a.MCP, name)
				}
			}
			return nil
		}
		return fmt.Errorf("no agent has a server called %s that the library hasn't", name)
	})
}

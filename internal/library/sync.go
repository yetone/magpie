package library

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/yetone/magpie/internal/mcpauth"
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
	// Unremoved are, for skills or servers taken out together, the ones
	// that couldn't be (What is skill:<name> or mcp:<name>)
	Unremoved []Problem `json:"unremoved,omitempty"`
	// Installed, Had and Skipped are, for skills installed together, the
	// ones added, the ones the library had already from the same source,
	// and the ones left out with why: another skill by that name, the
	// user's, is never written over (What is skill:<name>)
	Installed []string  `json:"installed,omitempty"`
	Had       []string  `json:"had,omitempty"`
	Skipped   []Problem `json:"skipped,omitempty"`
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
	// an edit made in an agent's copy of a skill goes to the library first,
	// for the others to get rather than be undone (takeEdits)
	l.takeEdits(all, res)
	// ~/.agents/skills first: an agent that reads it too gets no second
	// link to what is there already
	shared := realDir(sharedSkillsDir())
	for _, first := range []bool{true, false} {
		for _, t := range all {
			switch {
			case t.Desktop != nil:
				if !first {
					l.syncDesktopSkills(t, res)
				}
			case (t.Skills != "" && realDir(t.Skills) == shared) == first:
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
	base := gatewayOf(t)
	for _, s := range l.MCP {
		if !slices.Contains(s.Agents, id) {
			continue
		}
		s := through(s, base)
		if err := t.MCP.supports(s); err != nil {
			res.fail(id, "mcp:"+s.Name, err)
			continue
		}
		s, _ = t.MCP.side(s)
		old := entries[s.Name]
		if old != nil {
			if cur, ok := t.MCP.current(s.Name, old, s); ok && cur.same(s) && t.MCP.has(s) && !t.MCP.behind(s, old) {
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
	ProjectSkills string `json:"projectSkills,omitempty"`
	// ProjectMCP is the file, in a project, it reads MCP servers from
	ProjectMCP string `json:"projectMCP,omitempty"`
	// ProjectNoSSE: its project file can't take a server over SSE
	ProjectNoSSE bool     `json:"projectNoSSE,omitempty"`
	SkillsAlso   []string `json:"skillsAlso,omitempty"`
	Note         string   `json:"note,omitempty"`
	NoSSE        bool     `json:"noSSE,omitempty"`
	NoRemote     bool     `json:"noRemote,omitempty"`
	// NoEnvRefs: it reads no ${NAME} from its MCP config, so a server
	// whose headers or environment have one isn't given to it (envref.go)
	NoEnvRefs bool   `json:"noEnvRefs,omitempty"`
	MCPVia    string `json:"mcpVia,omitempty"`
	// How is the way it is given its skills, link or copy, and HowOwn its
	// own over the library's; MustCopy is one that can only take copies
	// (in WSL)
	How      string `json:"how,omitempty"`
	HowOwn   bool   `json:"howOwn,omitempty"`
	MustCopy bool   `json:"mustCopy,omitempty"`
}

// ServerView is a library server, and what each agent it's on made of it.
type ServerView struct {
	*Server
	Icon string `json:"icon,omitempty"`
	// Problems are the agents it couldn't be given to, and why
	Problems map[string]string `json:"problems,omitempty"`
	// SignIn is magpie's sign-in to a streamable HTTP server, which the
	// agents then use (#615)
	SignIn *mcpauth.Status `json:"signIn,omitempty"`
}

// SkillView is a library skill as the page shows it.
type SkillView struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Source      string            `json:"source,omitempty"`
	Kind        string            `json:"kind"`             // github, folder, or "" for one kept in the library
	Origin      string            `json:"origin,omitempty"` // on GitHub, as CC Switch installed it: it can be updated from there
	Repo        string            `json:"repo,omitempty"`   // the repository it came from, however it came in, for the page to group by
	Icon        string            `json:"icon,omitempty"`
	Agents      []string          `json:"agents"`
	Missing     bool              `json:"missing,omitempty"` // its folder is gone
	Problems    map[string]string `json:"problems,omitempty"`
	Check       *SkillCheck       `json:"check,omitempty"` // what the last check for updates found
	// Always are the agents that have it whatever the library gives them:
	// it is kept in ~/.agents/skills, which they read themselves (#595)
	Always []string `json:"always,omitempty"`
	// Behind are the agents given it as a copy whose copy differs from it,
	// till the next sync makes it again
	Behind []string `json:"behind,omitempty"`
}

// View is the Library page.
type View struct {
	Agents       []AgentView  `json:"agents"`
	Servers      []ServerView `json:"servers"`
	FoundServers []Found      `json:"foundServers"`
	Skills       []SkillView  `json:"skills"`
	FoundSkills  []FoundSkill `json:"foundSkills"`
	// NewSkills are skills a check found its skills' repositories to have
	// added beside them, to add or set aside
	NewSkills    []NewSkill        `json:"newSkills"`
	Projects     []ProjectView     `json:"projects"`
	Instructions *InstructionsView `json:"instructions"`
	Dir          string            `json:"dir"`
	Backups      string            `json:"backups"`
	SkillGroups  []SkillGroup      `json:"skillGroups"`
	CopySkills   bool              `json:"copySkills"` // the library gives skills as copies (#896)
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
	v := &View{Agents: []AgentView{}, Servers: []ServerView{}, Skills: []SkillView{}, Instructions: iv, Dir: Dir(), Backups: BackupDir(), CopySkills: l.CopySkills}
	targets := Targets()
	behind := l.behind(targets)
	for _, t := range targets {
		av := AgentView{ID: t.Agent.ID, Name: t.Agent.Name, Icon: t.Agent.Icon, Instructions: t.Instructions, Skills: t.Skills,
			SkillsAlso: t.SkillsAlso, Note: t.Note, MCPVia: t.MCPVia, ProjectSkills: ProjectSkillsDir(t.Agent.ID), ProjectMCP: ProjectMCPFile(t.Agent.ID),
			ProjectNoSSE: ProjectNoSSE(t.Agent.ID)}
		if t.Skills != "" {
			_, av.HowOwn = l.SkillHow[t.Agent.ID]
			av.How, av.MustCopy = l.howOf(t.Agent.ID), t.Copy || t.Desktop != nil
			if av.MustCopy {
				av.How = HowCopy
			}
		}
		if t.MCP != nil {
			av.MCP = t.MCP.Path
			av.NoSSE = t.MCP.supports(&Server{Transport: "sse"}) != nil
			av.NoRemote = t.MCP.supports(&Server{Transport: "http"}) != nil
			av.NoEnvRefs = t.MCP.refsOf() == envSyntax{}
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
		if s.Transport == "http" {
			st := mcpauth.StatusOf(s.Name, s.URL)
			sv.SignIn = &st
		}
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
	tr := &tracer{}
	for _, s := range l.Skills {
		sv := SkillView{Name: s.Name, Agents: append([]string{}, s.Agents...), Icon: skillIcon(s), Problems: of("skill:" + s.Name), Repo: tr.repoOf(s)}
		if s.Source != nil {
			sv.Source, sv.Kind = s.Source.String(), s.Source.Kind
		}
		// linked from a folder elsewhere is what the library's entry is, not
		// what was written down when it came in: a linked folder moved in by
		// hand is the library's own now, and Remove moves it to the backups
		// (#595)
		if p := skillDir(s.Name); linked(p) {
			if sv.Kind != "folder" || realDir(s.Source.Dir) != realDir(p) {
				sv.Kind, sv.Source = "folder", realDir(p)
			}
		} else if _, err := os.Lstat(p); err == nil && sv.Kind == "folder" {
			sv.Kind, sv.Source = "", ""
		}
		if sharedHas(s.Name) {
			for _, t := range targets {
				if t.Skills != "" && (slices.Contains(readsShared, t.Agent.ID) || realDir(t.Skills) == realDir(sharedSkillsDir())) {
					sv.Always = append(sv.Always, t.Agent.ID)
				}
			}
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
		sv.Behind = behind[s.Name]
		v.Skills = append(v.Skills, sv)
	}
	v.FoundServers = foundServers(l)
	for i := range v.FoundServers {
		v.FoundServers[i].Icon = serverIcon(l, v.FoundServers[i].Server)
	}
	v.FoundSkills = foundSkills(l)
	v.NewSkills = newSkills(l)
	v.Projects = projectViews(l, problems)
	v.SkillGroups = l.skillGroups()
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
			// magpie's sign-in goes with a renamed server, before the agents
			// are given it under its new name
			if old != s.Name {
				if err := mcpauth.Rename(old, s.Name); err != nil {
					return err
				}
			}
			l.MCP = slices.Delete(l.MCP, i, i+1)
			if old != s.Name {
				l.renameProjectServers(old, s.Name)
			}
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
	return serversAgents(nil, agents, on)
}

// SomeServersAgents does as EveryServerAgents for the servers named alone:
// the ones picked on the page (#1027).
func SomeServersAgents(names, agents []string, on bool) (*Result, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("no servers to give the agents")
	}
	if len(agents) == 0 {
		return nil, fmt.Errorf("no agents to give the servers to")
	}
	return serversAgents(names, agents, on)
}

// serversAgents gives the servers named, or every one when none are, to
// the agents, or takes them from them.
func serversAgents(names, agents []string, on bool) (*Result, error) {
	return change(func(l *Library) error {
		for _, name := range names {
			if l.server(name) == nil {
				return fmt.Errorf("no server called %s", name)
			}
		}
		mcp := map[string]*mcpFile{}
		if on {
			for _, t := range Targets() {
				if t.MCP != nil {
					mcp[t.Agent.ID] = t.MCP
				}
			}
		}
		for _, s := range l.MCP {
			if names != nil && !slices.Contains(names, s.Name) {
				continue
			}
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
	res, err := change(func(l *Library) error { return removeServer(l, name) })
	if err == nil {
		// its sign-in goes with it
		_ = mcpauth.Forget(name)
	}
	return res, err
}

// RemoveServers takes the servers named out of the library and every agent
// in one write (#1027), each as RemoveServer does: only the entries magpie
// wrote (Applied) leave the agents' files, a server of the agent's own by
// another name stays, and each one's sign-in goes with it. One that can't
// be taken out stays, and is said in Unremoved.
func RemoveServers(names []string) (*Result, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("no servers to remove")
	}
	names = slices.Compact(slices.Sorted(slices.Values(names)))
	var failed []Problem
	var gone []string
	res, err := change(func(l *Library) error {
		failed, gone = nil, nil
		for _, name := range names {
			if err := removeServer(l, name); err != nil {
				failed = append(failed, Problem{What: "mcp:" + name, Error: err.Error()})
			} else {
				gone = append(gone, name)
			}
		}
		if len(failed) == len(names) {
			return errors.New(failed[0].Error)
		}
		return nil
	})
	if err == nil {
		for _, name := range gone {
			_ = mcpauth.Forget(name)
		}
	}
	if res != nil {
		res.Unremoved = failed
	}
	return res, err
}

// removeServer takes a server out of l and its projects; its icon goes
// with it, unless another server runs the same thing.
func removeServer(l *Library, name string) error {
	i := slices.IndexFunc(l.MCP, func(x *Server) bool { return x.Name == name })
	if i < 0 {
		return fmt.Errorf("no server called %s", name)
	}
	k := serverKey(l.MCP[i])
	l.MCP = slices.Delete(l.MCP, i, i+1)
	for _, p := range l.Projects {
		delete(p.Servers, name)
	}
	if !slices.ContainsFunc(l.MCP, func(x *Server) bool { return serverKey(x) == k }) {
		delete(l.Icons, k)
	}
	return nil
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

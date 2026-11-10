package library

import (
	"errors"
	"fmt"
	"log"
	"os"
	"slices"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/mcpauth"
	"github.com/yetone/magpie/internal/settings"
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
	// Gone are, for an update of skills, the ones their sources no longer
	// have: removed there, not failed (skill_gone.go)
	Gone []string `json:"gone,omitempty"`
	// Unimported are, for skills brought in together, the ones that
	// couldn't be (What is skill:<name>)
	Unimported []Problem `json:"unimported,omitempty"`
	// Unremoved are, for skills or servers taken out together, the ones
	// that couldn't be (What is skill:<name> or mcp:<name>)
	Unremoved []Problem `json:"unremoved,omitempty"`
	// Unmoved are, for skills handed over from ~/.agents/skills, the ones
	// that couldn't be moved and stay where they were (What is skill:<name>)
	Unmoved []Problem `json:"unmoved,omitempty"`
	// Installed, Had and Skipped are, for skills installed together, the
	// ones added, the ones the library had already from the same source,
	// and the ones left out with why: another skill by that name, the
	// user's, is never written over (What is skill:<name>)
	Installed []string  `json:"installed,omitempty"`
	Had       []string  `json:"had,omitempty"`
	Skipped   []Problem `json:"skipped,omitempty"`
	// Linked are, for skills installed from GitHub, the ones the library
	// had from a folder by that name that now follow GitHub, with its
	// files (skill_link.go); LinkedKept those of them whose own files
	// weren't GitHub's, kept with the backups
	Linked     []string `json:"linked,omitempty"`
	LinkedKept []string `json:"linkedKept,omitempty"`
	// dropped are the servers taken out of an agent's config, as it had
	// them, whose processes go once no agent runs them (mcp_reap.go)
	dropped []*Server
	// run are the servers given Codex as the gateway's, which magpie runs
	// for it (mcp_run.go), by name
	run map[string]runSpec
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
	for _, step := range syncSteps(all, l.held(all), b, res) {
		step(l)
	}
	res.Backup = b.dir
	if b.dir != "" {
		pruneBackups()
	}
	runGiven(res.run)
	return res
}

// syncSteps are what a sync does, one agent's part at a time, in order. The
// agents gone from here that still have what magpie gave them (held) only
// have what was unticked taken out, after the rest.
func syncSteps(all, gone []*Target, b *backups, res *Result) []func(*Library) {
	// a skill whose app moved it is followed first, for every agent to
	// get the moved one (skill_moved.go)
	steps := []func(*Library){func(l *Library) { l.followMovedSkills() }}
	for _, t := range all {
		steps = append(steps, func(l *Library) {
			l.syncInstructions(t, b, res, true)
			l.syncMCP(t, b, res)
			l.syncHooks(t, b, res)
			l.syncSubagents(t, b, res)
		})
	}
	// an edit made in an agent's copy of a skill goes to the library first,
	// for the others to get rather than be undone (takeEdits); one in a
	// gone agent's copy too, before that copy is taken out
	steps = append(steps, func(l *Library) { l.takeEdits(append(slices.Clone(all), gone...), res) })
	for _, t := range gone {
		steps = append(steps, func(l *Library) {
			l.syncMCP(t, b, res)
			l.syncHooks(t, b, res)
			l.syncSkills(t, res, all)
		})
	}
	// ~/.agents/skills first: an agent that reads it too gets no second
	// link to what is there already
	shared := realDir(sharedSkillsDir())
	steps = append(steps, func(l *Library) { l.sweepShared(append(slices.Clone(all), gone...), res) })
	steps = append(steps, func(l *Library) { l.syncShared(all, res) })
	for _, first := range []bool{true, false} {
		for _, t := range all {
			switch {
			case t.Desktop != nil:
				if !first {
					steps = append(steps, func(l *Library) { l.syncDesktopSkills(t, res) })
				}
			case (t.Skills != "" && realDir(t.Skills) == shared) == first:
				steps = append(steps, func(l *Library) { l.syncSkills(t, res, all) })
			}
		}
	}
	// magpie's copies in a WSL distro no agent there is linked to any more
	steps = append(steps, func(l *Library) { l.sweepWSLStores(append(slices.Clone(all), gone...), res) })
	return append(steps, func(l *Library) { l.syncProjects(res) })
}

// Sync writes the library into the agents again: after an agent is
// installed, or a profile brings another library in. magpie does one at
// every start, and the Library page waits on the lock for each read and
// change: held for the whole sync, through every agent's skills, the
// first operations after opening magpie took seconds (#1624, Hipple).
// It lets go after each agent's part, for whatever waits to go first.
func Sync() (*Result, error) {
	// a plugin's tools, a server of their own (plugin_tools.go)
	if _, err := adoptPluginTools(); err != nil {
		log.Printf("library: giving plugin tools a server: %v", err)
	}
	res := &Result{Changed: []string{}, Problems: []Problem{}}
	b := newBackups()
	var all, gone []*Target
	// what the step before learnt of the skills (hashes, copies found
	// untouched), kept while nothing else held the lock between
	var memo *Library
	var heldAt uint64
	step := func(f func(*Library)) error {
		mu.Lock()
		defer mu.Unlock()
		l, err := load()
		if err != nil {
			return err
		}
		if memo != nil && mu.held == heldAt+1 {
			l.hashes, l.untouched = memo.hashes, memo.untouched
		}
		f(l)
		if err := l.save(); err != nil {
			return err
		}
		memo, heldAt = l, mu.held
		return nil
	}
	// the agents are found under the lock, as a change finds them
	var npxDirs map[string]bool
	if err := step(func(l *Library) { all = Targets(); gone = l.held(all); npxDirs = l.npxDirs() }); err != nil {
		return nil, err
	}
	for _, f := range syncSteps(all, gone, b, res) {
		if err := step(f); err != nil {
			return nil, err
		}
	}
	res.Backup = b.dir
	if b.dir != "" {
		mu.Lock()
		pruneBackups()
		mu.Unlock()
	}
	if len(res.dropped) > 0 {
		ending(res.dropped)
	}
	npxPrune(npxDirs)
	runGiven(res.run)
	return res, nil
}

func (l *Library) syncMCP(t *Target, b *backups, res *Result) {
	if res.unreached(t) {
		return
	}
	if t.MCP == nil {
		return
	}
	id := t.Agent.ID
	base := gatewayOf(t)
	run := runsFor(t, base)
	if t.MCP.Format == fmtCodex && t.Agent != nil {
		runs.Lock()
		if runs.wired == nil {
			runs.wired = map[string]bool{}
		}
		runs.wired[id] = run
		runs.Unlock()
	}
	a := l.applied(id)
	// an agent given no server, with none of magpie's in it, isn't read:
	// a file of its that can't be read is nothing the library did
	if len(a.MCP) == 0 && len(a.ToolRules) == 0 && (t.Gone || !slices.ContainsFunc(l.MCP, func(s *Server) bool { return slices.Contains(s.Agents, id) })) {
		return
	}
	how := toolsOffOf(t)
	toolsOff := map[string][]string{} // what magpie's in each entry's list now
	startup := map[string]float64{}   // the startup timeout magpie's in each entry now
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
	// with the hub on, the agent is given magpie's one entry in place of
	// the servers behind it (mcp_hub.go); one that can't take it keeps
	// each server's own
	hub, _ := l.hubFor(t, a, entries)
	if t.Gone {
		// nor is a gone agent given the hub anew
		hub = nil
		if l.MCPHub && slices.Contains(a.MCP, HubEntry) && l.server(HubEntry) == nil &&
			slices.ContainsFunc(l.MCP, func(s *Server) bool { return slices.Contains(s.Agents, id) && inHub(s, id) }) {
			hub = l.hubServerFor(t)
		}
	}
	hubbed := func(s *Server) bool { return hub != nil && inHub(s, id) }
	var mine []string
	for _, name := range a.MCP {
		if s := l.server(name); s != nil && slices.Contains(s.Agents, id) && t.MCP.supports(s) == nil && !hubbed(s) {
			continue
		}
		if name == HubEntry && hub != nil {
			continue
		}
		e, ok := entries[name]
		if !ok && !t.MCP.holds(name) {
			continue
		}
		if !write("mcp:"+name, func() error { return t.MCP.del(name) }) {
			mine = append(mine, name)
			toolsOff[name] = a.ToolsOff[name]
			startup[name] = a.Startup[name]
		} else if had, ok := t.MCP.decode(name, e); ok && e != nil {
			res.dropped = append(res.dropped, had)
		}
	}
	given := []*Server{}
	for _, s := range l.MCP {
		if slices.Contains(s.Agents, id) && !hubbed(s) {
			given = append(given, s)
		}
	}
	if hub != nil {
		given = append(given, hub)
	}
	for _, s := range given {
		// an agent gone from here is given nothing: what it has still
		// stays its, until unticked
		if t.Gone {
			if slices.Contains(a.MCP, s.Name) {
				mine = append(mine, s.Name)
				toolsOff[s.Name] = a.ToolsOff[s.Name]
				startup[s.Name] = a.Startup[s.Name]
			}
			continue
		}
		if s != hub {
			s = through(s, base)
		}
		if err := t.MCP.supports(s); err != nil {
			res.fail(id, "mcp:"+s.Name, err)
			continue
		}
		s, _ = t.MCP.side(s)
		if !t.MCP.WSL {
			s = npxDirect(s)
		}
		// a server the user keeps direct is started by Codex itself, in
		// the thread's folder (#4382): magpie would start it from home
		if run && s.Hub != HubDirect {
			if via, spec, ok := runVia(s, base); ok {
				if res.run == nil {
					res.run = map[string]runSpec{}
				}
				s, res.run[s.Name] = via, spec
			}
		}
		s = t.MCP.withCmdVars(s)
		s = wroteStartup(s, a.Startup[s.Name])
		old := entries[s.Name]
		if err := t.MCP.foreign(old); err != nil {
			res.fail(id, "mcp:"+s.Name, err)
			continue
		}
		// the tools turned off, in the list its entry has of them
		var off *toolsEdit
		prev := a.ToolsOff[s.Name]
		offMine := prev
		if how == toolsInEntry {
			e, m, err := entryToolsEdit(t.MCP, old, prev, s.DisabledTools)
			if err != nil {
				res.fail(id, "mcp:"+s.Name, err)
			}
			off, offMine = e, m
		}
		if old != nil && off == nil {
			if cur, ok := t.MCP.current(s.Name, old, s); ok && cur.same(s) && t.MCP.has(s) && !t.MCP.behind(s, old) {
				mine = append(mine, s.Name)
				toolsOff[s.Name] = offMine
				startup[s.Name] = t.MCP.startupWritten(s, old)
				continue
			}
		}
		if write("mcp:"+s.Name, func() error { return t.MCP.putEntry(s, off.apply(t.MCP.merged(s, old))) }) {
			mine = append(mine, s.Name)
			toolsOff[s.Name] = offMine
			startup[s.Name] = t.MCP.startupWritten(s, old)
		} else if old != nil {
			toolsOff[s.Name] = prev // the entry is as it was
			startup[s.Name] = a.Startup[s.Name]
		}
	}
	a.MCP = mine
	for k, v := range startup {
		if v == 0 || !slices.Contains(mine, k) {
			delete(startup, k)
		}
	}
	a.Startup = startup
	if len(a.Startup) == 0 {
		a.Startup = nil
	}
	for k, v := range toolsOff {
		if len(v) == 0 {
			delete(toolsOff, k)
		}
	}
	a.ToolsOff = toolsOff
	if len(a.ToolsOff) == 0 {
		a.ToolsOff = nil
	}
	if how == toolsClaudeDeny || how == toolsOpenCode {
		l.syncToolRules(t, how, mine, b, res)
	}
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
	ProjectNoSSE bool `json:"projectNoSSE,omitempty"`
	// ProjectOff and ProjectSkillOff: a server, or a skill, it has from
	// the Library can be turned off for it in one project (project_scope.go)
	ProjectOff      bool     `json:"projectOff,omitempty"`
	ProjectSkillOff bool     `json:"projectSkillOff,omitempty"`
	SkillsAlso      []string `json:"skillsAlso,omitempty"`
	Note            string   `json:"note,omitempty"`
	NoSSE           bool     `json:"noSSE,omitempty"`
	NoRemote        bool     `json:"noRemote,omitempty"`
	// NoEnvRefs: it reads no ${NAME} from its MCP config, so a server
	// whose headers or environment have one isn't given to it (envref.go)
	NoEnvRefs bool `json:"noEnvRefs,omitempty"`
	// ToolsOff: it can be told to leave out single tools of a server
	// (mcp_tools_off.go); the page names those that can't
	ToolsOff bool `json:"toolsOff,omitempty"`
	// Startup: it is given a server's startup timeout (Codex's
	// startup_timeout_sec)
	Startup bool   `json:"startup,omitempty"`
	MCPVia  string `json:"mcpVia,omitempty"`
	// Hub, with the MCP hub on, is "on" for an agent given magpie's one
	// entry in place of its servers, or why it keeps theirs: no-remote
	// (it reaches no server over HTTP), taken (a library server is called
	// magpie) or own (it has an entry called magpie of its own); "" for one
	// given none of the hub's servers
	Hub string `json:"hub,omitempty"`
	// SkillRouter, with the skill router on, is "on" for an agent whose
	// skills folder is given magpie-skills, or why it keeps each skill's
	// own link (routerWhy): copies, liveagent, shared, taken or own
	SkillRouter string `json:"skillRouter,omitempty"`
	// How is the way it is given its skills, link or copy, and HowOwn its
	// own over the library's; MustCopy is one that can only take copies
	// (in WSL)
	How      string `json:"how,omitempty"`
	HowOwn   bool   `json:"howOwn,omitempty"`
	MustCopy bool   `json:"mustCopy,omitempty"`
	// Gone: not installed here any more, and listed only for what magpie
	// gave it to be taken out (held); it is given nothing new
	Gone bool `json:"gone,omitempty"`
	// Asleep is the WSL distro, not running now, the agent is in: it is
	// listed by what magpie last knew of it (wsl_asleep.go), and a change
	// for it is written there when the distro next runs. Unseen: magpie
	// never saw where it keeps its MCP servers or skills, so its ticks
	// can't be shown or changed until the distro runs
	Asleep string `json:"asleep,omitempty"`
	Unseen bool   `json:"unseen,omitempty"`
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
	Bundle      string            `json:"bundle,omitempty"` // the plugin it came with
	Description string            `json:"description"`
	Source      string            `json:"source,omitempty"`
	Kind        string            `json:"kind"`             // github, git, folder, or "" for one kept in the library
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
	// Shared is its entry in ~/.agents/skills when it is kept there:
	// HandOverSkills moves it into the library's own folder
	Shared string `json:"shared,omitempty"`
	// SharedElsewhere: that entry only links to a folder kept elsewhere,
	// which stays there when it's handed over; only the link goes
	SharedElsewhere bool `json:"sharedElsewhere,omitempty"`
	// Behind are the agents given it as a copy whose copy differs from it,
	// till the next sync makes it again
	Behind []string `json:"behind,omitempty"`
	// Edited is set for one from GitHub changed here since it was fetched,
	// in the library or in an agent's copy: an update replaces that, so
	// the page asks first (#1449)
	Edited bool `json:"edited,omitempty"`
	// Gone is when its source was found without it: removed there, the
	// page offers to take it out (skill_gone.go)
	Gone string `json:"gone,omitempty"`
	// OffHere is set when it is turned off on this computer alone: Agents
	// are still the ones a sync carries to the others
	OffHere bool `json:"offHere,omitempty"`
	// Note is the user's own note on it (Skill.Note)
	Note string `json:"note,omitempty"`
	// Manual: its SKILL.md sets disable-model-invocation (meta.Manual)
	Manual bool `json:"manual,omitempty"`
	// Router is "direct" for one the skill router leaves out (Skill.Router)
	Router string `json:"router,omitempty"`
}

// View is the Library page.
type View struct {
	Agents  []AgentView  `json:"agents"`
	Servers []ServerView `json:"servers"`
	// Hooks are the library's hooks, and HookAgents what each agent on
	// this computer can take of them (hooks.go)
	Hooks      []HookView      `json:"hooks"`
	HookAgents []HookAgentView `json:"hookAgents"`
	// Subagents are the library's subagents, and SubagentAgents what
	// each agent on this computer makes of them (subagents.go)
	Subagents      []SubagentView      `json:"subagents"`
	SubagentAgents []SubagentAgentView `json:"subagentAgents"`
	FoundServers   []Found             `json:"foundServers"`
	// Plugins are the plugins kept as one item each (plugin.go)
	Plugins     []PluginView `json:"plugins"`
	Skills      []SkillView  `json:"skills"`
	FoundSkills []FoundSkill `json:"foundSkills"`
	// NewSkills are skills a check found its skills' repositories, or
	// the folders they were added from, to have beside them since, to
	// add or set aside
	NewSkills []NewSkill `json:"newSkills"`
	// CheckFolders is whether a check looks in folders the user added
	// skills from, which the page then offers with no remote skill
	CheckFolders bool              `json:"checkFolders,omitempty"`
	Projects     []ProjectView     `json:"projects"`
	Instructions *InstructionsView `json:"instructions"`
	Dir          string            `json:"dir"`
	Backups      string            `json:"backups"`
	SkillGroups  []SkillGroup      `json:"skillGroups"`
	CopySkills   bool              `json:"copySkills"` // the library gives skills as copies (#896)
	// SharedSkills: every skill on here is in ~/.agents/skills too
	// (skills_shared.go), at SharedDir
	SharedSkills bool   `json:"sharedSkills"`
	SharedDir    string `json:"sharedDir"`
	// MCPHub: the agents are given magpie's one MCP server in place of
	// the library's (#2679), at HubPath<agent> on the gateway
	MCPHub  bool   `json:"mcpHub"`
	HubPath string `json:"hubPath"`
	// SkillRouter: the agents are given magpie's one skill, magpie-skills,
	// listing their skills in place of a link of each (skill_router.go)
	SkillRouter bool `json:"skillRouter"`
	// HubNote is what the router says of itself to the agents (settings'
	// RouterNote), and HubName what it says with none: this computer's
	// name, to an agent on another computer (gateway.hubWhere)
	HubNote string `json:"hubNote"`
	HubName string `json:"hubName"`
	// HubAllTools: every agent is given the router's tools whole, none on
	// demand (settings' RouterAllTools, gateway.hubOnDemand)
	HubAllTools bool `json:"hubAllTools"`
	// CheckDue is set when the skills from GitHub weren't checked for
	// updates since magpie started, or not for a while: the page checks
	// them as it shows them (#1449)
	CheckDue bool `json:"checkDue,omitempty"`
	// Partial is a glance (Glance): FoundSkills and each skill's Behind
	// aren't read yet
	Partial bool `json:"partial,omitempty"`
}

// Read is the whole page: the library, and what's found in the agents.
// problems are those of the last change, for the page to keep showing.
func Read(problems []Problem) (*View, error) { return readView(problems, false) }

// Glance is the page but for what takes reading the agents' skill folders
// through: the skills the agents have of their own, and which copies are
// out of date (Partial). The page shows it at once, and Read's after it
// (0day404, #541): on a machine with many big skills in many agents, that
// part can take minutes the first time.
func Glance(problems []Problem) (*View, error) { return readView(problems, true) }

func readView(problems []Problem, glance bool) (*View, error) {
	mu.Lock()
	locked := true
	forget := func() {}
	defer func() {
		if locked {
			forget()
			mu.Unlock()
		}
	}()
	l, err := load()
	if err != nil {
		return nil, err
	}
	// each folder on the way to a path is looked at once while the lock
	// is held (linkmemo.go, #1771)
	forget = readParts()
	// a skill whose app moved it to its new version's folder is linked
	// there (skill_moved.go); what was kept of the links is let go then,
	// for the rest of the read to see the new one
	if l.followMovedSkills() {
		forget()
		forget = readParts()
		if err := l.save(); err != nil {
			return nil, err
		}
	}
	// a plugin's tools, a server of their own (plugin_tools.go), and a
	// Remote magpie's router (bridge_mcp.go): saved, and given to the
	// agents by a sync of their own
	if plugins, bridges := l.adoptPlugins(), l.adoptBridges(); plugins || bridges {
		if err := l.save(); err != nil {
			return nil, err
		}
		go func() {
			if _, err := Sync(); err != nil {
				log.Printf("library: syncing after plugin tools: %v", err)
			}
		}()
	}
	// the agents are found once for the whole read: finding them looks
	// through every agent's folders, in WSL too (#1624)
	targets, asleep := targetsAsleep(false)
	v := &View{Agents: []AgentView{}, Servers: []ServerView{}, Skills: []SkillView{}, FoundSkills: []FoundSkill{}, Instructions: l.instructionsView(targets, asleep), Dir: Dir(), Backups: BackupDir(), CopySkills: l.CopySkills, SharedSkills: l.SharedSkills, SharedDir: sharedSkillsDir(), MCPHub: l.MCPHub, SkillRouter: l.SkillRouter, HubPath: gateway.HubMCPPath, HubNote: settings.Shared().RouterNote, HubName: gateway.HostName(), HubAllTools: settings.Shared().RouterAllTools, Partial: glance}
	for _, t := range targets {
		av := AgentView{ID: t.Agent.ID, Name: t.Agent.Name, Icon: t.Agent.Icon, Instructions: t.Instructions, Skills: t.Skills,
			SkillsAlso: t.SkillsAlso, Note: t.Note, MCPVia: t.MCPVia, ProjectSkills: ProjectSkillsDir(t.Agent.ID), ProjectMCP: ProjectMCPFile(t.Agent.ID),
			ProjectOff: slices.Contains(projectOffAgents, t.Agent.ID), ProjectSkillOff: slices.Contains(projectSkillOffAgents, t.Agent.ID),
			ProjectNoSSE: ProjectNoSSE(t.Agent.ID)}
		if t.Skills != "" {
			_, av.HowOwn = l.SkillHow[t.Agent.ID]
			av.How, av.MustCopy = l.howOf(t.Agent.ID), t.Copy || t.Desktop != nil
			if av.MustCopy {
				av.How = HowCopy
			}
			if l.SkillRouter {
				av.SkillRouter = "on"
				if why := l.routerWhy(t, av.How == HowCopy); why != "" {
					av.SkillRouter = why
				}
			}
		}
		if t.MCP != nil {
			av.MCP = t.MCP.Path
			av.NoSSE = t.MCP.supports(&Server{Transport: "sse"}) != nil
			av.NoRemote = t.MCP.supports(&Server{Transport: "http"}) != nil
			av.NoEnvRefs = t.MCP.refsOf() == envSyntax{}
			av.ToolsOff = toolsOffOf(t) != toolsNone
			av.Startup = t.MCP.Format == fmtCodex
			if l.MCPHub {
				a := l.Applied[t.Agent.ID]
				if a == nil {
					a = &Applied{}
				}
				entries, _ := t.MCP.entries()
				if hs, why := l.hubFor(t, a, entries); hs != nil {
					av.Hub = "on"
				} else {
					av.Hub = why
				}
			}
		}
		v.Agents = append(v.Agents, av)
	}
	// an agent gone from here that still has what magpie gave it: listed on
	// the rows of what it has, for that to be taken out (held)
	gone := l.held(targets)
	for _, t := range gone {
		av := AgentView{ID: t.Agent.ID, Name: t.Agent.Name, Icon: t.Agent.Icon, Skills: t.Skills, Gone: true}
		if t.MCP != nil {
			av.MCP = t.MCP.Path
		}
		v.Agents = append(v.Agents, av)
	}
	// a stopped WSL distro's agents, by what magpie last knew of them:
	// nothing of theirs is opened, which would start the distro (#3896)
	for _, a := range asleep {
		if av, ok := l.asleepView(a); ok {
			v.Agents = append(v.Agents, av)
		}
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
	v.Hooks, v.HookAgents = l.hooksView(append(slices.Clone(targets), gone...), asleep, of)
	v.Subagents, v.SubagentAgents = l.subagentsView(targets, asleep, of)
	v.Plugins = l.pluginsView()
	tr := &tracer{}
	for _, s := range l.Skills {
		sv := SkillView{Name: s.Name, Bundle: s.Bundle, Agents: append([]string{}, s.Agents...), Icon: skillIcon(s), Problems: of("skill:" + s.Name), Repo: tr.repoOf(s), OffHere: s.OffHere, Note: s.Note, Router: s.Router}
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
		// kept in ~/.agents/skills by the user, or put there by the
		// setting: every agent reading that folder has it
		sv.Shared = sharedEntry(s.Name)
		if sv.Shared != "" {
			sv.SharedElsewhere = !within(realDir(skillDir(s.Name)), realDir(sharedSkillsDir()))
		}
		if sv.Shared != "" || l.inSharedHere(s) {
			for _, t := range targets {
				if t.Skills != "" && (t.readsSharedDir() || realDir(t.Skills) == realDir(sharedSkillsDir())) {
					sv.Always = append(sv.Always, t.Agent.ID)
				}
			}
		}
		if o, ok := ccSwitchOrigin(s); ok {
			o.Path = ""
			sv.Origin = o.String()
		} else if o, ok := lockOrigin(s); ok {
			// one the skills CLI installed: the row says where its lock
			// says it came from (#4645)
			sv.Origin = o.String()
		}
		if m, ok := readMeta(skillDir(s.Name)); ok {
			sv.Description, sv.Manual = m.Description, m.Manual
		} else {
			sv.Missing = true
		}
		sv.Check = lastCheck(s.Name)
		sv.Edited, _ = l.editState(s, targets)
		sv.Gone = s.Gone
		v.Skills = append(v.Skills, sv)
	}
	v.FoundServers = foundServersOf(l, targets)
	for i := range v.FoundServers {
		v.FoundServers[i].Icon = serverIcon(l, v.FoundServers[i].Server)
	}
	v.NewSkills = newSkills(l)
	v.CheckFolders = l.addedFromFolders()
	v.Projects = projectViews(l, problems)
	v.SkillGroups = l.skillGroups()
	v.CheckDue = checkDue(l)
	if glance {
		return v, nil
	}
	// the agents' skill folders are read through without the lock, so a
	// change made meanwhile doesn't wait on them: what they show is read
	// from the library as it was loaded
	forget()
	mu.Unlock()
	locked = false
	behind := l.behind(targets)
	for i := range v.Skills {
		v.Skills[i].Behind = behind[v.Skills[i].Name]
	}
	v.FoundSkills = foundSkills(l)
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
			// the tools turned off stay, the editor not sending them
			if s.DisabledTools == nil {
				s.DisabledTools = l.MCP[i].DisabledTools
			}
			// a plugin's server stays the plugin's, and on this computer
			if p := l.MCP[i].Plugin; p != "" && s.Plugin == "" {
				s.Plugin, s.Local = p, true
			}
			// still off, it still knows whom to give it back to
			if len(s.Agents) == 0 && !s.Hidden {
				s.Hidden, s.Paused = l.MCP[i].Hidden, l.MCP[i].Paused
			}
			// a Remote magpie's router stays its own while it is at the
			// provider's address and key (bridge_mcp.go)
			keptBridge(l.MCP[i], &s)
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
		var r reach
		if on {
			r = reachNow()
		}
		for _, s := range l.MCP {
			if names != nil && !slices.Contains(names, s.Name) {
				continue
			}
			// an agent's every server leaves out a plugin's hidden from
			// every agent on the Plugins page; named, it is shown
			if names == nil && on && s.Hidden {
				continue
			}
			kept := slices.DeleteFunc(slices.Clone(s.Agents), func(a string) bool { return slices.Contains(agents, a) })
			for _, a := range agents {
				if on && r.takes(a, s) {
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

// reach is which agents a server can be given: those whose MCP file
// supports it, and a stopped distro's by what magpie last knew of them.
type reach struct {
	mcp    map[string]*mcpFile
	shapes map[string]*shape
	order  []string // every agent it knows, as found
}

func reachNow() reach {
	r := reach{mcp: map[string]*mcpFile{}, shapes: map[string]*shape{}}
	ts, asleep := targetsAsleep(false)
	for _, t := range ts {
		if t.MCP != nil {
			r.mcp[t.Agent.ID] = t.MCP
			r.order = append(r.order, t.Agent.ID)
		}
	}
	for _, a := range asleep {
		if sh := shapeOf(a); sh != nil {
			r.shapes[a.ID] = sh
			r.order = append(r.order, a.ID)
		}
	}
	return r
}

func (r reach) takes(a string, s *Server) bool {
	return r.mcp[a] != nil && r.mcp[a].supports(s) == nil || r.shapes[a] != nil && r.shapes[a].reaches(s)
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
	if b := l.MCP[i].Bridge; b != "" {
		// a Remote magpie's router taken out: its editor's switch is off
		l.BridgesOn = slices.DeleteFunc(l.BridgesOn, func(id string) bool { return id == b })
	}
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

// RemoveFoundServer takes a server the agents have of their own out of
// every agent that has it as it is (#1618), without bringing it into the
// library first: its entry goes from each file the agent reads servers
// from (dsh's other profiles, Claude Desktop's Claude-3p file, Pi's other
// mcp.json too), each file copied to the backups before it is written, and
// the rest of each file — the user's other servers, their keys, comments —
// left as it is. Another server by that name (Others) is another server
// and stays. One the agent's app puts back each time it starts (Own) is
// left: taking it out wouldn't last. A file that can't be read now is
// never written: what it holds is unknown, not nothing.
func RemoveFoundServer(name string) (*Result, error) {
	return change(func(l *Library) error {
		found := foundServers(l)
		i := slices.IndexFunc(found, func(f Found) bool { return f.Server.Name == name })
		if i < 0 {
			return fmt.Errorf("no agent has a server called %s that the library hasn't", name)
		}
		f := found[i]
		if f.Own {
			return fmt.Errorf("%s is put there by the agent's app itself, each time it starts: taking it out wouldn't last", name)
		}
		b := l.kept
		if b == nil {
			b = newBackups()
			l.kept = b
		}
		for _, id := range f.Server.Agents {
			t := targetByID(id)
			if t == nil || t.MCP == nil {
				continue
			}
			if err := removeFoundEntry(t.MCP, id, name, b); err != nil {
				return fmt.Errorf("%s: %w", t.Agent.Name, err)
			}
		}
		return nil
	})
}

// removeFoundEntry takes the server by that name out of the agent's MCP
// file and the files read with it, each backed up first.
func removeFoundEntry(mf *mcpFile, id, name string, b *backups) error {
	extra := make([]*mcpFile, 0, len(mf.Extra))
	for _, p := range mf.Extra {
		extra = append(extra, &mcpFile{Path: p, Format: mf.Format})
	}
	// every file read before any is written: one that doesn't parse stops
	// it all, with nothing changed
	es, err := mf.entries()
	if err != nil {
		return err
	}
	in := es[name] != nil || mf.holds(name)
	var inExtra []*mcpFile
	for _, x := range extra {
		xes, err := x.entries()
		if err != nil {
			return err
		}
		if xes[name] != nil {
			inExtra = append(inExtra, x)
		}
	}
	if in {
		for _, p := range mf.files() {
			if err := b.keep(id, p); err != nil {
				return err
			}
		}
		if err := mf.del(name); err != nil {
			return err
		}
	}
	for _, x := range inExtra {
		if err := b.keep(id, x.Path); err != nil {
			return err
		}
		if err := x.del(name); err != nil {
			return err
		}
	}
	// read back: a server left in a file the agent reads is said, not
	// reported gone
	if have, err := mf.read(); err != nil {
		return err
	} else if have[name] != nil {
		return fmt.Errorf("%s is still in %s", name, mf.Path)
	}
	return nil
}

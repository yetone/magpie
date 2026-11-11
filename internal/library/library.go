// Package library keeps what every agent should know, once: the
// instructions each reads before a conversation, the MCP servers it can
// call and the skills it can load. magpie writes them into each agent's own
// files in that agent's own format, and takes back only what it wrote —
// whatever else is in those files stays as it was.
package library

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/capre"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/settings"
)

// Library is library.json: what magpie keeps, and which agents get it.
type Library struct {
	Instructions Instructions `json:"instructions"`
	MCP          []*Server    `json:"mcp"`
	Skills       []*Skill     `json:"skills"`
	// Projects are folders whose agents get some of the skills there
	Projects []*Project `json:"projects,omitempty"`
	// Hooks are commands the agents ticked for each run at a point of
	// their turn (hooks.go, #1790)
	Hooks []*Hook `json:"hooks,omitempty"`
	// Subagents are agents of their own the agents ticked for each can
	// hand a task to (subagents.go, #2283)
	Subagents []*Subagent `json:"subagents,omitempty"`
	// Plugins are the Claude Code plugins kept as one item each: its
	// skills, servers, hooks and subagents are the items above marked
	// with its name (plugin.go)
	Plugins []*Plugin `json:"plugins,omitempty"`
	// Applied is what magpie wrote into each agent, so that what it takes
	// away is only ever its own.
	Applied map[string]*Applied `json:"applied,omitempty"`
	// Away are other computers' servers by the name of one kept to this
	// computer here (Server.Local), as a sync last brought them: never
	// given to an agent here, and carried on as they came, so a sync from
	// here takes them from nobody (carry.go)
	Away []*Server `json:"away,omitempty"`
	// Icons are the icons of the servers added from the market, by what
	// each runs, for the page to show them by
	Icons map[string]string `json:"icons,omitempty"`
	// SkillGroups are the groups the user made of some skills on the
	// page, each shown as a group of its own (#791)
	SkillGroups []*SkillGroup `json:"skillGroups,omitempty"`
	// CopySkills gives the agents copies of their skills rather than links
	// to the library's, and SkillHow is an agent's own way over it, link or
	// copy (#896, skill_how.go)
	CopySkills bool              `json:"copySkills,omitempty"`
	SkillHow   map[string]string `json:"skillHow,omitempty"`
	// SharedSkills puts every skill on here in ~/.agents/skills too, as
	// magpie's link to the library's folder, for every agent and tool
	// that reads it (usrname1833, skills_shared.go). This computer's
	// own: Collect doesn't carry it
	SharedSkills bool `json:"sharedSkills,omitempty"`
	// MCPHub gives each agent that can reach it magpie's one MCP server,
	// which hands each call on to the library's servers ticked for it, in
	// place of their own entries (#2679, mcp_hub.go)
	MCPHub bool `json:"mcpHub,omitempty"`
	// SkillRouter gives each agent that can take it magpie's one skill,
	// magpie-skills, listing the library's skills ticked for it, in place
	// of a link of each (skill_router.go). This computer's own: Collect
	// doesn't carry it
	SkillRouter bool `json:"skillRouter,omitempty"`
	// PluginsOffered are the plugins (plugin.Name) given a server of their
	// tools, for one the user removed not to come back (plugin_tools.go)
	PluginsOffered []string `json:"pluginsOffered,omitempty"`
	// BridgesOffered are the Remote magpies (provider ids) whose MCP
	// router was given a server, for one the user removed not to come
	// back (bridge_mcp.go)
	BridgesOffered []string `json:"bridgesOffered,omitempty"`
	// BridgesOn are the Remote magpies (provider ids) the user asked to
	// give their router a server, in the editor's switch (bridge_mcp.go)
	BridgesOn []string `json:"bridgesOn,omitempty"`
	// BorrowOn are the Remote magpies (provider ids) whose Library's
	// skills and prompts this computer uses, and Borrowed what it took
	// from each (borrow.go). This computer's own: Collect doesn't carry
	// either.
	BorrowOn []string             `json:"borrowOn,omitempty"`
	Borrowed map[string]*Borrowed `json:"borrowed,omitempty"`
	// SeenSkills are, by repository and ref ("owner/repo@ref", or a git
	// address and ref), or by a folder the user added ("folder:<path>"),
	// the folders of skills there the page has offered: picked or not at
	// install, or set aside after a check. A check offers only the others
	// as new (skillnew.go).
	SeenSkills map[string][]string `json:"seenSkills,omitempty"`
	// kept is where a change put what it kept aside before the sync, for
	// the sync to keep the agents' files beside it
	kept *backups
	// hashes are the library's skills' hashes as this change read them
	// (libHash); one taken from an agent's edit is read again
	hashes map[string]string
	// untouched are the agents' copies takeEdits found unchanged since
	// magpie made them, for the sync not to look through them again
	untouched map[string]bool
	// blockedSkills are copies takeEdits couldn't scan, kept in place for
	// this sync rather than replaced or removed. Keys are physical paths.
	blockedSkills map[string]bool
	// stored are the skills this sync made sure of in a WSL distro's
	// store, by path (storeSkill), with what went wrong
	stored map[string]error
}

// Instructions are one shared text, and for each agent whether it gets it
// and what it gets besides (kept in files beside library.json). The shared
// text is one of several sets kept to switch between (#106), the one on.
type Instructions struct {
	Agents []string `json:"agents"` // the agents the shared text is written to
	// Sets are the named texts to pick the shared one from; with none kept
	// there is the one magpie always had, "default"
	Sets   []InstrSet `json:"sets,omitempty"`
	Active string     `json:"active,omitempty"` // the set agents get; "" is "default"
	// NoMemory leaves memory's digest (memory.Digest) out of what the
	// agents are written; by default it follows the shared text
	NoMemory bool `json:"noMemory,omitempty"`
}

// InstrSet is one set of shared instructions: its text is in its own file.
type InstrSet struct {
	ID   string `json:"id"`
	Name string `json:"name"` // "" for the first, which the page calls Default
	// File is a file of the user's own the set's text is kept in instead,
	// read each time and written when the set is saved: one shared file
	// such as ~/.agents/AGENTS.md, kept by hand or by another tool.
	File string `json:"file,omitempty"`
}

// Applied is what magpie last wrote into one agent.
type Applied struct {
	Instructions bool     `json:"instructions,omitempty"`
	InstrHash    string   `json:"instrHash,omitempty"` // of the part last written
	MCP          []string `json:"mcp,omitempty"`
	Skills       []string `json:"skills,omitempty"`
	// ToolsOff are, for each server, the tools magpie put in the list its
	// entry has of those the agent leaves out; ToolRules the rules beside
	// the servers magpie added to leave tools out (Claude Code's deny
	// rules, OpenCode's tools keys). Neither holds one the user had.
	ToolsOff  map[string][]string `json:"toolsOff,omitempty"`
	ToolRules []string            `json:"toolRules,omitempty"`
	// Hooks are the entries magpie wrote into the agent's hooks, each
	// exactly as written: one changed since is the user's (hooks.go)
	Hooks []HookEntry `json:"hooks,omitempty"`
	// Subagents are the files magpie wrote for the subagents, each with
	// the hash of what it wrote: one changed since is the user's
	Subagents []SubagentFile `json:"subagents,omitempty"`
	// Startup is, for each server, the startup timeout magpie wrote in its
	// entry (Codex's startup_timeout_sec): one there other than this is
	// the user's
	Startup map[string]float64 `json:"startup,omitempty"`
	// Picked are the skills magpie picked in the agent's own list of
	// those it uses (LiveAgent's skills-settings.json, liveagent.go): one
	// the user unpicked since stays unpicked
	Picked []string `json:"picked,omitempty"`
	// Router: magpie put the skill router's entry, magpie-skills, in the
	// agent's skills folder
	Router bool `json:"router,omitempty"`
	// RouterRules are the rules magpie put in Claude Code's
	// permissions.allow so it reads a listed skill without asking.
	RouterRules []string `json:"routerRules,omitempty"`
}

// empty is whether a holds nothing magpie wrote: a sync that only looked
// at an agent — its hooks, its subagents, its MCP file, with nothing of
// the library's to give it — makes no record, and a save drops the entry
// it looked at the agent through (#4247).
func (a *Applied) empty() bool {
	return !a.Instructions && a.InstrHash == "" && len(a.MCP) == 0 && len(a.Skills) == 0 &&
		len(a.ToolsOff) == 0 && len(a.ToolRules) == 0 && len(a.Hooks) == 0 &&
		len(a.Subagents) == 0 && len(a.Startup) == 0 && len(a.Picked) == 0 && !a.Router && len(a.RouterRules) == 0
}

var mu lock // one change at a time, from the page or the CLI's process

// lock is a mutex that counts the times it was taken, for Sync to tell
// whether anything held it between two of its steps.
type lock struct {
	sync.Mutex
	held uint64
}

func (m *lock) Lock() {
	m.Mutex.Lock()
	m.held++
}

// Dir is where the library's own files are: the instructions and the skills.
func Dir() string { return filepath.Join(settings.Dir(), "library") }

func path() string { return filepath.Join(settings.Dir(), "library.json") }

func load() (*Library, error) {
	l := &Library{}
	b, err := edit.Read(path())
	if err != nil {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, l); err != nil {
			return nil, fmt.Errorf("%s: %w", path(), err)
		}
	}
	if l.Applied == nil {
		l.Applied = map[string]*Applied{}
	}
	l.heal() // magpie's own servers run this computer's magpie (self_here.go)
	return l, nil
}

func (l *Library) save() error {
	sort.Slice(l.MCP, func(i, j int) bool { return l.MCP[i].Name < l.MCP[j].Name })
	// a paused server given an agent, however, is on again
	for _, s := range l.MCP {
		if len(s.Agents) > 0 {
			s.Hidden, s.Paused = false, nil
		}
	}
	sort.Slice(l.Skills, func(i, j int) bool { return l.Skills[i].Name < l.Skills[j].Name })
	sort.Slice(l.Projects, func(i, j int) bool { return l.Projects[i].Dir < l.Projects[j].Dir })
	sort.Slice(l.Hooks, func(i, j int) bool { return l.Hooks[i].Name < l.Hooks[j].Name })
	for _, h := range l.Hooks {
		h.Agents = orNone(h.Agents)
	}
	sort.Slice(l.Subagents, func(i, j int) bool { return l.Subagents[i].Name < l.Subagents[j].Name })
	for _, s := range l.Subagents {
		s.Agents = orNone(s.Agents)
	}
	// an entry with nothing in it is no record of what magpie wrote, and
	// is not kept: a sync that only looked at an agent used to leave one
	// behind, one per agent, and the write its save brought into a file
	// otherwise unchanged was caught red by a test of the CLI's (#4247)
	for id, a := range l.Applied {
		if a.empty() {
			delete(l.Applied, id)
			continue
		}
		sort.Strings(a.MCP)
		sort.Strings(a.Skills)
	}
	// a skill or server on no agent is on none, [] not null: a list sorted
	// with slices.Sorted, or cloned, from an empty one is nil (#1217)
	for _, s := range l.Skills {
		s.Agents = orNone(s.Agents)
	}
	for _, s := range l.MCP {
		s.Agents = orNone(s.Agents)
	}
	sort.Slice(l.Away, func(i, j int) bool { return l.Away[i].Name < l.Away[j].Name })
	l.Instructions.Agents = orNone(l.Instructions.Agents)
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(path(), append(b, '\n'))
}

func (l *Library) applied(agent string) *Applied {
	a := l.Applied[agent]
	if a == nil {
		a = &Applied{}
		l.Applied[agent] = a
	}
	return a
}

func (l *Library) server(name string) *Server {
	for _, s := range l.MCP {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func (l *Library) skill(name string) *Skill {
	for _, s := range l.Skills {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// A name is what a server or a skill is called in every agent's file, and
// a skill's folder: it has to be a bare key in TOML and a plain file name.
var nameRe = capre.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`, 64)

func checkName(kind, name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%s name %q: use letters, digits, - and _ only", kind, name)
	}
	return nil
}

// An agent's id is a name, or one in a WSL distro's
// (codex@wsl:Ubuntu-24.04): distroRe is the distro's name.
var distroRe = capre.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`, 64)

func checkAgent(id string) error {
	name, distro, wsl := strings.Cut(id, "@wsl:")
	if !nameRe.MatchString(name) || wsl && !distroRe.MatchString(distro) {
		return fmt.Errorf("agent %q: not an agent's id", id)
	}
	return nil
}

// fileName is an agent's id as the name of a file or folder of its:
// Windows has no colon in one, so a WSL agent's is codex@wsl.Ubuntu.
func fileName(id string) string { return strings.Replace(id, "@wsl:", "@wsl.", 1) }

// agentOfFile is the agent's id a fileName is of.
func agentOfFile(name string) string { return strings.Replace(name, "@wsl.", "@wsl:", 1) }

// set turns id on or off in a list of agent ids, which stays sorted.
func set(list []string, id string, on bool) []string {
	i := slices.Index(list, id)
	switch {
	case on && i < 0:
		list = append(list, id)
		sort.Strings(list)
	case !on && i >= 0:
		list = slices.Delete(list, i, i+1)
	}
	return list
}

// change runs f on the library under the lock, saves it, and writes what
// changed into the agents. The processes of a server taken out are ended
// after, with the lock let go (mcp_reap.go).
func change(f func(l *Library) error) (*Result, error) {
	res, err := changeLocked(f)
	if res != nil && len(res.dropped) > 0 {
		ending(res.dropped)
	}
	return res, err
}

func changeLocked(f func(l *Library) error) (*Result, error) {
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return nil, err
	}
	if err := f(l); err != nil {
		return nil, err
	}
	l.settleAway()
	l.heal() // a server f brought in, from another computer too
	if err := l.save(); err != nil {
		return nil, err
	}
	res := l.sync()
	return res, l.save()
}

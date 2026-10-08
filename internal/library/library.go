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
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

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
	// Applied is what magpie wrote into each agent, so that what it takes
	// away is only ever its own.
	Applied map[string]*Applied `json:"applied,omitempty"`
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
	// SeenSkills are, by repository and ref ("owner/repo@ref"), the
	// folders of skills there the page has offered: picked or not at
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
}

// InstrSet is one set of shared instructions: its text is in its own file.
type InstrSet struct {
	ID   string `json:"id"`
	Name string `json:"name"` // "" for the first, which the page calls Default
}

// Applied is what magpie last wrote into one agent.
type Applied struct {
	Instructions bool     `json:"instructions,omitempty"`
	InstrHash    string   `json:"instrHash,omitempty"` // of the part last written
	MCP          []string `json:"mcp,omitempty"`
	Skills       []string `json:"skills,omitempty"`
}

var mu sync.Mutex // one change at a time, from the page or the CLI's process

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
	return l, nil
}

func (l *Library) save() error {
	sort.Slice(l.MCP, func(i, j int) bool { return l.MCP[i].Name < l.MCP[j].Name })
	sort.Slice(l.Skills, func(i, j int) bool { return l.Skills[i].Name < l.Skills[j].Name })
	sort.Slice(l.Projects, func(i, j int) bool { return l.Projects[i].Dir < l.Projects[j].Dir })
	for _, a := range l.Applied {
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
var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func checkName(kind, name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%s name %q: use letters, digits, - and _ only", kind, name)
	}
	return nil
}

// agentRe is an agent's id: a name, or one in a WSL distro's
// (codex@wsl:Ubuntu-24.04).
var agentRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}(@wsl:[A-Za-z0-9][A-Za-z0-9._-]{0,63})?$`)

func checkAgent(id string) error {
	if !agentRe.MatchString(id) {
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
// changed into the agents.
func change(f func(l *Library) error) (*Result, error) {
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return nil, err
	}
	if err := f(l); err != nil {
		return nil, err
	}
	if err := l.save(); err != nil {
		return nil, err
	}
	res := l.sync()
	return res, l.save()
}

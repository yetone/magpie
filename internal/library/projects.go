package library

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// Project is a folder of the user's whose agents get some of the library's
// skills there, as the project's own: each is placed in the folder the
// agent reads a project's skills from, and kept out of git.
type Project struct {
	Dir string `json:"dir"`
	// Copy puts copies of the skills there, not links to the library's
	// (for Windows, or a folder synced or shared where a link won't do);
	// a copy is made again when the library's skill changes
	Copy bool `json:"copy,omitempty"`
	// Skills are the library's skills the project has, and for which agents
	Skills map[string][]string `json:"skills,omitempty"`
	// Placed are the entries magpie put in the folder (.claude/skills/pdf),
	// the only ones it ever takes away
	Placed []string `json:"placed,omitempty"`
	// Made are the folders magpie made to place them in, taken away again
	// once they're empty; Gitignore is whether it made the .gitignore
	Made      []string `json:"made,omitempty"`
	Gitignore bool     `json:"gitignore,omitempty"`
	// Servers are the library's MCP servers the project has, and for which
	// agents (project_mcp.go)
	Servers map[string][]string `json:"servers,omitempty"`
	// Wrote are, by the project's file (.mcp.json), the servers magpie
	// wrote in it, the only entries it ever takes out
	Wrote map[string][]string `json:"wrote,omitempty"`
	// Startup are, by the project's file, the startup timeout magpie
	// wrote in each server's entry there (Codex's startup_timeout_sec):
	// one other than this is the user's
	Startup map[string]map[string]float64 `json:"startup,omitempty"`
	// MadeFiles are the files magpie made to write servers in, listed in
	// the .gitignore and taken away again once nothing's left in them
	MadeFiles []string `json:"madeFiles,omitempty"`
	// ServersOff and SkillsOff are global servers and skills (given to
	// the agents themselves) the project turns off, and for which agents
	// (project_scope.go)
	ServersOff map[string][]string `json:"serversOff,omitempty"`
	SkillsOff  map[string][]string `json:"skillsOff,omitempty"`
	// Claude is what magpie wrote in Claude Code's own record of the
	// project (~/.claude.json): the router's entry, servers turned off
	Claude *claudeLocal `json:"claude,omitempty"`
	// CodexOff are the servers magpie turned off in the project's
	// .codex/config.toml, Overrides the skills it turned off in
	// .claude/settings.local.json
	CodexOff  []string `json:"codexOff,omitempty"`
	Overrides []string `json:"overrides,omitempty"`
	// Trees are the project's folder in each other worktree of its git
	// repository, given the project's set, with what magpie put in each
	Trees []*Project `json:"trees,omitempty"`
}

// projectSkillsDirs is where, in a project, each agent reads skills of its
// own from — only an agent that does: Claude Code its .claude/skills, and
// those that read the shared .agents/skills (Codex from where it runs to
// the repository's root, Gemini CLI, OpenCode, Pi, Cursor, Copilot, Antigravity,
// Kimi Code, Goose and Grok Build, Droid — docs.factory.com/cli/configuration/
// skills — and Cline's CLI, which reads .clinerules, .cline and .agents
// skills folders in the workspace, @cline/shared 3.0.70) that one, so that
// one folder serves them all. Qoder (and Qoder CN) read .qoder/skills
// (getProjectSkillsDir, @qoder-ai/qodercli 1.1.69, whose .agents/skills
// a setting can turn off), CodeBuddy Code .codebuddy/skills
// (getProjectSkillsDir, @tencent-ai/codebuddy-code 2.164.0) and Command
// Code .commandcode/skills (commandcode.ai/docs/skills). DeepSeek Harness
// reads <projectRoot>/.dsh/skills and <projectRoot>/.agents/skills, the
// project root being the nearest folder up from where it runs with a .git
// in it, else that folder itself (the provider's roots and findProjectRoot,
// @deepseek-ai/dsh-skill-filesystem 0.2.1-rc.5, mounted by dsh-base's
// cordis.patch.yml in every profile; the same since 0.0.1-rc.3), so it is
// given the shared one. A project folder inside a repository but not at
// its root isn't read by dsh: run there, it looks in the repository's
// root (#5172). MiMo Code, Crush,
// ZCode and oh-my-pi say of no project folder magpie can rely on.
var projectSkillsDirs = map[string]string{
	"claude":      ".claude/skills",
	"codex":       ".agents/skills",
	"gemini":      ".agents/skills",
	"opencode":    ".agents/skills",
	"pi":          ".agents/skills",
	"cursor":      ".agents/skills",
	"copilot":     ".agents/skills",
	"agy":         ".agents/skills",
	"kimi":        ".agents/skills",
	"goose":       ".agents/skills",
	"grok":        ".agents/skills",
	"droid":       ".agents/skills",
	"cline":       ".agents/skills",
	"dsh":         ".agents/skills",
	"qoder":       ".qoder/skills",
	"qoder-cn":    ".qoder/skills",
	"codebuddy":   ".codebuddy/skills",
	"commandcode": ".commandcode/skills",
}

// ProjectSkillsDir is the folder, in a project, an agent reads its skills
// from: "" for one magpie can't give a project's skills to.
func ProjectSkillsDir(agent string) string { return projectSkillsDirs[agent] }

func (l *Library) project(dir string) *Project {
	for _, p := range l.Projects {
		if p.Dir == dir {
			return p
		}
	}
	return nil
}

// The .gitignore lines magpie keeps between these two, and only there.
const (
	ignoreBegin = "# >>> magpie: library skills placed here (magpie adds and removes these lines) >>>"
	ignoreEnd   = "# <<< magpie <<<"
)

// syncProjects places the library's skills in every project as it says.
func (l *Library) syncProjects(res *Result) {
	for _, p := range l.Projects {
		l.syncProject(p, res)
	}
}

func (l *Library) syncProject(p *Project, res *Result) {
	if fi, err := os.Stat(p.Dir); err != nil || !fi.IsDir() {
		if len(p.Skills) > 0 || len(p.Servers) > 0 || len(p.ServersOff) > 0 || len(p.SkillsOff) > 0 || p.holds() {
			res.Problems = append(res.Problems, Problem{Agent: p.Dir, What: "project:", Error: "the folder is gone"})
		}
		return
	}
	hubs := l.projectHubs(p)
	l.syncFolder(p, res, hubs)
	l.syncClaudeLocal(p, hubs["claude"], func(what string, err error) {
		res.Problems = append(res.Problems, Problem{Agent: p.Dir, What: "project:" + what, Error: err.Error()})
	})
	l.syncTrees(p, res, hubs)
	if err := p.keepOutOfGit(); err != nil {
		res.Problems = append(res.Problems, Problem{Agent: p.Dir, What: "project:", Error: "keeping it out of git: " + err.Error()})
	}
}

// syncFolder places the project's skills and writes its servers, and what
// it turns off, in one folder: the project's, or its folder in another
// worktree (syncTrees), whose agents are given the same hub entries.
func (l *Library) syncFolder(p *Project, res *Result, hubs map[string]*Server) {
	fail := func(name string, err error) {
		res.Problems = append(res.Problems, Problem{Agent: p.Dir, What: "project:" + name, Error: err.Error()})
	}
	want := map[string]string{} // entry → the skill
	for name, agents := range p.Skills {
		if l.skill(name) == nil {
			continue
		}
		for _, id := range agents {
			if d := ProjectSkillsDir(id); d != "" {
				want[d+"/"+name] = name
			}
		}
	}
	var placed []string
	for _, e := range p.Placed {
		if _, ok := want[e]; ok {
			continue
		}
		name, abs := filepath.Base(filepath.FromSlash(e)), filepath.Join(p.Dir, filepath.FromSlash(e))
		if !ours(abs, name) {
			continue // gone, or the user's own by now: forgotten, left as it is
		}
		if err := unlink(abs); err != nil {
			fail(name, err)
			placed = append(placed, e)
		}
	}
	entries := slices.Sorted(func(yield func(string) bool) {
		for e := range want {
			if !yield(e) {
				return
			}
		}
	})
	for _, e := range entries {
		name := want[e]
		switch err := p.place(e, name); {
		case err == nil:
			placed = append(placed, e)
		case errors.Is(err, errForeign):
			fail(name, fmt.Errorf("%s is in the project already and isn't magpie's: it's left as it is", e))
		default:
			fail(name, err)
			if ours(filepath.Join(p.Dir, filepath.FromSlash(e)), name) {
				placed = append(placed, e)
			}
		}
	}
	sort.Strings(placed)
	p.Placed = placed
	l.syncProjectMCP(p, hubs, func(what string, err error) {
		res.Problems = append(res.Problems, Problem{Agent: p.Dir, What: "project:" + what, Error: err.Error()})
	})
	l.syncSkillsOff(p, fail)
	p.tidy()
}

// keepOutOfGit keeps what magpie placed in the project, and in its folder
// in each other worktree, out of git: in the repository's info/exclude,
// never in a file of the project's (project_git.go). The lines an older
// magpie wrote in the project's .gitignore come out of it, and a
// .gitignore it made goes; a folder outside git gets nothing.
func (p *Project) keepOutOfGit() error {
	if err := p.writeIgnore(); err != nil {
		return fmt.Errorf(".gitignore: %w", err)
	}
	g, ok := gitOf(p.Dir)
	if !ok {
		return nil
	}
	entries := slices.Concat(p.Placed, p.MadeFiles)
	for _, t := range p.Trees {
		entries = append(entries, t.Placed...)
		entries = append(entries, t.MadeFiles...)
	}
	return p.writeExclude(g, entries)
}

var errForeign = errors.New("not magpie's")

// place puts a skill at e in the project: a link to the library's, or a
// copy made again whenever the library's differs from it.
func (p *Project) place(e, name string) error {
	abs := filepath.Join(p.Dir, filepath.FromSlash(e))
	if fi, err := os.Lstat(abs); err == nil {
		if !ours(abs, name) {
			return errForeign
		}
		link := linkEntry(fi)
		// a copy stands for a link on Windows without the right to make one
		if !p.Copy && link {
			return nil
		}
		if !link {
			current, err := copyIsFresh(abs, hashDir(realDir(skillDir(name))), false)
			if err != nil {
				return err
			}
			if (p.Copy || runtime.GOOS == "windows") && current {
				return nil
			}
		}
	}
	if err := p.mkdirs(filepath.Dir(abs)); err != nil {
		return err
	}
	if !p.Copy {
		if err := unlink(abs); err != nil {
			return err
		}
		return link(abs, name)
	}
	return copyIn(abs, name)
}

// mkdirs makes dir and the folders above it the project hasn't, noting
// which magpie made.
func (p *Project) mkdirs(dir string) error {
	var made []string
	for d := dir; d != p.Dir && strings.HasPrefix(d, p.Dir+string(filepath.Separator)); d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil {
			break
		}
		made = append(made, d)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, d := range made {
		rel, _ := filepath.Rel(p.Dir, d)
		if rel = filepath.ToSlash(rel); !slices.Contains(p.Made, rel) {
			p.Made = append(p.Made, rel)
		}
	}
	return nil
}

// tidy takes away the folders magpie made that are empty now, the deepest
// first; one with anything in it stays.
func (p *Project) tidy() {
	sort.Slice(p.Made, func(i, j int) bool { return len(p.Made[i]) > len(p.Made[j]) })
	var kept []string
	for _, rel := range p.Made {
		d := filepath.Join(p.Dir, filepath.FromSlash(rel))
		if es, err := os.ReadDir(d); err == nil && len(es) == 0 {
			if os.Remove(d) == nil {
				continue
			}
		} else if os.IsNotExist(err) {
			continue
		}
		kept = append(kept, rel)
	}
	sort.Strings(kept)
	p.Made = kept
}

// writeIgnore takes the lines an older magpie kept in the project's
// .gitignore, between its two lines, out of it; the rest of the file is
// the user's and stays. A .gitignore magpie made is taken away once
// nothing's left in it.
func (p *Project) writeIgnore() error {
	f := filepath.Join(p.Dir, ".gitignore")
	b, err := os.ReadFile(f)
	if os.IsNotExist(err) {
		p.Gitignore = false
		return nil
	}
	if err != nil {
		return err
	}
	nl := "\n"
	if strings.Contains(string(b), "\r\n") {
		nl = "\r\n"
	}
	out := strings.ReplaceAll(stripIgnore(strings.ReplaceAll(string(b), "\r\n", "\n")), "\n", nl)
	if out == string(b) {
		return nil
	}
	if strings.TrimSpace(out) == "" && p.Gitignore {
		p.Gitignore = false
		return os.Remove(f)
	}
	return edit.WriteAtomic(f, []byte(out))
}

// stripIgnore is a .gitignore without magpie's lines, and the blank line
// left before them.
func stripIgnore(s string) string { return stripBlock(s, ignoreBegin) }

// ---- changing -------------------------------------------------------------

// AddProject adds a folder whose agents can be given library skills.
func AddProject(dir string) (*Result, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("choose a project's folder")
	}
	dir = filepath.Clean(expand(dir))
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("%s isn't a folder's full path", dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%s isn't a folder", dir)
	}
	if err := notGlobal(dir); err != nil {
		return nil, err
	}
	return change(func(l *Library) error {
		for _, p := range l.Projects {
			if realDir(p.Dir) == realDir(dir) {
				return fmt.Errorf("%s is a project already", p.Dir)
			}
		}
		l.Projects = append(l.Projects, &Project{Dir: dir})
		return nil
	})
}

// notGlobal refuses a folder whose skills folders are an agent's own for
// every project: the home folder, where .claude/skills is Claude Code's.
func notGlobal(dir string) error {
	if realDir(dir) == realDir(home()) {
		return fmt.Errorf("the home folder's skills are every project's: give skills to the agents themselves instead")
	}
	for _, t := range everyTarget() {
		if t.Skills == "" {
			continue
		}
		for _, d := range projectSkillsDirs {
			if realDir(filepath.Join(dir, filepath.FromSlash(d))) == realDir(t.Skills) {
				return fmt.Errorf("%s in there is %s's own skills folder", d, t.Agent.Name)
			}
		}
	}
	for _, t := range everyTarget() {
		if t.MCP == nil {
			continue
		}
		for _, f := range projectMCPFiles {
			if realDir(filepath.Join(dir, filepath.FromSlash(f.rel))) == realDir(t.MCP.Path) {
				return fmt.Errorf("%s in there is %s's own MCP file", f.rel, t.Agent.Name)
			}
		}
	}
	return nil
}

// RemoveProject takes every skill magpie placed out of a project, and the
// project off the list; what's left, it couldn't take away and says why.
// keep takes the project off the list alone (#514: a project given a few
// skills or servers once, to keep them): everything magpie put in the
// folder — skills, servers in the agents' files, its lines in the
// .gitignore — stays as it is, the user's from then on.
func RemoveProject(dir string, keep bool) (*Result, error) {
	return change(func(l *Library) error {
		p := l.project(dir)
		if p == nil {
			return fmt.Errorf("no project %s", dir)
		}
		if keep {
			l.Projects = slices.DeleteFunc(l.Projects, func(x *Project) bool { return x == p })
			return nil
		}
		// a folder that's gone has nothing of magpie's on disk to take
		// back: the records of what it placed and wrote go with the
		// entry, as a skill's do when its folder is gone already (#3510).
		// A folder that couldn't be looked at (no permission, a drive
		// that answers with an error) isn't known to be gone: its records
		// stay, and the removal goes as before.
		if fi, err := os.Stat(p.Dir); errors.Is(err, os.ErrNotExist) || err == nil && !fi.IsDir() {
			// Claude Code's record of it isn't in the folder
			p.ServersOff = nil
			l.syncClaudeLocal(p, nil, func(string, error) {})
			l.Projects = slices.DeleteFunc(l.Projects, func(x *Project) bool { return x == p })
			return nil
		}
		p.Skills, p.Servers, p.ServersOff, p.SkillsOff = nil, nil, nil, nil
		l.syncProject(p, &Result{})
		if !p.holds() && p.Claude == nil && !slices.ContainsFunc(p.Trees, (*Project).holds) {
			l.Projects = slices.DeleteFunc(l.Projects, func(x *Project) bool { return x == p })
		}
		return nil
	})
}

// ProjectSkill sets which agents get a library skill in a project; none
// takes it out of the project.
func ProjectSkill(dir, name string, agents []string) (*Result, error) {
	return ProjectSkills(dir, []string{name}, agents)
}

// ProjectSkills sets which agents get each of some library skills in a
// project, in one write: the skills a project's card picks from its menu
// (POM_JUMPING on X). The project's other skills stay as they are; none
// takes these out of it.
func ProjectSkills(dir string, names, agents []string) (*Result, error) {
	return change(func(l *Library) error {
		p := l.project(dir)
		if p == nil {
			return fmt.Errorf("no project %s", dir)
		}
		if len(names) == 0 {
			return fmt.Errorf("no skills given")
		}
		for _, name := range names {
			if l.skill(name) == nil {
				return fmt.Errorf("no skill called %s", name)
			}
		}
		var ids []string
		for _, id := range agents {
			if ProjectSkillsDir(id) == "" {
				return fmt.Errorf("magpie knows of no folder %s reads a project's skills from", id)
			}
			ids = set(ids, id, true)
		}
		for _, name := range names {
			if len(ids) == 0 {
				delete(p.Skills, name)
				continue
			}
			if p.Skills == nil {
				p.Skills = map[string][]string{}
			}
			p.Skills[name] = slices.Clone(ids)
		}
		return nil
	})
}

// ProjectCopy sets whether a project gets copies of its skills rather than
// links; those there are made again the other way.
func ProjectCopy(dir string, copy bool) (*Result, error) {
	return change(func(l *Library) error {
		p := l.project(dir)
		if p == nil {
			return fmt.Errorf("no project %s", dir)
		}
		p.Copy = copy
		return nil
	})
}

// ProjectView is a project as the page shows it.
type ProjectView struct {
	Dir     string              `json:"dir"`
	Name    string              `json:"name"`
	Copy    bool                `json:"copy"`
	Skills  map[string][]string `json:"skills"`
	Placed  []string            `json:"placed"`
	Servers map[string][]string `json:"servers"`
	// Wrote are, by the project's file, the servers magpie wrote there
	Wrote map[string][]string `json:"wrote"`
	// ServersOff and SkillsOff are the Library's servers and skills
	// turned off in this project, each for the agents named
	ServersOff map[string][]string `json:"serversOff"`
	SkillsOff  map[string][]string `json:"skillsOff"`
	// Trees are the project's folder in the repository's other git
	// worktrees, which follow it
	Trees   []string `json:"trees,omitempty"`
	Missing bool     `json:"missing,omitempty"` // the folder is gone
	// Problems are, by skill ("" for the project's own), what couldn't be done
	Problems map[string]string `json:"problems,omitempty"`
}

func projectViews(l *Library, problems []Problem) []ProjectView {
	out := []ProjectView{}
	for _, p := range l.Projects {
		v := ProjectView{Dir: p.Dir, Name: filepath.Base(p.Dir), Copy: p.Copy, Skills: map[string][]string{}, Placed: append([]string{}, p.Placed...)}
		for k, a := range p.Skills {
			v.Skills[k] = append([]string{}, a...)
		}
		v.Servers, v.Wrote = map[string][]string{}, map[string][]string{}
		for k, a := range p.Servers {
			v.Servers[k] = append([]string{}, a...)
		}
		for k, a := range p.Wrote {
			v.Wrote[k] = append([]string{}, a...)
		}
		v.ServersOff, v.SkillsOff = map[string][]string{}, map[string][]string{}
		for k, a := range p.ServersOff {
			v.ServersOff[k] = append([]string{}, a...)
		}
		for k, a := range p.SkillsOff {
			v.SkillsOff[k] = append([]string{}, a...)
		}
		for _, t := range p.Trees {
			v.Trees = append(v.Trees, t.Dir)
		}
		if fi, err := os.Stat(p.Dir); err != nil || !fi.IsDir() {
			v.Missing = true
		}
		for _, x := range problems {
			if x.Agent == p.Dir {
				if v.Problems == nil {
					v.Problems = map[string]string{}
				}
				v.Problems[strings.TrimPrefix(x.What, "project:")] = x.Error
			}
		}
		out = append(out, v)
	}
	return out
}

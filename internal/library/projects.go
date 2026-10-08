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
	// MadeFiles are the files magpie made to write servers in, listed in
	// the .gitignore and taken away again once nothing's left in them
	MadeFiles []string `json:"madeFiles,omitempty"`
}

// projectSkillsDirs is where, in a project, each agent reads skills of its
// own from — only an agent that does: Claude Code its .claude/skills, and
// those that read the shared .agents/skills (Codex from where it runs to
// the repository's root, Gemini CLI, OpenCode, Pi, Cursor, Copilot, Antigravity,
// Kimi Code, Goose and Grok Build) that one, so that one folder serves them all. MiMo Code, Crush, ZCode,
// DeepSeek Harness and oh-my-pi say of no project folder magpie can rely on.
var projectSkillsDirs = map[string]string{
	"claude":   ".claude/skills",
	"codex":    ".agents/skills",
	"gemini":   ".agents/skills",
	"opencode": ".agents/skills",
	"pi":       ".agents/skills",
	"cursor":   ".agents/skills",
	"copilot":  ".agents/skills",
	"agy":      ".agents/skills",
	"kimi":     ".agents/skills",
	"goose":    ".agents/skills",
	"grok":     ".agents/skills",
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
	fail := func(name string, err error) {
		res.Problems = append(res.Problems, Problem{Agent: p.Dir, What: "project:" + name, Error: err.Error()})
	}
	if fi, err := os.Stat(p.Dir); err != nil || !fi.IsDir() {
		if len(p.Skills) > 0 || len(p.Placed) > 0 || len(p.Servers) > 0 || len(p.Wrote) > 0 {
			fail("", fmt.Errorf("the folder is gone"))
		}
		return
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
	l.syncProjectMCP(p, func(what string, err error) {
		res.Problems = append(res.Problems, Problem{Agent: p.Dir, What: "project:" + what, Error: err.Error()})
	})
	if err := p.writeIgnore(); err != nil {
		fail("", fmt.Errorf(".gitignore: %w", err))
	}
	p.tidy()
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
		if !p.Copy && link || !link && (p.Copy || runtime.GOOS == "windows") && fresh(abs, name) {
			return nil
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

// writeIgnore keeps the project's .gitignore listing what magpie placed,
// between its two lines; the rest of the file is the user's and stays.
// A .gitignore magpie made is taken away once nothing's left in it.
func (p *Project) writeIgnore() error {
	f := filepath.Join(p.Dir, ".gitignore")
	b, err := os.ReadFile(f)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	had := err == nil
	nl := "\n"
	if strings.Contains(string(b), "\r\n") {
		nl = "\r\n"
	}
	rest := stripIgnore(strings.ReplaceAll(string(b), "\r\n", "\n"))
	out := rest
	if ignored := slices.Concat(p.Placed, p.MadeFiles); len(ignored) > 0 {
		sort.Strings(ignored)
		if out != "" {
			out += "\n"
		}
		out += ignoreBegin + "\n"
		for _, e := range ignored {
			out += "/" + e + "\n"
		}
		out += ignoreEnd + "\n"
	}
	out = strings.ReplaceAll(out, "\n", nl)
	switch {
	case out == string(b) && had:
		return nil
	case strings.TrimSpace(out) == "":
		if had && p.Gitignore {
			p.Gitignore = false
			return os.Remove(f)
		}
		if !had {
			return nil
		}
	case !had:
		p.Gitignore = true
	}
	return edit.WriteAtomic(f, []byte(out))
}

// stripIgnore is a .gitignore without magpie's lines, and the blank line
// left before them.
func stripIgnore(s string) string {
	var out []string
	in := false
	for _, line := range strings.Split(s, "\n") {
		switch t := strings.TrimSpace(line); {
		case t == ignoreBegin:
			in = true
		case in && t == ignoreEnd:
			in = false
		case !in:
			out = append(out, line)
		}
	}
	r := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if r != "" {
		r += "\n"
	}
	return r
}

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
	for _, t := range Targets() {
		if t.Skills == "" {
			continue
		}
		for _, d := range projectSkillsDirs {
			if realDir(filepath.Join(dir, filepath.FromSlash(d))) == realDir(t.Skills) {
				return fmt.Errorf("%s in there is %s's own skills folder", d, t.Agent.Name)
			}
		}
	}
	for _, t := range Targets() {
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
		p.Skills, p.Servers = nil, nil
		l.syncProject(p, &Result{})
		if len(p.Placed) == 0 && len(p.Wrote) == 0 {
			l.Projects = slices.DeleteFunc(l.Projects, func(x *Project) bool { return x == p })
		}
		return nil
	})
}

// ProjectSkill sets which agents get a library skill in a project; none
// takes it out of the project.
func ProjectSkill(dir, name string, agents []string) (*Result, error) {
	return change(func(l *Library) error {
		p := l.project(dir)
		if p == nil {
			return fmt.Errorf("no project %s", dir)
		}
		if l.skill(name) == nil {
			return fmt.Errorf("no skill called %s", name)
		}
		var ids []string
		for _, id := range agents {
			if ProjectSkillsDir(id) == "" {
				return fmt.Errorf("magpie knows of no folder %s reads a project's skills from", id)
			}
			ids = set(ids, id, true)
		}
		if len(ids) == 0 {
			delete(p.Skills, name)
			return nil
		}
		if p.Skills == nil {
			p.Skills = map[string][]string{}
		}
		p.Skills[name] = ids
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
	Wrote   map[string][]string `json:"wrote"`
	Missing bool                `json:"missing,omitempty"` // the folder is gone
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

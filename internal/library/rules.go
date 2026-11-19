package library

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agent"
)

// Rules are Markdown files of guidance kept once and given to the agents
// ticked for each, in the folder of rules each reads by itself
// (JonahFu on X: Claude Code's rules folder). A rule may name the files
// it is about (Paths, globs): the agent then reads it only while working
// on a file that matches, instead of in every conversation. Each agent's
// folder and shape, read from its docs and source:
//
//	Claude Code   <claude dir>/rules/<name>.md    paths: [globs] in the frontmatter; one without loads at launch
//	              (code.claude.com/docs/en/memory; 2.1.327's binary reads <config home>/rules, every .md under it)
//	Cline         <cline dir>/rules/<name>.md     always read: Cline's rules take no paths (CLI 3.0.70's
//	              e1(): ~/.cline/rules, ~/Cline/Rules…; name, enabled, disabled its only keys)
//	Kiro CLI      ~/.kiro/steering/<name>.md      inclusion: fileMatch and fileMatchPattern: [globs] for paths,
//	              always included otherwise (kiro.dev/docs/steering)
//
// A rule with paths isn't written for Cline, which would read it in every
// conversation: the page says why. Cursor keeps its user rules in its own
// settings (only a project has .cursor/rules), Codex's rules are command
// policies and Droid's command permissions, and Gemini CLI, Qwen Code and
// OpenCode read no folder of rules: none of them is listed. What magpie
// wrote is recorded file by file with its hash (Applied.Rules), as
// subagents' and slash commands' are (syncOwnFiles): a file still as
// magpie wrote it is magpie's to rewrite or remove, one the user changed
// is theirs from then on, and a file by that name magpie didn't write is
// never touched.

// Rule is a file of guidance the agents ticked for it are given.
type Rule struct {
	Name string `json:"name"`
	// Paths are the globs of the files it is about, relative to the
	// project; none: it is read in every conversation
	Paths  []string `json:"paths,omitempty"`
	Body   string   `json:"body"`
	Agents []string `json:"agents"`
}

const (
	maxRuleBody  = 100_000
	maxRulePaths = 50
	maxRulePath  = 500
)

// RuleAgents are the agents a rule can be given, in the order the page
// lists them.
var RuleAgents = []string{"claude", "cline", "kiro-cli"}

// ruleSpec is where an agent keeps its rules and how it reads one.
type ruleSpec struct {
	dir string
	// paths: it reads a rule only for the files its globs match
	paths  bool
	render func(r *Rule) []byte
}

func (sp *ruleSpec) path(name string) string { return filepath.Join(sp.dir, name+".md") }

// wslNoRules is why an agent in WSL is given no rules, its distro running
// or not.
const wslNoRules = "magpie gives no rules to an agent in WSL yet"

// plainRule is a rule's body as a file with no frontmatter of its own: one
// that starts with --- is given an empty frontmatter first, or the agent
// would take its first lines for one.
func plainRule(body string) []byte {
	if strings.HasPrefix(body, "---") {
		return frontmatter(nil, body)
	}
	return []byte(strings.TrimRight(body, "\n") + "\n")
}

func ruleSpecOf(t *Target) (*ruleSpec, string) {
	a := t.Agent
	if a.WSL != "" {
		return nil, wslNoRules
	}
	switch a.ID {
	case "claude":
		return &ruleSpec{dir: filepath.Join(claudeDir(), "rules"), paths: true, render: func(r *Rule) []byte {
			if len(r.Paths) == 0 {
				return plainRule(r.Body)
			}
			return frontmatter([][2]string{{"paths", yamlList(r.Paths)}}, r.Body)
		}}, ""
	case "cline":
		return &ruleSpec{dir: filepath.Join(a.Dir, "rules"), render: func(r *Rule) []byte { return plainRule(r.Body) }}, ""
	case "kiro-cli":
		// beside the AGENTS.md magpie gives it its instructions in
		dir := filepath.Join(home(), ".kiro", "steering")
		if t.Instructions != "" {
			dir = filepath.Dir(t.Instructions)
		}
		return &ruleSpec{dir: dir, paths: true, render: func(r *Rule) []byte {
			if len(r.Paths) == 0 {
				return plainRule(r.Body)
			}
			return frontmatter([][2]string{{"inclusion", "fileMatch"}, {"fileMatchPattern", yamlList(r.Paths)}}, r.Body)
		}}, ""
	}
	return nil, "magpie can't give " + a.Name + " rules: it reads none from a folder of its own"
}

// noPaths is why an agent that reads every rule always isn't given one
// that names its files.
func noPaths(name string) error {
	return fmt.Errorf("%s reads every rule in every conversation: it can't keep one to the files it names", name)
}

// kiroAgents is why Kiro CLI isn't given a rule called agents: its file
// would be the AGENTS.md magpie gives Kiro its instructions in, on a disk
// that tells no case apart.
const kiroAgents = "Kiro CLI keeps its instructions in steering/AGENTS.md: give this rule another name"

func (r *Rule) check() error {
	if !commandNameRe.MatchString(r.Name) {
		return fmt.Errorf("rule name %q: use lowercase letters, digits, - and _, at most 64, not starting or ending with - or _", r.Name)
	}
	if strings.TrimSpace(r.Body) == "" {
		return errors.New("a rule needs what it says")
	}
	if len(r.Body) > maxRuleBody {
		return fmt.Errorf("a rule is at most %d bytes", maxRuleBody)
	}
	if len(r.Paths) > maxRulePaths {
		return fmt.Errorf("a rule names at most %d paths", maxRulePaths)
	}
	for _, p := range r.Paths {
		if p == "" || len(p) > maxRulePath || strings.ContainsAny(p, "\n\r") {
			return fmt.Errorf("the path %q: one glob a line, at most %d characters", p, maxRulePath)
		}
	}
	for _, a := range r.Agents {
		if base, _, _ := strings.Cut(a, "@wsl:"); !slices.Contains(RuleAgents, base) {
			return fmt.Errorf("%s can't be given rules", a)
		}
	}
	return nil
}

func (l *Library) rule(name string) *Rule {
	for _, r := range l.Rules {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// ruleWhyNot is why the agent can't have this rule, or "".
func ruleWhyNot(t *Target, sp *ruleSpec, r *Rule) string {
	switch {
	case !sp.paths && len(r.Paths) > 0:
		return noPaths(t.Agent.Name).Error()
	case t.Agent.ID == "kiro-cli" && r.Name == "agents":
		return kiroAgents
	}
	return ""
}

func (l *Library) syncRules(t *Target, b *backups, res *Result) {
	id := t.Agent.ID
	a := l.applied(id)
	var want []*Rule
	for _, r := range l.Rules {
		if slices.Contains(r.Agents, id) {
			want = append(want, r)
		}
	}
	if len(a.Rules) == 0 && len(want) == 0 {
		return
	}
	sp, why := ruleSpecOf(t)
	if sp == nil {
		for _, r := range want {
			res.fail(id, "rule:"+r.Name, errors.New(why))
		}
		return
	}
	var files []ownFile
	for _, r := range want {
		if why := ruleWhyNot(t, sp, r); why != "" {
			// not written: what magpie wrote of it before goes
			res.fail(id, "rule:"+r.Name, errors.New(why))
			continue
		}
		files = append(files, ownFile{name: r.Name, path: sp.path(r.Name), body: func() []byte { return sp.render(r) }})
	}
	a.Rules = syncOwnFiles(t, "rule", a.Rules, files, b, res)
}

// ---- changes ---------------------------------------------------------------

// cleanPaths are the globs as kept: one a line, each trimmed, no blank one
// and none twice, in the order given.
func cleanPaths(ps []string) []string {
	var out []string
	for _, p := range ps {
		for _, x := range strings.Split(strings.ReplaceAll(p, "\r\n", "\n"), "\n") {
			if x = strings.TrimSpace(x); x != "" && !slices.Contains(out, x) {
				out = append(out, x)
			}
		}
	}
	return out
}

// SaveRule adds a rule, or replaces the one called old (renaming it: the
// files magpie wrote by the old name go).
func SaveRule(old string, r Rule) (*Result, error) {
	r.Name = strings.TrimSpace(r.Name)
	r.Paths = cleanPaths(r.Paths)
	r.Body = strings.TrimRight(strings.ReplaceAll(r.Body, "\r\n", "\n"), " \n\t")
	if err := r.check(); err != nil {
		return nil, err
	}
	return change(func(l *Library) error {
		if r.Name != old && l.rule(r.Name) != nil {
			return fmt.Errorf("the library already has a rule called %s", r.Name)
		}
		if old != "" {
			i := slices.IndexFunc(l.Rules, func(x *Rule) bool { return x.Name == old })
			if i < 0 {
				return fmt.Errorf("no rule called %s", old)
			}
			l.Rules = slices.Delete(l.Rules, i, i+1)
		}
		r.Agents = orNone(slices.Compact(slices.Sorted(slices.Values(r.Agents))))
		l.Rules = append(l.Rules, &r)
		return nil
	})
}

// RuleAgentsSet sets which agents get a rule.
func RuleAgentsSet(name string, agents []string) (*Result, error) {
	return change(func(l *Library) error {
		r := l.rule(name)
		if r == nil {
			return fmt.Errorf("no rule called %s", name)
		}
		x := *r
		x.Agents = agents
		if err := x.check(); err != nil {
			return err
		}
		r.Agents = orNone(slices.Compact(slices.Sorted(slices.Values(agents))))
		return nil
	})
}

// RemoveRules takes the rules named out of the library, and the files
// magpie wrote of them, still as it wrote them, out of every agent.
func RemoveRules(names []string) (*Result, error) {
	if len(names) == 0 {
		return nil, errors.New("no rules to remove")
	}
	return change(func(l *Library) error {
		for _, n := range names {
			if l.rule(n) == nil {
				return fmt.Errorf("no rule called %s", n)
			}
		}
		l.Rules = slices.DeleteFunc(l.Rules, func(r *Rule) bool { return slices.Contains(names, r.Name) })
		return nil
	})
}

// ---- the page --------------------------------------------------------------

// RuleView is a library rule, and where each agent has it.
type RuleView struct {
	*Rule
	// Files are, for each agent it was written to, the file
	Files map[string]string `json:"files,omitempty"`
	// Problems are the agents it couldn't be given to, and why
	Problems map[string]string `json:"problems,omitempty"`
}

// RuleAgentView is an agent the Rules tab lists.
type RuleAgentView struct {
	ID string `json:"id"`
	// Dir is where its rules are; No is why magpie can't give it any
	Dir string `json:"dir,omitempty"`
	No  string `json:"no,omitempty"`
	WSL bool   `json:"wsl,omitempty"`
	// Paths: it keeps a rule to the files it names; one without reads
	// every rule always
	Paths bool `json:"paths,omitempty"`
}

// rulesView is the Rules page for these agents, and those in stopped WSL
// distros (asleep), greyed as while their distro runs.
func (l *Library) rulesView(targets []*Target, asleep []*agent.Agent, of func(string) map[string]string) ([]RuleView, []RuleAgentView) {
	agents := []RuleAgentView{}
	type known struct {
		t  *Target
		sp *ruleSpec
	}
	byID := map[string]known{}
	for _, t := range targets {
		if base, _, _ := strings.Cut(t.Agent.ID, "@wsl:"); !slices.Contains(RuleAgents, base) {
			continue
		}
		sp, why := ruleSpecOf(t)
		av := RuleAgentView{ID: t.Agent.ID, No: why, WSL: t.Agent.WSL != ""}
		if sp != nil {
			av.Dir, av.Paths = sp.dir, sp.paths
			byID[t.Agent.ID] = known{t, sp}
		}
		agents = append(agents, av)
	}
	for _, a := range asleep {
		if base, _, _ := strings.Cut(a.ID, "@wsl:"); slices.Contains(RuleAgents, base) && shapeOf(a) != nil {
			agents = append(agents, RuleAgentView{ID: a.ID, No: wslNoRules, WSL: true})
		}
	}
	views := []RuleView{}
	for _, r := range l.Rules {
		x := *r
		x.Agents = orNone(x.Agents)
		rv := RuleView{Rule: &x, Problems: of("rule:" + r.Name)}
		for id, a := range l.Applied {
			for _, f := range a.Rules {
				if f.Name == r.Name && slices.Contains(r.Agents, id) {
					if rv.Files == nil {
						rv.Files = map[string]string{}
					}
					rv.Files[id] = f.Path
				}
			}
		}
		for _, av := range agents {
			if !slices.Contains(r.Agents, av.ID) || rv.Problems[av.ID] != "" {
				continue
			}
			why := av.No
			if k, ok := byID[av.ID]; ok && why == "" {
				why = ruleWhyNot(k.t, k.sp, r)
			}
			if why == "" {
				continue
			}
			if rv.Problems == nil {
				rv.Problems = map[string]string{}
			}
			rv.Problems[av.ID] = why
		}
		views = append(views, rv)
	}
	return views, agents
}

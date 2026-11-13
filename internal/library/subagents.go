package library

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/lazyre"
)

// Subagents (#2283) are agents of their own the main agent hands a task
// to: a name, what it is for, its prompt, and the skills and servers it
// works with. One is kept once and written into each agent ticked for it,
// as a file of that agent's own, in its own folder and shape (each read
// from the agent's source or binary):
//
//	Claude Code     <claude dir>/agents/<name>.md      name, description, skills, mcpServers; the prompt below
//	Codex           <codex home>/agents/<name>.toml    name, description, model, developer_instructions
//	OpenCode        <config>/agents/<name>.md          description, mode: subagent; named by its file
//	Gemini CLI      ~/.gemini/agents/<name>.md         name, description
//	Qwen Code       <qwen home>/agents/<name>.md       name, description
//	Factory Droid   ~/.factory/droids/<name>.md        name, description, mcpServers
//	Grok Build      <grok home>/agents/<name>.md       name, description
//	Cursor          ~/.cursor/agents/<name>.md         name, description
//	Copilot CLI     <copilot home>/agents/<name>.agent.md  name, description
//
// Only Claude Code takes a subagent's skills, and only Claude Code and
// Droid its servers by name; every other agent's subagents use whatever
// the agent has (the page says so). Only Codex is given a subagent's
// model (luckymanjc on Discord): its role file is a config layer whose
// model is laid over the one the lead names in spawn_agent
// (codex-rs core/src/agent/child_config.rs, apply_spawn_agent_role after
// apply_requested_spawn_agent_model_overrides), so the subagent's
// requests carry it. Every other agent's subagent runs on the model that
// agent picks for it (the page says so). What magpie wrote is recorded file
// by file with its hash (Applied.Subagents): a file still as magpie wrote
// it is magpie's to rewrite or remove, one the user changed is theirs from
// then on, and a file by that name magpie didn't write is never touched.

// Subagent is one the agents ticked for it are given.
type Subagent struct {
	Name        string `json:"name"`
	Bundle      string `json:"bundle,omitempty"` // the plugin it came with (plugin.go)
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	// Model is the model or routing group the subagent runs on, as the
	// agent names it; "" leaves it to the agent
	Model  string   `json:"model,omitempty"`
	Skills []string `json:"skills,omitempty"`
	MCP    []string `json:"mcp,omitempty"`
	Agents []string `json:"agents"`
}

// SubagentFile is a file magpie wrote for a subagent, and the hash of what
// it wrote there.
type SubagentFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// subagentNameRe is a name every agent takes: Cursor's and CodeBuddy's
// are lowercase letters, digits and -, Qwen Code's 2 to 50 characters
// that neither start nor end with - (validateName in its subagents).
var subagentNameRe = lazyre.New(`^[a-z0-9][a-z0-9-]{0,48}[a-z0-9]$`)

// reservedSubagents are names Qwen Code keeps for itself.
var reservedSubagents = []string{"self", "system", "user", "model", "tool", "config", "default", "main"}

const (
	maxSubagentDescription = 1000
	maxSubagentPrompt      = 100_000
	maxSubagentModel       = 200
)

// SubagentAgents are the agents a subagent can be given, in the order the
// page lists them.
var SubagentAgents = []string{"claude", "codex", "opencode", "gemini", "qwen", "droid", "grok", "cursor", "copilot"}

// subagentSpec is where an agent keeps its subagents and how it reads one.
type subagentSpec struct {
	dir string
	ext string
	// skills and mcp: the agent's file names the subagent's own; one
	// without uses all the agent has. model: it names the subagent's
	// model; one without runs it on the model the agent picks
	skills, mcp, model bool
	render             func(s *Subagent, skills, mcp []string) []byte
}

func (sp *subagentSpec) path(name string) string { return filepath.Join(sp.dir, name+sp.ext) }

// wslNoSubagents is why an agent in WSL is given no subagents, its distro
// running or not.
const wslNoSubagents = "magpie gives no subagents to an agent in WSL yet"

func subagentSpecOf(t *Target) (*subagentSpec, string) {
	a := t.Agent
	if a.WSL != "" {
		return nil, wslNoSubagents
	}
	md := func(dir string, keys func(s *Subagent, skills, mcp []string) [][2]string) *subagentSpec {
		return &subagentSpec{dir: dir, ext: ".md", render: func(s *Subagent, skills, mcp []string) []byte {
			return frontmatter(keys(s, skills, mcp), s.Prompt)
		}}
	}
	plain := func(s *Subagent, _, _ []string) [][2]string {
		return [][2]string{{"name", yamlStr(s.Name)}, {"description", yamlStr(s.Description)}}
	}
	h := home()
	switch a.ID {
	case "claude":
		sp := md(filepath.Join(claudeDir(), "agents"), func(s *Subagent, skills, mcp []string) [][2]string {
			kv := plain(s, nil, nil)
			if len(skills) > 0 {
				kv = append(kv, [2]string{"skills", yamlList(skills)})
			}
			if len(mcp) > 0 {
				kv = append(kv, [2]string{"mcpServers", yamlList(mcp)})
			}
			return kv
		})
		sp.skills, sp.mcp = true, true
		return sp, ""
	case "codex":
		return &subagentSpec{dir: filepath.Join(codexDir(), "agents"), ext: ".toml", render: codexRole, model: true}, ""
	case "opencode":
		// named by its file; mode subagent is for the main agent to call
		return md(filepath.Join(filepath.Dir(a.Path), "agents"), func(s *Subagent, _, _ []string) [][2]string {
			return [][2]string{{"description", yamlStr(s.Description)}, {"mode", "subagent"}}
		}), ""
	case "gemini":
		return md(filepath.Join(h, ".gemini", "agents"), plain), ""
	case "qwen":
		return md(filepath.Join(a.Dir, "agents"), plain), ""
	case "droid":
		sp := md(filepath.Join(a.Dir, "droids"), func(s *Subagent, _, mcp []string) [][2]string {
			kv := plain(s, nil, nil)
			if len(mcp) > 0 {
				kv = append(kv, [2]string{"mcpServers", yamlList(mcp)})
			}
			return kv
		})
		sp.mcp = true
		return sp, ""
	case "grok":
		return md(filepath.Join(a.Dir, "agents"), plain), ""
	case "cursor":
		return md(filepath.Join(h, ".cursor", "agents"), plain), ""
	case "copilot":
		d := appdir.Getenv("COPILOT_HOME")
		if d == "" {
			d = filepath.Join(h, ".copilot")
		}
		sp := md(filepath.Join(d, "agents"), plain)
		sp.ext = ".agent.md"
		return sp, ""
	}
	return nil, "magpie can't give " + a.Name + " subagents: it reads none from a folder of its own"
}

// yamlStr is s as a YAML scalar: JSON's quoted string is one.
func yamlStr(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func yamlList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = yamlStr(x)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func frontmatter(kv [][2]string, body string) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	for _, p := range kv {
		b.WriteString(p[0] + ": " + p[1] + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimRight(body, "\n") + "\n")
	return []byte(b.String())
}

// codexRole is an agent role file as Codex reads one from its agents
// folder (codex-rs/agent-roles): name, description and its prompt as
// developer_instructions, each required, and the model it runs on when
// one is set. The file is a config layer (RawAgentRoleFileToml flattens
// ConfigToml), and its model is put over the lead's pick
// (AgentRoleOverrides in core/src/agent/role.rs). A subagent with no
// model is written as it was before there was one.
func codexRole(s *Subagent, _, _ []string) []byte {
	b, _ := toml.Marshal(struct {
		Name                  string `toml:"name"`
		Description           string `toml:"description"`
		Model                 string `toml:"model,omitempty"`
		DeveloperInstructions string `toml:"developer_instructions,multiline"`
	}{s.Name, s.Description, s.Model, strings.TrimRight(s.Prompt, "\n") + "\n"})
	return b
}

func subagentHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (s *Subagent) check() error {
	if !subagentNameRe.MatchString(s.Name) {
		return fmt.Errorf("subagent name %q: use 2 to 50 lowercase letters, digits and -, not starting or ending with -", s.Name)
	}
	if slices.Contains(reservedSubagents, s.Name) {
		return fmt.Errorf("%s is a name agents keep for themselves", s.Name)
	}
	if s.Description == "" {
		return errors.New("a subagent needs a description: it is how the main agent knows when to use it")
	}
	if utf8.RuneCountInString(s.Description) > maxSubagentDescription {
		return fmt.Errorf("a subagent's description is at most %d characters", maxSubagentDescription)
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return errors.New("a subagent needs a prompt")
	}
	if len(s.Prompt) > maxSubagentPrompt {
		return fmt.Errorf("a subagent's prompt is at most %d bytes", maxSubagentPrompt)
	}
	if len(s.Model) > maxSubagentModel || strings.ContainsFunc(s.Model, unicode.IsSpace) {
		return fmt.Errorf("subagent model %q: a model's id, without spaces, of at most %d bytes", s.Model, maxSubagentModel)
	}
	for _, a := range s.Agents {
		if base, _, _ := strings.Cut(a, "@wsl:"); !slices.Contains(SubagentAgents, base) {
			return fmt.Errorf("%s can't be given subagents", a)
		}
	}
	return nil
}

func (l *Library) subagent(name string) *Subagent {
	for _, s := range l.Subagents {
		if s.Name == name {
			return s
		}
	}
	return nil
}

// renderSubagent is s as an agent's file: of its skills and servers,
// those the library still has, a server turned off (ServerOn) left out.
func (l *Library) renderSubagent(sp *subagentSpec, s *Subagent) []byte {
	var skills, mcp []string
	if sp.skills {
		for _, n := range s.Skills {
			if l.skill(n) != nil {
				skills = append(skills, n)
			}
		}
	}
	if sp.mcp {
		for _, n := range s.MCP {
			if x := l.server(n); x != nil && !x.Hidden {
				mcp = append(mcp, n)
			}
		}
	}
	return sp.render(s, skills, mcp)
}

func (l *Library) syncSubagents(t *Target, b *backups, res *Result) {
	id := t.Agent.ID
	a := l.applied(id)
	var want []*Subagent
	for _, s := range l.Subagents {
		if slices.Contains(s.Agents, id) {
			want = append(want, s)
		}
	}
	if len(a.Subagents) == 0 && len(want) == 0 {
		return
	}
	sp, why := subagentSpecOf(t)
	if sp == nil {
		for _, s := range want {
			res.fail(id, "subagent:"+s.Name, errors.New(why))
		}
		return
	}
	wanted := map[string]*Subagent{} // path → the subagent written there
	for _, s := range want {
		wanted[sp.path(s.Name)] = s
	}
	done := map[string]bool{}
	var kept []SubagentFile
	write := func(s *Subagent, path string, body []byte) bool {
		if err := b.keep(id, path); err != nil {
			res.fail(id, "subagent:"+s.Name, err)
			return false
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			res.fail(id, "subagent:"+s.Name, err)
			return false
		}
		if err := edit.WriteAtomic(path, body); err != nil {
			res.fail(id, "subagent:"+s.Name, err)
			return false
		}
		res.changed(id)
		return true
	}
	for _, f := range a.Subagents {
		cur, err := os.ReadFile(f.Path)
		switch {
		case os.IsNotExist(err):
			// gone: written again below if it is still wanted
			continue
		case err != nil:
			// can't tell whether it is still magpie's: left, and kept on
			// record, untouched
			res.fail(id, "subagent:"+f.Name, err)
			kept = append(kept, f)
			done[f.Path] = true
			continue
		case subagentHash(cur) != f.Hash:
			// changed since magpie wrote it: the user's from now on, which
			// the loop below says if the library still wants that name
			continue
		}
		s := wanted[f.Path]
		if s == nil {
			if err := b.keep(id, f.Path); err != nil {
				res.fail(id, "subagent:"+f.Name, err)
				kept = append(kept, f)
				continue
			}
			if err := os.Remove(f.Path); err != nil && !os.IsNotExist(err) {
				res.fail(id, "subagent:"+f.Name, err)
				kept = append(kept, f)
				continue
			}
			res.changed(id)
			continue
		}
		done[f.Path] = true
		body := l.renderSubagent(sp, s)
		if !bytes.Equal(cur, body) && !write(s, f.Path, body) {
			kept = append(kept, f)
			continue
		}
		kept = append(kept, SubagentFile{Name: s.Name, Path: f.Path, Hash: subagentHash(body)})
	}
	for _, s := range want {
		path := sp.path(s.Name)
		if done[path] {
			continue
		}
		body := l.renderSubagent(sp, s)
		cur, err := os.ReadFile(path)
		switch {
		case err == nil && bytes.Equal(cur, body):
			// the same as magpie would write: taken as magpie's
		case err == nil:
			res.fail(id, "subagent:"+s.Name, fmt.Errorf("%s has a subagent of its own at %s, which magpie leaves as it is", t.Agent.Name, path))
			continue
		case !os.IsNotExist(err):
			res.fail(id, "subagent:"+s.Name, err)
			continue
		default:
			if !write(s, path, body) {
				continue
			}
		}
		kept = append(kept, SubagentFile{Name: s.Name, Path: path, Hash: subagentHash(body)})
	}
	slices.SortFunc(kept, func(x, y SubagentFile) int { return strings.Compare(x.Path, y.Path) })
	a.Subagents = kept
}

// ---- changes ---------------------------------------------------------------

// SaveSubagent adds a subagent, or replaces the one called old (renaming
// it: the files magpie wrote by the old name go).
func SaveSubagent(old string, s Subagent) (*Result, error) {
	s.Name = strings.TrimSpace(s.Name)
	s.Description = strings.Join(strings.Fields(s.Description), " ")
	s.Prompt = strings.TrimRight(strings.ReplaceAll(s.Prompt, "\r\n", "\n"), " \n\t")
	s.Model = strings.TrimSpace(s.Model)
	if err := s.check(); err != nil {
		return nil, err
	}
	return change(func(l *Library) error {
		if s.Name != old && l.subagent(s.Name) != nil {
			return fmt.Errorf("the library already has a subagent called %s", s.Name)
		}
		if old != "" {
			i := slices.IndexFunc(l.Subagents, func(x *Subagent) bool { return x.Name == old })
			if i < 0 {
				return fmt.Errorf("no subagent called %s", old)
			}
			l.Subagents = slices.Delete(l.Subagents, i, i+1)
		}
		s.Skills = slices.Compact(slices.Sorted(slices.Values(s.Skills)))
		s.MCP = slices.Compact(slices.Sorted(slices.Values(s.MCP)))
		s.Agents = orNone(slices.Compact(slices.Sorted(slices.Values(s.Agents))))
		l.Subagents = append(l.Subagents, &s)
		return nil
	})
}

// SubagentAgentsSet sets which agents get a subagent.
func SubagentAgentsSet(name string, agents []string) (*Result, error) {
	return change(func(l *Library) error {
		s := l.subagent(name)
		if s == nil {
			return fmt.Errorf("no subagent called %s", name)
		}
		c := *s
		c.Agents = agents
		if err := c.check(); err != nil {
			return err
		}
		s.Agents = orNone(slices.Compact(slices.Sorted(slices.Values(agents))))
		return nil
	})
}

// RemoveSubagents takes the subagents named out of the library, and the
// files magpie wrote of them, still as it wrote them, out of every agent.
func RemoveSubagents(names []string) (*Result, error) {
	if len(names) == 0 {
		return nil, errors.New("no subagents to remove")
	}
	return change(func(l *Library) error {
		for _, n := range names {
			if l.subagent(n) == nil {
				return fmt.Errorf("no subagent called %s", n)
			}
		}
		l.Subagents = slices.DeleteFunc(l.Subagents, func(s *Subagent) bool { return slices.Contains(names, s.Name) })
		return nil
	})
}

// ---- the page --------------------------------------------------------------

// SubagentView is a library subagent, and where each agent has it.
type SubagentView struct {
	*Subagent
	// Files are, for each agent it was written to, the file
	Files map[string]string `json:"files,omitempty"`
	// Problems are the agents it couldn't be given to, and why
	Problems map[string]string `json:"problems,omitempty"`
}

// SubagentAgentView is an agent the Subagents tab lists.
type SubagentAgentView struct {
	ID string `json:"id"`
	// Dir is where its subagents are; No is why magpie can't give it any
	Dir string `json:"dir,omitempty"`
	No  string `json:"no,omitempty"`
	WSL bool   `json:"wsl,omitempty"`
	// Skills and MCP: its subagents take skills, servers of their own;
	// one that doesn't uses all the agent has
	Skills bool `json:"skills,omitempty"`
	MCP    bool `json:"mcp,omitempty"`
	// Model: its subagents run on a model of their own when one is set;
	// one that doesn't runs them on the model the agent picks
	Model bool `json:"model,omitempty"`
}

// subagentsView is the Subagents page for these agents, and those in
// stopped WSL distros (asleep) that the page lists by what magpie last
// knew of them (asleepView): greyed as while their distro runs, since
// magpie gives no agent in WSL subagents yet, and nothing of theirs opened
// (#3896, #4238).
func (l *Library) subagentsView(targets []*Target, asleep []*agent.Agent, of func(string) map[string]string) ([]SubagentView, []SubagentAgentView) {
	agents := []SubagentAgentView{}
	for _, t := range targets {
		if base, _, _ := strings.Cut(t.Agent.ID, "@wsl:"); !slices.Contains(SubagentAgents, base) {
			continue
		}
		sp, why := subagentSpecOf(t)
		av := SubagentAgentView{ID: t.Agent.ID, No: why, WSL: t.Agent.WSL != ""}
		if sp != nil {
			av.Dir, av.Skills, av.MCP, av.Model = sp.dir, sp.skills, sp.mcp, sp.model
		}
		agents = append(agents, av)
	}
	for _, a := range asleep {
		if base, _, _ := strings.Cut(a.ID, "@wsl:"); slices.Contains(SubagentAgents, base) && shapeOf(a) != nil {
			agents = append(agents, SubagentAgentView{ID: a.ID, No: wslNoSubagents, WSL: true})
		}
	}
	views := []SubagentView{}
	for _, s := range l.Subagents {
		c := *s
		c.Agents = orNone(c.Agents)
		sv := SubagentView{Subagent: &c, Problems: of("subagent:" + s.Name)}
		for id, a := range l.Applied {
			for _, f := range a.Subagents {
				if f.Name == s.Name && slices.Contains(s.Agents, id) {
					if sv.Files == nil {
						sv.Files = map[string]string{}
					}
					sv.Files[id] = f.Path
				}
			}
		}
		for _, av := range agents {
			if av.No != "" && slices.Contains(s.Agents, av.ID) {
				if sv.Problems == nil {
					sv.Problems = map[string]string{}
				}
				if sv.Problems[av.ID] == "" {
					sv.Problems[av.ID] = av.No
				}
			}
		}
		views = append(views, sv)
	}
	return views, agents
}

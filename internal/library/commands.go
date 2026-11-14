package library

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/lazyre"
)

// Slash commands are prompts kept once and typed as /name in the agents
// ticked for each (miantiao_me on X): a name, what it is for, and the
// prompt the agent is sent, with the words typed after the name put in
// where it says $ARGUMENTS, and the first, second… of them where it says
// $1, $2… Each is written into the agent's own folder of commands, in its
// own shape (each read from the agent's docs and source):
//
//	Claude Code     <claude dir>/commands/<name>.md    description; $n is $ARGUMENTS[n-1] there, counted from 0
//	OpenCode        <config>/commands/<name>.md        description; $ARGUMENTS and $1… as magpie's
//	Pi              <agent dir>/prompts/<name>.md      description; $ARGUMENTS and $1… as magpie's
//	Gemini CLI      ~/.gemini/commands/<name>.toml     description, prompt; $ARGUMENTS is {{args}}, no $1…
//	Qwen Code       <qwen home>/commands/<name>.md     description; $ARGUMENTS is {{args}}, no $1…
//	Factory Droid   ~/.factory/commands/<name>.md      description; $ARGUMENTS, no $1…
//
// A command that uses $1… isn't written for an agent that has no such
// thing: the page says why. Codex's custom prompts (~/.codex/prompts) are
// listed but greyed: Codex reads them no more, its skills having taken
// their place. What magpie wrote is recorded file by file with its hash
// (Applied.Commands), as subagents' are (syncOwnFiles): a file still as
// magpie wrote it is magpie's to rewrite or remove, one the user changed
// is theirs from then on, and a file by that name magpie didn't write is
// never touched.

// Command is a slash command the agents ticked for it are given.
type Command struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Prompt      string   `json:"prompt"`
	Agents      []string `json:"agents"`
}

// commandNameRe is a name each agent takes as one command of its folder:
// a file's name, no / (a folder there makes a namespace) and no :
// (Gemini CLI's namespace mark).
var commandNameRe = lazyre.New(`^[a-z0-9](?:[a-z0-9_-]{0,62}[a-z0-9])?$`)

const (
	maxCommandDescription = 1000
	maxCommandPrompt      = 100_000
)

// CommandAgents are the agents a slash command can be given, in the order
// the page lists them.
var CommandAgents = []string{"claude", "codex", "opencode", "pi", "gemini", "qwen", "droid"}

// commandSpec is where an agent keeps its slash commands and how it reads
// one.
type commandSpec struct {
	dir string
	ext string
	// args: it puts the first, second… word typed in for $1, $2…
	args   bool
	render func(c *Command) []byte
}

func (sp *commandSpec) path(name string) string { return filepath.Join(sp.dir, name+sp.ext) }

const (
	// wslNoCommands is why an agent in WSL is given no slash commands,
	// its distro running or not.
	wslNoCommands = "magpie gives no slash commands to an agent in WSL yet"
	// codexNoCommands is why Codex is given none: custom prompts were
	// deprecated for skills, and Codex no longer reads ~/.codex/prompts.
	codexNoCommands = "Codex reads custom prompts no more: its skills took their place"
)

// argRe is a word typed in, by its place: $1, $2…
var argRe = lazyre.New(`\$([0-9]+)`)

// usesArgs says whether a prompt takes words typed in by their place.
func usesArgs(prompt string) bool {
	for _, m := range argRe.FindAllStringSubmatch(prompt, -1) {
		if m[1] != "0" {
			return true
		}
	}
	return false
}

func commandSpecOf(t *Target) (*commandSpec, string) {
	a := t.Agent
	if a.WSL != "" {
		return nil, wslNoCommands
	}
	md := func(dir string, body func(string) string) *commandSpec {
		return &commandSpec{dir: dir, ext: ".md", render: func(c *Command) []byte {
			if c.Description == "" {
				// each takes the prompt's first line for what it is for
				return []byte(strings.TrimRight(body(c.Prompt), "\n") + "\n")
			}
			return frontmatter([][2]string{{"description", yamlStr(c.Description)}}, body(c.Prompt))
		}}
	}
	same := func(p string) string { return p }
	args := func(p string) string { return strings.ReplaceAll(p, "$ARGUMENTS", "{{args}}") }
	switch a.ID {
	case "claude":
		// Claude Code counts $0 as the first word, and reads \$ as a $
		sp := md(filepath.Join(claudeDir(), "commands"), claudeArgs)
		sp.args = true
		return sp, ""
	case "codex":
		return nil, codexNoCommands
	case "opencode":
		// it reads command/ and commands/; commands/ is the one its docs name
		sp := md(filepath.Join(filepath.Dir(a.Path), "commands"), same)
		sp.args = true
		return sp, ""
	case "pi":
		sp := md(filepath.Join(a.Dir, "prompts"), same)
		sp.args = true
		return sp, ""
	case "gemini":
		return &commandSpec{dir: filepath.Join(home(), ".gemini", "commands"), ext: ".toml", render: func(c *Command) []byte {
			b, _ := toml.Marshal(struct {
				Description string `toml:"description,omitempty"`
				Prompt      string `toml:"prompt,multiline"`
			}{c.Description, strings.TrimRight(args(c.Prompt), "\n") + "\n"})
			return b
		}}, ""
	case "qwen":
		return md(filepath.Join(a.Dir, "commands"), args), ""
	case "droid":
		return md(filepath.Join(a.Dir, "commands"), same), ""
	}
	return nil, "magpie can't give " + a.Name + " slash commands: it reads none from a folder of its own"
}

// claudeArgs is a prompt as Claude Code reads one: $n is $ARGUMENTS[n-1],
// and a $0 that is no word typed in is written \$0, as its docs have it.
func claudeArgs(p string) string {
	return argRe.ReplaceAllStringFunc(p, func(m string) string {
		n, err := strconv.Atoi(m[1:])
		if err != nil || n == 0 {
			return `\` + m
		}
		return "$ARGUMENTS[" + strconv.Itoa(n-1) + "]"
	})
}

func (c *Command) check() error {
	if !commandNameRe.MatchString(c.Name) {
		return fmt.Errorf("slash command name %q: use lowercase letters, digits, - and _, at most 64, not starting or ending with - or _", c.Name)
	}
	if utf8.RuneCountInString(c.Description) > maxCommandDescription {
		return fmt.Errorf("a slash command's description is at most %d characters", maxCommandDescription)
	}
	if strings.TrimSpace(c.Prompt) == "" {
		return errors.New("a slash command needs a prompt")
	}
	if len(c.Prompt) > maxCommandPrompt {
		return fmt.Errorf("a slash command's prompt is at most %d bytes", maxCommandPrompt)
	}
	for _, a := range c.Agents {
		if base, _, _ := strings.Cut(a, "@wsl:"); !slices.Contains(CommandAgents, base) {
			return fmt.Errorf("%s can't be given slash commands", a)
		}
	}
	return nil
}

func (l *Library) command(name string) *Command {
	for _, c := range l.Commands {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// noArgs is why an agent that has no $1… isn't given a command that uses
// them.
func noArgs(name string) error {
	return fmt.Errorf("%s takes no words by their place ($1, $2…): only $ARGUMENTS, all of them", name)
}

func (l *Library) syncCommands(t *Target, b *backups, res *Result) {
	id := t.Agent.ID
	a := l.applied(id)
	var want []*Command
	for _, c := range l.Commands {
		if slices.Contains(c.Agents, id) {
			want = append(want, c)
		}
	}
	if len(a.Commands) == 0 && len(want) == 0 {
		return
	}
	sp, why := commandSpecOf(t)
	if sp == nil {
		for _, c := range want {
			res.fail(id, "command:"+c.Name, errors.New(why))
		}
		return
	}
	var files []ownFile
	for _, c := range want {
		if !sp.args && usesArgs(c.Prompt) {
			// not written: what magpie wrote of it before goes
			res.fail(id, "command:"+c.Name, noArgs(t.Agent.Name))
			continue
		}
		files = append(files, ownFile{name: c.Name, path: sp.path(c.Name), body: func() []byte { return sp.render(c) }})
	}
	a.Commands = syncOwnFiles(t, "command", a.Commands, files, b, res)
}

// ---- changes ---------------------------------------------------------------

// SaveCommand adds a slash command, or replaces the one called old
// (renaming it: the files magpie wrote by the old name go).
func SaveCommand(old string, c Command) (*Result, error) {
	c.Name = strings.TrimSpace(c.Name)
	c.Description = strings.Join(strings.Fields(c.Description), " ")
	c.Prompt = strings.TrimRight(strings.ReplaceAll(c.Prompt, "\r\n", "\n"), " \n\t")
	if err := c.check(); err != nil {
		return nil, err
	}
	return change(func(l *Library) error {
		if c.Name != old && l.command(c.Name) != nil {
			return fmt.Errorf("the library already has a slash command called %s", c.Name)
		}
		if old != "" {
			i := slices.IndexFunc(l.Commands, func(x *Command) bool { return x.Name == old })
			if i < 0 {
				return fmt.Errorf("no slash command called %s", old)
			}
			l.Commands = slices.Delete(l.Commands, i, i+1)
		}
		c.Agents = orNone(slices.Compact(slices.Sorted(slices.Values(c.Agents))))
		l.Commands = append(l.Commands, &c)
		return nil
	})
}

// CommandAgentsSet sets which agents get a slash command.
func CommandAgentsSet(name string, agents []string) (*Result, error) {
	return change(func(l *Library) error {
		c := l.command(name)
		if c == nil {
			return fmt.Errorf("no slash command called %s", name)
		}
		x := *c
		x.Agents = agents
		if err := x.check(); err != nil {
			return err
		}
		c.Agents = orNone(slices.Compact(slices.Sorted(slices.Values(agents))))
		return nil
	})
}

// RemoveCommands takes the slash commands named out of the library, and
// the files magpie wrote of them, still as it wrote them, out of every
// agent.
func RemoveCommands(names []string) (*Result, error) {
	if len(names) == 0 {
		return nil, errors.New("no slash commands to remove")
	}
	return change(func(l *Library) error {
		for _, n := range names {
			if l.command(n) == nil {
				return fmt.Errorf("no slash command called %s", n)
			}
		}
		l.Commands = slices.DeleteFunc(l.Commands, func(c *Command) bool { return slices.Contains(names, c.Name) })
		return nil
	})
}

// ---- the page --------------------------------------------------------------

// CommandView is a library slash command, and where each agent has it.
type CommandView struct {
	*Command
	// Files are, for each agent it was written to, the file
	Files map[string]string `json:"files,omitempty"`
	// Problems are the agents it couldn't be given to, and why
	Problems map[string]string `json:"problems,omitempty"`
}

// CommandAgentView is an agent the Slash commands tab lists.
type CommandAgentView struct {
	ID string `json:"id"`
	// Dir is where its commands are; No is why magpie can't give it any
	Dir string `json:"dir,omitempty"`
	No  string `json:"no,omitempty"`
	WSL bool   `json:"wsl,omitempty"`
	// Args: it takes $1, $2…; one without takes only $ARGUMENTS
	Args bool `json:"args,omitempty"`
}

// commandsView is the Slash commands page for these agents, and those in
// stopped WSL distros (asleep), greyed as while their distro runs.
func (l *Library) commandsView(targets []*Target, asleep []*agent.Agent, of func(string) map[string]string) ([]CommandView, []CommandAgentView) {
	agents := []CommandAgentView{}
	names := map[string]string{}
	for _, t := range targets {
		names[t.Agent.ID] = t.Agent.Name
		if base, _, _ := strings.Cut(t.Agent.ID, "@wsl:"); !slices.Contains(CommandAgents, base) {
			continue
		}
		sp, why := commandSpecOf(t)
		av := CommandAgentView{ID: t.Agent.ID, No: why, WSL: t.Agent.WSL != ""}
		if sp != nil {
			av.Dir, av.Args = sp.dir, sp.args
		}
		agents = append(agents, av)
	}
	for _, a := range asleep {
		if base, _, _ := strings.Cut(a.ID, "@wsl:"); slices.Contains(CommandAgents, base) && shapeOf(a) != nil {
			agents = append(agents, CommandAgentView{ID: a.ID, No: wslNoCommands, WSL: true})
		}
	}
	views := []CommandView{}
	for _, c := range l.Commands {
		x := *c
		x.Agents = orNone(x.Agents)
		cv := CommandView{Command: &x, Problems: of("command:" + c.Name)}
		for id, a := range l.Applied {
			for _, f := range a.Commands {
				if f.Name == c.Name && slices.Contains(c.Agents, id) {
					if cv.Files == nil {
						cv.Files = map[string]string{}
					}
					cv.Files[id] = f.Path
				}
			}
		}
		for _, av := range agents {
			if slices.Contains(c.Agents, av.ID) && cv.Problems[av.ID] == "" {
				why := av.No
				if why == "" && !av.Args && usesArgs(c.Prompt) {
					why = noArgs(names[av.ID]).Error()
				}
				if why == "" {
					continue
				}
				if cv.Problems == nil {
					cv.Problems = map[string]string{}
				}
				cv.Problems[av.ID] = why
			}
		}
		views = append(views, cv)
	}
	return views, agents
}

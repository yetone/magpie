package library

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
	"github.com/yetone/magpie/internal/lazyre"
)

// Slash commands the user already made in their agents, taken into the
// library (miantiao_me on X: "can the ones I defined in Pi be
// imported?"). Each agent the Slash commands tab gives commands to is
// read in the folder magpie writes them to (commandSpecOf), at its top
// only, as the agent reads it: Pi's prompts/*.md, Claude Code's
// commands/*.md, OpenCode's commands/*.md and command/*.md, Gemini CLI's
// commands/*.toml, Qwen Code's commands/*.md and *.toml, Droid's
// commands/*.md. The file's name is the command's; the description is
// the front matter's (Gemini's TOML description), the prompt the body
// (Gemini's prompt), with the agent's way of saying the words typed in
// put back as magpie's: Claude Code's $ARGUMENTS[n] and $n (counted from
// 0) as $n+1, Pi's $@ and Gemini's and Qwen's {{args}} as $ARGUMENTS. A
// file magpie wrote, still as it wrote it, is not one.
//
// Files of one name saying the same in several agents are one command
// for all of them; saying different things, they are listed apart, one
// to pick. A name the library has for a command saying the same gets the
// agent ticked; for another command, it is listed and can't be picked:
// the user's file stays, and the library's isn't written over it.
//
// An imported command keeps each file it came from as it was (Taken):
// while the command says what it did then, that file is what magpie
// writes for that agent, byte for byte, so the sync takes the user's own
// file as magpie's (syncOwnFiles) and nothing is written or doubled. An
// edit here writes the command in the agent's shape at the same place.

// TakenFile is an agent's own file a command was imported from.
type TakenFile struct {
	Path string `json:"path"`
	// Body is the file as it was
	Body string `json:"body"`
	// Hash is of the description and prompt read from it
	Hash string `json:"hash"`
}

// CommandProbe is the slash commands found in the agents.
type CommandProbe struct {
	Candidates []CommandCandidate `json:"candidates"`
	// Left are the files that can't be a command of the library's, with why
	Left []SubagentLeft `json:"left,omitempty"`
}

// CommandCandidate is one command found, in one agent or several.
type CommandCandidate struct {
	// Path is its first file's, which names it to the import
	Path        string `json:"path"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	// Agents are those it was found in, each with its file
	Agents []string          `json:"agents"`
	Files  map[string]string `json:"files"`
	// State is, against the library: "" a new one, "same" the library
	// has it saying the same (the import gives it to these agents too),
	// "taken" the library has another command by the name (not pickable)
	State string `json:"state,omitempty"`
	// Clash: another one found by this name says something else
	Clash bool `json:"clash,omitempty"`
	files map[string]*TakenFile
}

// CommandsImported is what an import did, by name.
type CommandsImported struct {
	Added []string `json:"added,omitempty"`
	// Joined are the library's own commands the agents were added to
	Joined []string `json:"joined,omitempty"`
}

const maxCommandFile = 1 << 20

// commandHash is what a command says.
func commandHash(description, prompt string) string {
	h := sha256.Sum256([]byte(description + "\x00" + prompt))
	return hex.EncodeToString(h[:8])
}

// commandReads are the folders and extensions an agent reads its commands
// from, beyond the one magpie writes to.
func commandReads(id string, sp *commandSpec) [][2]string {
	r := [][2]string{{sp.dir, sp.ext}}
	switch id {
	case "opencode":
		// it reads command/ as well (opencode's config.ts globs both)
		r = append(r, [2]string{filepath.Join(filepath.Dir(sp.dir), "command"), ".md"})
	case "qwen":
		// Qwen Code reads *.toml as Gemini CLI does, and *.md
		// (FileCommandLoader.ts)
		r = append(r, [2]string{sp.dir, ".toml"})
	}
	return r
}

// takenFor is the file c was imported from that magpie keeps for this
// agent: its place, and its bytes while c says what it did then.
func takenFor(c *Command, id string, sp *commandSpec) (path string, body []byte, ok bool) {
	k := c.Taken[id]
	if k == nil {
		return "", nil, false
	}
	in := false
	for _, r := range commandReads(id, sp) {
		if filepath.Dir(k.Path) == r[0] && filepath.Ext(k.Path) == r[1] && strings.TrimSuffix(filepath.Base(k.Path), r[1]) == c.Name {
			in = true
		}
	}
	if !in {
		return "", nil, false
	}
	if k.Hash == commandHash(c.Description, c.Prompt) {
		return k.Path, []byte(k.Body), true
	}
	if filepath.Ext(k.Path) == sp.ext {
		// changed here: written in the agent's shape at the same place
		return k.Path, nil, true
	}
	return "", nil, false
}

// claudeBackRe is a word typed in as Claude Code says it.
var claudeBackRe = lazyre.New(`\\\$([0-9]+)|\$ARGUMENTS\[([0-9]+)\]|\$([0-9]+)`)

// claudeBack is a Claude Code prompt as magpie's: $ARGUMENTS[n] and $n,
// counted from 0, are $n+1, and the \$0 claudeArgs writes is $0.
func claudeBack(p string) string {
	return claudeBackRe.ReplaceAllStringFunc(p, func(m string) string {
		g := claudeBackRe.FindStringSubmatch(m)
		if g[1] != "" {
			if g[1] == "0" {
				return "$0"
			}
			return m // a $ the user kept from being a word
		}
		n, err := strconv.Atoi(g[2] + g[3])
		if err != nil {
			return m
		}
		return "$" + strconv.Itoa(n+1)
	})
}

// piAllRe is Pi's $@, every word typed in; ${@:…} is left as Pi's own
var piAllRe = lazyre.New(`\$@`)

// commandBack is a prompt read from an agent's file, as magpie says it.
func commandBack(id, p string) string {
	switch id {
	case "claude":
		return claudeBack(p)
	case "pi":
		return piAllRe.ReplaceAllString(p, "$$ARGUMENTS")
	case "gemini", "qwen":
		return strings.ReplaceAll(p, "{{args}}", "$ARGUMENTS")
	}
	return p
}

// readCommandAt is the command in one of an agent's files; nil with no
// why for a file that isn't one.
func readCommandAt(id, f, ext string) (*Command, string, []byte) {
	fi, err := os.Stat(f)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, "", nil
	}
	if fi.Size() > maxCommandFile {
		return nil, "the file is over 1 MB", nil
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return nil, err.Error(), nil
	}
	name := strings.TrimSuffix(filepath.Base(f), ext)
	if !commandNameRe.MatchString(name) {
		return nil, "magpie names a command with lowercase letters, digits, - and _ only", nil
	}
	var desc, prompt string
	if ext == ".toml" {
		var t struct {
			Description string `toml:"description"`
			Prompt      string `toml:"prompt"`
		}
		if err := toml.Unmarshal(b, &t); err != nil {
			return nil, "it isn't TOML magpie can read", nil
		}
		desc, prompt = t.Description, t.Prompt
	} else {
		front, body := splitFront(b)
		desc, prompt = plStr(front["description"]), body
	}
	desc = strings.Join(strings.Fields(desc), " ")
	if utf8.RuneCountInString(desc) > maxCommandDescription {
		desc = string([]rune(desc)[:maxCommandDescription-1]) + "…"
	}
	// as SaveCommand keeps a prompt, for an unchanged save to keep it
	prompt = strings.ReplaceAll(prompt, "\r\n", "\n")
	prompt = strings.TrimRight(strings.TrimLeft(commandBack(id, prompt), "\n"), " \n\t")
	c := &Command{Name: name, Description: desc, Prompt: prompt, Agents: []string{id}}
	if err := c.check(); err != nil {
		return nil, err.Error(), nil
	}
	return c, "", b
}

// findCommands reads the agents' own slash commands, against l.
func (l *Library) findCommands(targets []*Target) *CommandProbe {
	p := &CommandProbe{Candidates: []CommandCandidate{}}
	type found struct {
		c    *Command
		id   string
		path string
		body []byte
	}
	var all []found
	seen := map[string]bool{}
	for _, t := range targets {
		id := t.Agent.ID
		if !slices.Contains(CommandAgents, id) {
			continue
		}
		sp, _ := commandSpecOf(t)
		if sp == nil {
			continue
		}
		var mine []SubagentFile
		if a := l.Applied[id]; a != nil {
			mine = a.Commands
		}
		for _, r := range commandReads(id, sp) {
			ents, err := os.ReadDir(r[0])
			if err != nil {
				continue
			}
			for _, e := range ents {
				if e.IsDir() || !strings.HasSuffix(e.Name(), r[1]) {
					continue
				}
				f := filepath.Join(r[0], e.Name())
				if seen[id+"\x00"+f] {
					continue
				}
				seen[id+"\x00"+f] = true
				c, why, b := readCommandAt(id, f, r[1])
				if c == nil {
					if why != "" {
						p.Left = append(p.Left, SubagentLeft{Path: f, Error: why})
					}
					continue
				}
				if slices.ContainsFunc(mine, func(m SubagentFile) bool { return m.Path == f && m.Hash == subagentHash(b) }) {
					continue // magpie's own, as it wrote it
				}
				all = append(all, found{c, id, f, b})
			}
		}
	}
	// one candidate for each name and what it says, in the order the
	// page lists the agents
	order := func(id string) int { return slices.Index(CommandAgents, id) }
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].c.Name != all[j].c.Name {
			return all[i].c.Name < all[j].c.Name
		}
		if order(all[i].id) != order(all[j].id) {
			return order(all[i].id) < order(all[j].id)
		}
		return all[i].path < all[j].path
	})
	for _, f := range all {
		h := commandHash(f.c.Description, f.c.Prompt)
		i := slices.IndexFunc(p.Candidates, func(x CommandCandidate) bool {
			return x.Name == f.c.Name && commandHash(x.Description, x.Prompt) == h
		})
		if i >= 0 && p.Candidates[i].files[f.id] != nil {
			// a second file of this agent (OpenCode's command/ and
			// commands/): the first is the one
			continue
		}
		if i < 0 {
			p.Candidates = append(p.Candidates, CommandCandidate{Path: f.path, Name: f.c.Name, Description: f.c.Description, Prompt: f.c.Prompt,
				Agents: []string{}, Files: map[string]string{}, files: map[string]*TakenFile{}})
			i = len(p.Candidates) - 1
		}
		x := &p.Candidates[i]
		x.Agents = append(x.Agents, f.id)
		x.Files[f.id] = f.path
		x.files[f.id] = &TakenFile{Path: f.path, Body: string(f.body), Hash: h}
	}
	for i := range p.Candidates {
		x := &p.Candidates[i]
		for j := range p.Candidates {
			if i != j && p.Candidates[j].Name == x.Name {
				x.Clash = true
			}
		}
		if c := l.command(x.Name); c != nil {
			x.State = "taken"
			if commandHash(c.Description, c.Prompt) == commandHash(x.Description, x.Prompt) {
				x.State = "same"
			}
		}
	}
	sort.Slice(p.Left, func(i, j int) bool { return p.Left[i].Path < p.Left[j].Path })
	return p
}

// ProbeCommands is the slash commands the agents here have of their own;
// nothing is written.
func ProbeCommands() (*CommandProbe, error) {
	l, err := loadLocked()
	if err != nil {
		return nil, err
	}
	return l.findCommands(Targets()), nil
}

// ImportCommands takes the commands found at those paths (each one's
// first file, as ProbeCommands lists it) into the library, for the agents
// they were found in.
func ImportCommands(paths []string) (*Result, error) {
	if len(paths) == 0 {
		return nil, errors.New("no slash commands to import")
	}
	done := &CommandsImported{}
	res, err := change(func(l *Library) error {
		*done = CommandsImported{}
		p := l.findCommands(Targets())
		var picked []*CommandCandidate
		for _, want := range paths {
			i := slices.IndexFunc(p.Candidates, func(c CommandCandidate) bool { return c.Path == want })
			if i < 0 {
				return fmt.Errorf("no slash command at %s any more", want)
			}
			x := &p.Candidates[i]
			if x.State == "taken" {
				return fmt.Errorf("the library has another slash command called %s", x.Name)
			}
			if slices.ContainsFunc(picked, func(y *CommandCandidate) bool { return y.Name == x.Name }) {
				return fmt.Errorf("two different slash commands called %s: pick one", x.Name)
			}
			picked = append(picked, x)
		}
		for _, x := range picked {
			c := l.command(x.Name)
			if c == nil {
				c = &Command{Name: x.Name, Description: x.Description, Prompt: x.Prompt, Agents: []string{}}
				l.Commands = append(l.Commands, c)
				done.Added = append(done.Added, x.Name)
			} else {
				done.Joined = append(done.Joined, x.Name)
			}
			if c.Taken == nil {
				c.Taken = map[string]*TakenFile{}
			}
			for id, f := range x.files {
				c.Taken[id] = f
			}
			c.Agents = slices.Compact(slices.Sorted(slices.Values(append(c.Agents, x.Agents...))))
		}
		return nil
	})
	if res != nil {
		res.Commands = done
	}
	return res, err
}

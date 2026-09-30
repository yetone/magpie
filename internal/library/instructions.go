package library

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/edit"
)

// The part of an agent's instructions file magpie writes sits between
// these two lines; the rest of the file is the user's.
const (
	blockBegin = "<!-- magpie:begin · written by magpie from its Library -->"
	blockEnd   = "<!-- magpie:end -->"
)

// defaultSet is the set magpie had before there were more: its text is
// where the one shared text always was.
const defaultSet = "default"

// setPath is where a set's text is kept.
func setPath(id string) string {
	if id == "" || id == defaultSet {
		return filepath.Join(Dir(), "instructions.md")
	}
	return filepath.Join(Dir(), "instruction-sets", id+".md")
}

// sets are the sets there are, the default first.
func (l *Library) sets() []InstrSet {
	out := []InstrSet{{ID: defaultSet}}
	for _, s := range l.Instructions.Sets {
		if s.ID == defaultSet {
			out[0] = s
		} else {
			out = append(out, s)
		}
	}
	return out
}

func (l *Library) instrSet(id string) *InstrSet {
	for i := range l.Instructions.Sets {
		if l.Instructions.Sets[i].ID == id {
			return &l.Instructions.Sets[i]
		}
	}
	return nil
}

func (l *Library) hasSet(id string) bool { return id == defaultSet || l.instrSet(id) != nil }

// active is the set the agents get.
func (l *Library) active() string {
	if l.hasSet(l.Instructions.Active) {
		return l.Instructions.Active
	}
	return defaultSet
}

// sharedPath is where the text the agents get is kept.
func (l *Library) sharedPath() string { return setPath(l.active()) }

func extraPath(agent string) string {
	return filepath.Join(Dir(), "instructions", agent+".md")
}

func readText(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n"))
}

func writeText(path, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return edit.WriteAtomic(path, []byte(text+"\n"))
}

// wanted is what magpie writes into the agent's file: the shared text and
// the agent's own additions after it; "" for nothing.
func (l *Library) wanted(agent string) string {
	if !slices.Contains(l.Instructions.Agents, agent) {
		return ""
	}
	parts := []string{}
	for _, s := range []string{readText(l.sharedPath()), readText(extraPath(agent))} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

// split takes a file apart: what is before magpie's part, magpie's part,
// and what is after it. ok is false when magpie has no part in it.
func split(file string) (before, block, after string, ok bool) {
	i := strings.Index(file, blockBegin)
	if i < 0 {
		return file, "", "", false
	}
	rest := file[i+len(blockBegin):]
	j := strings.Index(rest, blockEnd)
	if j < 0 {
		return file, "", "", false
	}
	return file[:i], strings.TrimSpace(rest[:j]), rest[j+len(blockEnd):], true
}

// own is the file without magpie's part: the user's own instructions.
func own(file string) string {
	before, _, after, _ := split(file)
	return strings.TrimSpace(strings.TrimSpace(before) + "\n\n" + strings.TrimSpace(after))
}

// withBlock is the file with magpie's part set to text, or taken out for "".
// A new part goes after what the user has written.
func withBlock(file, text string) string {
	file = strings.ReplaceAll(file, "\r\n", "\n")
	before, _, after, ok := split(file)
	before, after = strings.TrimRight(before, "\n"), strings.TrimLeft(after, "\n")
	block := ""
	if text != "" {
		block = blockBegin + "\n" + text + "\n" + blockEnd
	}
	if !ok {
		before, after = strings.TrimRight(file, "\n"), ""
	}
	var parts []string
	for _, p := range []string{before, block, strings.TrimRight(after, "\n")} {
		if strings.TrimSpace(p) != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// syncInstructions writes the agent's part as the library has it. A part
// the user edited in the file is left alone until the library's text for
// that agent changes; then it is replaced, the file kept aside first.
func (l *Library) syncInstructions(t *Target, b *backups, res *Result) {
	if t.Instructions == "" {
		return
	}
	id := t.Agent.ID
	a := l.applied(id)
	want := l.wanted(id)
	raw, err := edit.Read(t.Instructions)
	if err != nil {
		res.fail(id, "instructions", err)
		return
	}
	file := string(raw)
	_, have, _, ok := split(file)
	if want == "" && !ok {
		a.Instructions, a.InstrHash = false, ""
		return
	}
	if ok && have == want {
		a.Instructions, a.InstrHash = want != "", hash(want)
		return
	}
	if ok && want != "" && a.InstrHash != "" && hash(have) != a.InstrHash && hash(want) == a.InstrHash {
		return // edited in the file since, and the library hasn't changed for it
	}
	next := withBlock(file, want)
	if err := b.keep(id, t.Instructions); err != nil {
		res.fail(id, "instructions", err)
		return
	}
	if strings.TrimSpace(next) == "" && len(raw) > 0 && own(file) == "" {
		err = edit.Remove(t.Instructions)
	} else if next != "" {
		err = edit.WriteAtomic(t.Instructions, []byte(next))
	}
	if err != nil {
		res.fail(id, "instructions", err)
		return
	}
	a.Instructions, a.InstrHash = want != "", hash(want)
	res.changed(id)
}

// AgentInstructions is one agent's instructions file as the page shows it.
type AgentInstructions struct {
	Agent    string `json:"agent"`
	Name     string `json:"name"`
	Icon     string `json:"icon"`
	Path     string `json:"path"`
	On       bool   `json:"on"`
	Extra    string `json:"extra"`              // what this agent gets besides
	Own      int    `json:"own"`                // lines of the user's own in the file
	Edited   bool   `json:"edited,omitempty"`   // magpie's part was changed in the file
	Override string `json:"override,omitempty"` // a file the agent reads instead
}

// InstructionsView is the Instructions page.
type InstructionsView struct {
	Shared string              `json:"shared"` // the set on's text
	Sets   []SetView           `json:"sets"`
	Agents []AgentInstructions `json:"agents"`
}

// SetView is one set of shared instructions as the page shows it.
type SetView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Text   string `json:"text"`
	Active bool   `json:"active,omitempty"`
}

// ReadInstructions is the shared text and each agent's file.
func ReadInstructions() (*InstructionsView, error) {
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return nil, err
	}
	v := &InstructionsView{Shared: readText(l.sharedPath()), Agents: []AgentInstructions{}}
	for _, s := range l.sets() {
		v.Sets = append(v.Sets, SetView{ID: s.ID, Name: s.Name, Text: readText(setPath(s.ID)), Active: s.ID == l.active()})
	}
	for _, t := range Targets() {
		if t.Instructions == "" {
			continue
		}
		id := t.Agent.ID
		ai := AgentInstructions{Agent: id, Name: t.Agent.Name, Icon: t.Agent.Icon, Path: t.Instructions,
			On: slices.Contains(l.Instructions.Agents, id), Extra: readText(extraPath(id))}
		file := readText(t.Instructions)
		if o := own(file); o != "" {
			ai.Own = strings.Count(o, "\n") + 1
		}
		if _, have, _, ok := split(file); ok && have != l.wanted(id) {
			ai.Edited = true
		}
		if t.Override != "" && readText(t.Override) != "" {
			ai.Override = t.Override
		}
		v.Agents = append(v.Agents, ai)
	}
	return v, nil
}

// InstructionsChange is what the page saves: the shared text, which agents
// get it, and what each gets besides. A nil field is left as it is.
type InstructionsChange struct {
	Shared *string            `json:"shared,omitempty"`
	Agents []string           `json:"agents,omitempty"`
	Extra  map[string]*string `json:"extra,omitempty"`
	// Rewrite writes magpie's part again into these agents' files, over
	// edits made there.
	Rewrite []string `json:"rewrite,omitempty"`
	// The sets: texts by set, a set to switch the agents to, one to add
	// (id and name), one to rename and one to remove. Shared is the text
	// of the set on, as it is after Activate.
	Texts    map[string]*string `json:"texts,omitempty"`
	Activate string             `json:"activate,omitempty"`
	Create   *InstrSet          `json:"create,omitempty"`
	Rename   *InstrSet          `json:"rename,omitempty"`
	Remove   string             `json:"remove,omitempty"`
}

// SaveInstructions saves the change and writes it into the agents.
func SaveInstructions(c InstructionsChange) (*Result, error) {
	return change(func(l *Library) error {
		if err := l.changeSets(c); err != nil {
			return err
		}
		if c.Shared != nil {
			if err := writeText(l.sharedPath(), *c.Shared); err != nil {
				return err
			}
		}
		if c.Agents != nil {
			l.Instructions.Agents = slices.Sorted(slices.Values(c.Agents))
		}
		for id, x := range c.Extra {
			if x == nil {
				continue
			}
			if err := checkName("agent", id); err != nil {
				return err
			}
			if err := writeText(extraPath(id), *x); err != nil {
				return err
			}
		}
		for _, id := range c.Rewrite {
			l.applied(id).InstrHash = ""
		}
		return nil
	})
}

// ImportInstructions moves the user's own instructions out of an agent's
// file into the library: they are added to the shared text, taken out of
// the file (which is kept aside first), and the agent gets the shared text
// from then on — so what it reads is what it read before.
func ImportInstructions(id string) (*Result, error) {
	t := targetByID(id)
	if t == nil || t.Instructions == "" {
		return nil, fmt.Errorf("%s has no instructions file magpie knows", id)
	}
	return change(func(l *Library) error {
		raw, err := edit.Read(t.Instructions)
		if err != nil {
			return err
		}
		file := strings.ReplaceAll(string(raw), "\r\n", "\n")
		mine := own(file)
		if mine == "" {
			return fmt.Errorf("%s has nothing of its own to import", t.Instructions)
		}
		shared := readText(l.sharedPath())
		if !strings.Contains(shared, mine) {
			shared = strings.TrimSpace(shared + "\n\n" + mine)
		}
		if err := writeText(l.sharedPath(), shared); err != nil {
			return err
		}
		b := newBackups()
		if err := b.keep(id, t.Instructions); err != nil {
			return err
		}
		_, block, _, ok := split(file)
		next := ""
		if ok {
			next = withBlock("", block)
		}
		if next == "" {
			err = edit.Remove(t.Instructions)
		} else {
			err = edit.WriteAtomic(t.Instructions, []byte(next))
		}
		if err != nil {
			return err
		}
		l.Instructions.Agents = set(l.Instructions.Agents, id, true)
		l.applied(id).InstrHash = "" // what's there now isn't what the library says
		return nil
	})
}

// changeSets adds, renames, fills, removes and switches the sets as c says.
func (l *Library) changeSets(c InstructionsChange) error {
	if x := c.Create; x != nil {
		if err := checkName("set", x.ID); err != nil {
			return err
		}
		if l.hasSet(x.ID) {
			return fmt.Errorf("there is a set %q already", x.ID)
		}
		if strings.TrimSpace(x.Name) == "" {
			return errors.New("a new set needs a name")
		}
		l.Instructions.Sets = append(l.Instructions.Sets, InstrSet{ID: x.ID, Name: strings.TrimSpace(x.Name)})
	}
	if x := c.Rename; x != nil {
		if !l.hasSet(x.ID) {
			return fmt.Errorf("there is no set %q", x.ID)
		}
		if s := l.instrSet(x.ID); s != nil {
			s.Name = strings.TrimSpace(x.Name)
		} else {
			l.Instructions.Sets = append([]InstrSet{{ID: x.ID, Name: strings.TrimSpace(x.Name)}}, l.Instructions.Sets...)
		}
	}
	for id, text := range c.Texts {
		if text == nil {
			continue
		}
		if !l.hasSet(id) {
			return fmt.Errorf("there is no set %q", id)
		}
		if err := writeText(setPath(id), *text); err != nil {
			return err
		}
	}
	if id := c.Remove; id != "" {
		switch {
		case id == defaultSet:
			return errors.New("the first set can be renamed and emptied, not removed")
		case !l.hasSet(id):
			return fmt.Errorf("there is no set %q", id)
		case id == l.active():
			return errors.New("the set the agents get can't be removed: switch them to another first")
		}
		if err := writeText(setPath(id), ""); err != nil {
			return err
		}
		l.Instructions.Sets = slices.DeleteFunc(l.Instructions.Sets, func(s InstrSet) bool { return s.ID == id })
	}
	if id := c.Activate; id != "" {
		if !l.hasSet(id) {
			return fmt.Errorf("there is no set %q", id)
		}
		l.Instructions.Active = id
		if id == defaultSet {
			l.Instructions.Active = ""
		}
	}
	return nil
}

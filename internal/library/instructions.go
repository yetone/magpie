package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/agent"
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
func (l *Library) sharedPath() string { return l.textPath(l.active()) }

// textPath is where a set's text is kept: the user's own file, for a set
// linked to one, or setPath.
func (l *Library) textPath(id string) string {
	if s := l.instrSet(id); s != nil && s.File != "" {
		return s.File
	}
	return setPath(id)
}

// linked reports whether p is a file of the user's a set is linked to.
func (l *Library) linked(p string) bool {
	return slices.ContainsFunc(l.Instructions.Sets, func(s InstrSet) bool { return s.File != "" && s.File == p })
}

// putText writes a set's or an agent's text to p. A file of the user's a
// set is linked to is kept aside first and written, never removed: an
// emptied set leaves it empty, and a text it has already is no write.
func (l *Library) putText(p, text string) error {
	if !l.linked(p) {
		return writeText(p, text)
	}
	text = strings.TrimSpace(text)
	if readText(p) == text {
		return nil
	}
	if l.kept == nil {
		l.kept = newBackups()
	}
	if err := l.kept.keep("library", p); err != nil {
		return err
	}
	if text != "" {
		text += "\n"
	}
	return edit.WriteAtomic(p, []byte(text))
}

func extraPath(agent string) string {
	return filepath.Join(Dir(), "instructions", fileName(agent)+".md")
}

// setText is a set's text as kept at p. Magpie's part is left out of it:
// that part is what magpie wrote from the set, never the set. A set kept in
// the file an agent reads (instructions.md linked to ~/.claude/CLAUDE.md, or
// both linked to one file of the user's, #2349) has one, and read with it
// every write put the whole file into its own part again, so the file
// doubled at each one, to gigabytes.
func setText(p string) string { return own(readText(p)) }

// isSetFile reports whether p, links followed, is the file the agents' set
// is kept in.
func (l *Library) isSetFile(p string) bool {
	return p != "" && realBelow(p) == realBelow(l.sharedPath())
}

func readText(path string) string {
	b, _ := os.ReadFile(path)
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\r\n", "\n"))
}

func writeText(path, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		if err := edit.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
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
	for _, s := range []string{setText(l.sharedPath()), readText(extraPath(agent))} {
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
// Every part goes, and an end line left over: a part written into itself
// (#2349) holds others, each ended after the first end.
func own(file string) string {
	for {
		before, _, after, ok := split(file)
		if !ok {
			break
		}
		file = strings.TrimSpace(before) + "\n\n" + strings.TrimSpace(after)
	}
	if strings.Contains(file, blockEnd) {
		lines := strings.Split(file, "\n")
		lines = slices.DeleteFunc(lines, func(s string) bool { return strings.TrimSpace(s) == blockEnd })
		file = strings.Join(lines, "\n")
		for strings.Contains(file, "\n\n\n") {
			file = strings.ReplaceAll(file, "\n\n\n", "\n\n")
		}
	}
	return strings.TrimSpace(file)
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

// unreadable is the error reading a text of the library's gives, nil
// when it reads or isn't there at all.
func unreadable(p string) error {
	if _, err := os.ReadFile(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// instructionsLost says whether the library has no instructions to give
// anyone: no agent ticked, or no shared text. To an automatic write that
// is a library that lost them, as an upgrade's reset leaves one, not a
// take-back the user asked for, which the page writes itself (#5709).
func (l *Library) instructionsLost() bool {
	return len(l.Instructions.Agents) == 0 || setText(l.sharedPath()) == ""
}

// syncInstructions writes the agent's part as the library has it. A part
// the user edited in the file is left alone until the library's text for
// that agent changes; then it is replaced, the file kept aside first. An
// automatic write (auto: a sync, at a start or beside a change) that
// finds the library with nothing to give leaves a part where it is, and
// says so: a library reading empty has lost its instructions, not revoked
// them, and the parts out there are then the only copy left (#5709, where
// a start-up apply with an empty Library wiped nine agents' files). It is
// a write of the page — a take-back, made with the library as the user
// left it — that takes a part out.
func (l *Library) syncInstructions(t *Target, b *backups, res *Result, auto bool) {
	if res.unreached(t) {
		return
	}
	if t.Instructions == "" {
		return
	}
	id := t.Agent.ID
	a := l.applied(id)
	if slices.Contains(l.Instructions.Agents, id) {
		// a text that can't be read is unknown, not empty: writing what
		// is left would take the agent's part out or cut it down (#5709)
		for _, p := range []string{l.sharedPath(), extraPath(id)} {
			if err := unreadable(p); err != nil {
				res.fail(id, "instructions", err)
				return
			}
		}
	}
	want := l.wanted(id)
	raw, err := edit.Read(t.Instructions)
	if err != nil {
		res.fail(id, "instructions", err)
		return
	}
	file := string(raw)
	if l.isSetFile(t.Instructions) {
		// the agent reads the set's own file, so it has the text already:
		// magpie writes no part into it, and takes out one written before
		a.Instructions, a.InstrHash = false, ""
		file = strings.ReplaceAll(file, "\r\n", "\n")
		if !strings.Contains(file, blockBegin) && !strings.Contains(file, blockEnd) {
			return
		}
		if err := b.keep(id, t.Instructions); err != nil {
			res.fail(id, "instructions", err)
			return
		}
		next := own(file)
		if next != "" {
			next += "\n"
		}
		if err := edit.WriteAtomic(t.Instructions, []byte(next)); err != nil {
			res.fail(id, "instructions", err)
			return
		}
		res.changed(id)
		return
	}
	_, have, _, ok := split(file)
	if want == "" && !ok {
		a.Instructions, a.InstrHash = false, ""
		return
	}
	if ok && have == want {
		a.Instructions, a.InstrHash = want != "", hash(want)
		return
	}
	if auto && want == "" && ok && l.instructionsLost() {
		// An apply with nothing to give takes no part out (#5709): the
		// library's text itself gone, or no agent ticked, reads empty to
		// every agent at once, as an upgrade that lost them does (the
		// reporter's library.json had both), and the parts out there are
		// then the only copy of the instructions there is. A text the
		// library still holds with one agent unticked is another thing:
		// that take-back goes out as usual, and a write of the page — a
		// take-back, made with the library as the user left it — empties
		// a part as it goes. Left alone, and said so; the part comes back
		// with the library's next text.
		red := ""
		if a.InstrHash == hash("") {
			red = "; the last write on record hashes empty, a library reset under the agents"
		}
		log.Printf("library: %s: nothing to write, %s still holds magpie's part%s: left alone", id, t.Instructions, red)
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
		if auto {
			// an apply never takes a whole file away (#5709): what it
			// empties it empties in place, the file staying for the
			// agent that reads it
			err = edit.WriteAtomic(t.Instructions, nil)
		} else {
			err = edit.Remove(t.Instructions)
		}
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
	Profile  bool   `json:"profile,omitempty"`  // the file is the user's profile to the agent (Target.Profile)
	// Asleep is the WSL distro, not running now, the agent is in: its row
	// is what magpie last knew of it (asleepInstructions), its file isn't
	// read, and a change is written there when the distro next runs.
	// Unseen: magpie never saw where it keeps its instructions
	Asleep string `json:"asleep,omitempty"`
	Unseen bool   `json:"unseen,omitempty"`
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
	File   string `json:"file,omitempty"` // the user's own file it is kept in
}

// ReadInstructions is the shared text and each agent's file.
func ReadInstructions() (*InstructionsView, error) {
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return nil, err
	}
	return l.instructionsView(targetsAsleep(false)), nil
}

// instructionsView is the Instructions page for these agents, and those in
// stopped WSL distros (asleep). They are passed in: finding them looks
// through every agent's folders, in WSL too, which one read of the Library
// did three times over (#1624).
func (l *Library) instructionsView(targets []*Target, asleep []*agent.Agent) *InstructionsView {
	v := &InstructionsView{Shared: setText(l.sharedPath()), Agents: []AgentInstructions{}}
	for _, s := range l.sets() {
		v.Sets = append(v.Sets, SetView{ID: s.ID, Name: s.Name, Text: setText(l.textPath(s.ID)), Active: s.ID == l.active(), File: s.File})
	}
	for _, t := range targets {
		if t.Instructions == "" {
			continue
		}
		id := t.Agent.ID
		ai := AgentInstructions{Agent: id, Name: t.Agent.Name, Icon: t.Agent.Icon, Path: t.Instructions,
			On: slices.Contains(l.Instructions.Agents, id), Extra: readText(extraPath(id)), Profile: t.Profile}
		file := readText(t.Instructions)
		if o := own(file); o != "" {
			ai.Own = strings.Count(o, "\n") + 1
		}
		if _, have, _, ok := split(file); ok && have != l.wanted(id) && !l.isSetFile(t.Instructions) {
			ai.Edited = true
		}
		if t.Override != "" && readText(t.Override) != "" {
			ai.Override = t.Override
		}
		v.Agents = append(v.Agents, ai)
	}
	// a stopped WSL distro's agents, by what magpie last knew of them:
	// nothing of theirs is opened, which would start the distro (#3896)
	for _, a := range asleep {
		if ai, ok := l.asleepInstructions(a); ok {
			v.Agents = append(v.Agents, ai)
		}
	}
	return v
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

// changeInstructions is change for the instructions alone: it writes them
// into the agents and nothing else. Through change, a set made, saved or
// removed gave every agent its MCP servers and skills again too, looking
// through every skill folder, which took up to a minute a click on Windows
// (#1624, Hipple).
func changeInstructions(f func(l *Library) error) (*Result, error) {
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
	res := &Result{Changed: []string{}, Problems: []Problem{}}
	b := l.kept
	if b == nil {
		b = newBackups()
	}
	for _, t := range Targets() {
		l.syncInstructions(t, b, res, false)
	}
	res.Backup = b.dir
	if b.dir != "" {
		pruneBackups()
	}
	return res, l.save()
}

// SaveInstructions saves the change and writes it into the agents.
func SaveInstructions(c InstructionsChange) (*Result, error) {
	return changeInstructions(func(l *Library) error {
		if err := l.changeSets(c); err != nil {
			return err
		}
		if c.Shared != nil {
			if err := l.putText(l.sharedPath(), *c.Shared); err != nil {
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
			if err := checkAgent(id); err != nil {
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
	if t.Profile {
		// moved out, the profile would go into every agent's file, and the
		// agent would lose who its owner is (Alma's USER.md, #2315)
		return nil, fmt.Errorf("%s is %s's profile of its owner, not instructions alone: what is in it stays there", t.Instructions, t.Agent.Name)
	}
	return changeInstructions(func(l *Library) error {
		if l.isSetFile(t.Instructions) {
			return fmt.Errorf("%s is the file the instructions set is kept in already", t.Instructions)
		}
		raw, err := edit.Read(t.Instructions)
		if err != nil {
			return err
		}
		file := strings.ReplaceAll(string(raw), "\r\n", "\n")
		mine := own(file)
		if mine == "" {
			return fmt.Errorf("%s has nothing of its own to import", t.Instructions)
		}
		shared := setText(l.sharedPath())
		if !strings.Contains(shared, mine) {
			shared = strings.TrimSpace(shared + "\n\n" + mine)
		}
		if err := l.putText(l.sharedPath(), shared); err != nil {
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
		file := ""
		if x.File != "" {
			var err error
			if file, err = l.linkable(x.File); err != nil {
				return err
			}
		}
		l.Instructions.Sets = append(l.Instructions.Sets, InstrSet{ID: x.ID, Name: strings.TrimSpace(x.Name), File: file})
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
		if err := l.putText(l.textPath(id), *text); err != nil {
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
		// a linked file is the user's: the set lets go of it, and it stays
		if l.instrSet(id).File == "" {
			if err := writeText(setPath(id), ""); err != nil {
				return err
			}
		}
		l.Instructions.Sets = slices.DeleteFunc(l.Instructions.Sets, func(s InstrSet) bool { return s.ID == id })
	}
	// an empty set in use gives the agents nothing, so a set added, or one
	// given text, is the one they read from then on (Fate on Discord: a new
	// set was written into no agent while the Default set read was empty)
	if c.Activate == "" && c.Shared == nil && setText(l.sharedPath()) == "" {
		if x := c.Create; x != nil {
			l.Instructions.Active = x.ID
		}
		for _, id := range slices.Sorted(maps.Keys(c.Texts)) {
			if x := c.Texts[id]; x != nil && strings.TrimSpace(*x) != "" && id != l.active() {
				l.Instructions.Active = id
				break
			}
		}
		if l.Instructions.Active == defaultSet {
			l.Instructions.Active = ""
		}
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

// linkable is the file a new set can be linked to, made absolute, or why
// not: a file magpie writes into (an agent's own instructions, where the
// set would be read from the text written from it), one of the library's
// own, a folder, or one another set has.
func (l *Library) linkable(file string) (string, error) {
	file = strings.TrimSpace(file)
	if rest, ok := strings.CutPrefix(file, "~"); ok && (rest == "" || rest[0] == '/' || rest[0] == filepath.Separator) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		file = home + rest
	}
	if !filepath.IsAbs(file) {
		return "", fmt.Errorf("%s: give the file's full path", file)
	}
	file = filepath.Clean(file)
	real := realBelow(file)
	if st, err := os.Stat(file); err == nil && st.IsDir() {
		return "", fmt.Errorf("%s is a folder: give a file", file)
	}
	if strings.Contains(readText(file), blockBegin) {
		return "", fmt.Errorf("magpie writes its instructions into %s: a set can't be kept there too", file)
	}
	if lib := realBelow(Dir()); real == lib || strings.HasPrefix(real, lib+string(filepath.Separator)) || real == realBelow(path()) {
		return "", fmt.Errorf("%s is the library's own", file)
	}
	for _, s := range l.Instructions.Sets {
		if s.File != "" && realBelow(s.File) == real {
			return "", fmt.Errorf("the set %q is kept in %s already", s.Name, file)
		}
	}
	for _, t := range everyTarget() {
		for _, p := range []string{t.Instructions, t.Override} {
			if p != "" && realBelow(p) == real {
				return "", fmt.Errorf("%s is %s's own instructions, which magpie writes into: a set can't be kept there", file, t.Agent.Name)
			}
		}
	}
	return file, nil
}

// linkSeen is the linked file SyncLinked last looked at, as it was then.
var linkSeen struct {
	sync.Mutex
	file string
	mod  time.Time
	size int64
}

// SyncLinked writes the instructions into the agents again when the set
// they get is kept in a file of the user's that changed since it was last
// looked at — edited by hand, or by another tool that keeps it — and not
// otherwise: nil, nil. The first look at a file only notes it.
func SyncLinked() (*Result, error) {
	mu.Lock()
	l, err := load()
	file := ""
	if err == nil {
		if s := l.instrSet(l.active()); s != nil {
			file = s.File
		}
	}
	mu.Unlock()
	if err != nil {
		return nil, err
	}
	var mod time.Time
	var size int64 = -1
	if st, err := os.Stat(file); file != "" && err == nil {
		mod, size = st.ModTime(), st.Size()
	}
	linkSeen.Lock()
	changed := file != "" && linkSeen.file == file && (!linkSeen.mod.Equal(mod) || linkSeen.size != size)
	linkSeen.file, linkSeen.mod, linkSeen.size = file, mod, size
	linkSeen.Unlock()
	if !changed {
		return nil, nil
	}
	return changeInstructions(func(*Library) error { return nil })
}

// KeepLinked runs SyncLinked, and Woken, every interval, until ctx ends:
// the app's backend, which ends once it has handed the gateway to the
// new version (gui/handover.go). Before, it ran for as long as the
// process did, and the copy an update left finishing its requests went
// on writing the agents' files beside the new one (#4938).
func KeepLinked(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for ctx.Err() == nil {
		if res, err := SyncLinked(); err != nil {
			log.Println("library: linked instructions:", err)
		} else if res != nil {
			for _, p := range res.Problems {
				log.Println("library: linked instructions:", p.Agent, p.What, p.Error)
			}
		}
		// what was ticked for a stopped WSL distro's agents, once it runs
		keepWoken()
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}
}

package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func init() {
	gateway.SharedLibrary = func() ([]byte, error) {
		b, err := Shared()
		if err != nil {
			return nil, err
		}
		return json.Marshal(b)
	}
}

// borrowEvery is how often KeepLinked reads the Remote magpies' Libraries.
const borrowEvery = 10 * time.Minute

var lastBorrow time.Time

// keepBorrowed reads the Libraries of the Remote magpies switched on,
// each borrowEvery.
func keepBorrowed(ctx context.Context) {
	if time.Since(lastBorrow) < borrowEvery {
		return
	}
	lastBorrow = time.Now()
	UseRemoteLibraries(ctx)
}

// A Remote magpie's Library, used here (KoiLx0 on X: prompts and skills
// kept on one computer, not set up again on every client). The remote
// tells its skills and instruction sets (Shared, GET
// provider.RemoteLibraryPath); with the editor's switch on, this
// magpie keeps them as items of its own Library, and the agents get them
// as any other. Its MCP servers are the Remote magpie's other switch
// (bridge_mcp.go): they run there, behind its router.
//
//   - Nothing the user has is written over. A skill whose name the
//     library already has stays as it is, and so does one taken and then
//     changed here; a set is kept under an id of its own, so it is never
//     one the user made. An item the user changed is theirs from then on:
//     it isn't updated, and isn't taken out when the remote drops it or
//     the switch goes off.
//   - A skill is given to the agents the remote gives it to when it
//     arrives, and the agents ticked here stay as they are after.
//   - A set is added, never switched on over the user's: only when the
//     text the agents get is empty, and they are ticked for none, does
//     the first one become what they read (as a new set does, #106).
//   - The servers' keys never leave the remote (Shared blanks them), and
//     what the remote keeps for this computer alone (Local) is not told.
//   - What was taken is recorded with a hash, per item, to tell the
//     user's change from the remote's.

// Borrowed is what this computer took from one Remote magpie's Library:
// by name the hash of each skill as taken, and by id that of each set.
type Borrowed struct {
	Name   string            `json:"name,omitempty"`
	Skills map[string]string `json:"skills,omitempty"`
	Sets   map[string]string `json:"sets,omitempty"`
}

// maxShared is how much of the skills' files a Library tells another
// magpie, and the most of one file: base64 in JSON, it stays under what
// a remote reads (provider.maxRemoteLibrary).
const (
	maxSharedAll  = 24 << 20
	maxSharedFile = 8 << 20
)

// Shared is the Library as another magpie is told it: the instruction
// sets with their texts and the skills with their files. The servers, the
// hooks and what each agent gets besides are this computer's; a skill
// linked to a folder here comes as its files.
func Shared() (*Bundle, error) {
	mu.Lock()
	defer mu.Unlock()
	l, err := load()
	if err != nil {
		return nil, err
	}
	b := &Bundle{Instructions: Instructions{Agents: orNone(slices.Sorted(slices.Values(l.Instructions.Agents))), Active: l.Instructions.Active},
		Texts: map[string]string{}, MCP: []*Server{}, Skills: []*CarriedSkill{}}
	for _, s := range l.Instructions.Sets {
		if strings.HasPrefix(s.ID, borrowedPrefix) { // taken from a magpie: not passed on
			continue
		}
		s.File = ""
		b.Instructions.Sets = append(b.Instructions.Sets, s)
	}
	for _, s := range l.sets() {
		if strings.HasPrefix(s.ID, borrowedPrefix) {
			continue
		}
		if text := setText(l.textPath(s.ID)); text != "" {
			b.Texts[s.ID] = text
		}
	}
	if strings.HasPrefix(b.Instructions.Active, borrowedPrefix) {
		b.Instructions.Active = ""
	}
	skills := sorted(l.Skills, func(s *Skill) string { return s.Name })
	var names []*Skill
	var dirs []string
	for _, s := range skills {
		if l.borrowedSkill(s.Name) { // taken from a magpie: not passed on
			continue
		}
		names = append(names, s)
		dirs = append(dirs, realDir(skillDir(s.Name)))
	}
	for i, c := range readSkillsMax(dirs, maxSharedFile, maxSharedAll) {
		c.Name, c.Agents = names[i].Name, orNone(slices.Sorted(slices.Values(names[i].Agents)))
		b.Skills = append(b.Skills, c)
	}
	return b, nil
}

// borrowedPrefix starts the id of a set taken from a Remote magpie.
const borrowedPrefix = "remote-"

func (l *Library) borrowedSkill(name string) bool {
	for _, b := range l.Borrowed {
		if _, ok := b.Skills[name]; ok {
			return true
		}
	}
	return false
}

// setID is the id a set of the remote id is kept under here.
func setID(remote, set string) string {
	h := sha256.Sum256([]byte(remote))
	return borrowedPrefix + hex.EncodeToString(h[:3]) + "-" + set
}

// skillSum is a skill's files and which of them run, as one hash.
func skillSum(c *CarriedSkill) string {
	h := sha256.New()
	for _, rel := range slices.Sorted(maps.Keys(c.Files)) {
		fmt.Fprintf(h, "%s\x00%d\x00%v\x00", rel, len(c.Files[rel]), slices.Contains(c.Exec, rel))
		h.Write(c.Files[rel])
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// BorrowedOn are the Remote magpies (provider ids) whose Library is used
// here. Nil when the library can't be read.
func BorrowedOn() map[string]bool {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return nil
	}
	on := map[string]bool{}
	for _, id := range l.BorrowOn {
		on[id] = true
	}
	return on
}

// SetBorrow turns the switch of the Remote magpie id on or off. On, its
// Library is read at once (UseRemoteLibraries); off, what was taken from
// it and not changed since is taken out.
func SetBorrow(id string, on bool) (*Result, error) {
	if on {
		return change(func(l *Library) error {
			if !slices.Contains(l.BorrowOn, id) {
				l.BorrowOn = append(l.BorrowOn, id)
			}
			return nil
		})
	}
	return change(func(l *Library) error {
		l.BorrowOn = slices.DeleteFunc(l.BorrowOn, func(x string) bool { return x == id })
		l.dropBorrowed(id, &Bundle{})
		delete(l.Borrowed, id)
		return nil
	})
}

// dropBorrowed takes out what was taken from id that b no longer has and
// the user hasn't changed.
func (l *Library) dropBorrowed(id string, b *Bundle) {
	got := l.Borrowed[id]
	if got == nil {
		return
	}
	have := map[string]bool{}
	for _, s := range b.Skills {
		have[s.Name] = true
	}
	stamp := time.Now().Format("2006-01-02_15-04-05.000")
	for _, name := range slices.Sorted(maps.Keys(got.Skills)) {
		if have[name] {
			continue
		}
		cur := l.skill(name)
		if cur != nil && readSum(name) == got.Skills[name] {
			if err := removeSkill(l, name, stamp); err != nil {
				log.Printf("library: taking out %s, once its remote dropped it: %v", name, err)
				continue
			}
		}
		delete(got.Skills, name) // gone, or the user's now
	}
	haveSet := map[string]bool{}
	for _, s := range b.Instructions.Sets {
		haveSet[setID(id, s.ID)] = true
	}
	if _, ok := b.Texts[defaultSet]; ok {
		haveSet[setID(id, defaultSet)] = true
	}
	for _, sid := range slices.Sorted(maps.Keys(got.Sets)) {
		if haveSet[sid] {
			continue
		}
		if l.hasSet(sid) && sum(l.setName(sid), setText(l.textPath(sid))) == got.Sets[sid] {
			if sid == l.active() {
				l.Instructions.Active = ""
			}
			if l.kept == nil {
				l.kept = newBackups()
			}
			if p := setPath(sid); readText(p) != "" {
				l.kept.keep("library", p)
			}
			writeText(setPath(sid), "")
			l.Instructions.Sets = slices.DeleteFunc(l.Instructions.Sets, func(s InstrSet) bool { return s.ID == sid })
		}
		delete(got.Sets, sid)
	}
}

func (l *Library) setName(id string) string {
	if s := l.instrSet(id); s != nil {
		return s.Name
	}
	return ""
}

func sum(name, text string) string {
	h := sha256.Sum256([]byte(name + "\x00" + strings.TrimSpace(text)))
	return hex.EncodeToString(h[:12])
}

func readSum(name string) string {
	return skillSum(readSkills([]string{realDir(skillDir(name))})[0])
}

// BorrowResult says what a read of a Remote magpie's Library did.
type BorrowResult struct {
	Added   []string // skills and sets taken in
	Updated []string
	Removed []string
	Kept    []string // the user's own by the same name, left as they are
}

// Borrow keeps the Library b of the Remote magpie id (called name) here,
// as the notes above say.
func Borrow(id, name string, b *Bundle) (*BorrowResult, error) {
	if err := b.check(); err != nil {
		return nil, err
	}
	out := &BorrowResult{}
	_, err := change(func(l *Library) error {
		*out = BorrowResult{}
		if !slices.Contains(l.BorrowOn, id) { // switched off meanwhile
			return errors.New("switched off")
		}
		if l.Borrowed == nil {
			l.Borrowed = map[string]*Borrowed{}
		}
		got := l.Borrowed[id]
		if got == nil {
			got = &Borrowed{}
			l.Borrowed[id] = got
		}
		got.Name = name
		if got.Skills == nil {
			got.Skills = map[string]string{}
		}
		if got.Sets == nil {
			got.Sets = map[string]string{}
		}
		l.dropBorrowed(id, b)

		if err := os.MkdirAll(skillsDir(), 0o755); err != nil {
			return err
		}
		stage, err := os.MkdirTemp(Dir(), ".incoming-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		aside := filepath.Join(BackupDir(), time.Now().Format("2006-01-02_15-04-05.000"), "skills")
		for _, s := range b.Skills {
			cur := l.skill(s.Name)
			want := skillSum(s)
			switch {
			case cur == nil && got.Skills[s.Name] != "":
				// taken before and taken out by the user since: not again
				delete(got.Skills, s.Name)
				out.Kept = append(out.Kept, "skill:"+s.Name)
				continue
			case cur == nil:
				if _, err := os.Lstat(skillDir(s.Name)); err == nil { // a folder in the way that isn't a skill here
					out.Kept = append(out.Kept, "skill:"+s.Name)
					continue
				}
			case got.Skills[s.Name] == "" || readSum(s.Name) != got.Skills[s.Name]:
				// the user's own, or taken and changed since
				delete(got.Skills, s.Name)
				out.Kept = append(out.Kept, "skill:"+s.Name)
				continue
			case got.Skills[s.Name] == want:
				continue
			}
			if err := writeSkill(filepath.Join(stage, s.Name), s); err != nil {
				return fmt.Errorf("skill %s: %w", s.Name, err)
			}
			if cur != nil {
				if err := os.MkdirAll(aside, 0o700); err != nil {
					return err
				}
				if err := move(skillDir(s.Name), filepath.Join(aside, s.Name)); err != nil {
					return err
				}
				out.Updated = append(out.Updated, "skill:"+s.Name)
			} else {
				// the agents the remote gives it to, only when it comes
				l.Skills = append(l.Skills, &Skill{Name: s.Name, Agents: slices.Clone(orNone(s.Agents))})
				out.Added = append(out.Added, "skill:"+s.Name)
			}
			if err := os.Rename(filepath.Join(stage, s.Name), skillDir(s.Name)); err != nil {
				return err
			}
			got.Skills[s.Name] = readSum(s.Name)
		}

		// the instruction sets
		l.kept = newBackups()
		var first string
		sets := slices.Clone(b.Instructions.Sets)
		if _, ok := b.Texts[defaultSet]; ok && !slices.ContainsFunc(sets, func(s InstrSet) bool { return s.ID == defaultSet }) {
			sets = append(sets, InstrSet{ID: defaultSet})
		}
		for _, s := range sets {
			text := strings.TrimSpace(b.Texts[s.ID])
			if text == "" {
				continue
			}
			sid := setID(id, s.ID)
			label := name
			if s.Name != "" {
				label += " · " + s.Name
			}
			cur := l.instrSet(sid)
			switch {
			case cur == nil && got.Sets[sid] != "":
				// taken before and taken out by the user since: not again
				out.Kept = append(out.Kept, "set:"+sid)
				continue
			case cur != nil && (got.Sets[sid] == "" || sum(cur.Name, setText(l.textPath(sid))) != got.Sets[sid]):
				delete(got.Sets, sid)
				out.Kept = append(out.Kept, "set:"+sid)
				continue
			case cur != nil && got.Sets[sid] == sum(label, text):
				continue
			}
			if p := setPath(sid); readText(p) != "" {
				if err := l.kept.keep("library", p); err != nil {
					return err
				}
			}
			if err := writeText(setPath(sid), text); err != nil {
				return err
			}
			if cur == nil {
				l.Instructions.Sets = append(l.Instructions.Sets, InstrSet{ID: sid, Name: label})
				out.Added = append(out.Added, "set:"+sid)
				if first == "" {
					first = sid
				}
			} else {
				cur.Name = label
				out.Updated = append(out.Updated, "set:"+sid)
			}
			got.Sets[sid] = sum(label, text)
		}
		// the agents read nothing yet: the first set taken is what they read
		if first != "" && setText(l.sharedPath()) == "" {
			l.Instructions.Active = first
			if len(l.Instructions.Agents) == 0 {
				l.Instructions.Agents = slices.Clone(orNone(b.Instructions.Agents))
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// borrowNotes are, per Remote magpie, what its last read said: "" when it
// went well. In memory only.
var borrowNotes = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

// BorrowNote is what the last read of the Remote magpie id's Library
// said went wrong, and the names it left as the user's.
func BorrowNote(id string) string {
	borrowNotes.Lock()
	defer borrowNotes.Unlock()
	return borrowNotes.m[id]
}

func setBorrowNote(id, note string) {
	borrowNotes.Lock()
	borrowNotes.m[id] = note
	borrowNotes.Unlock()
}

// UseRemoteLibrary reads the Library of the Remote magpie id and keeps it
// here (Borrow).
func UseRemoteLibrary(ctx context.Context, id string) (*BorrowResult, error) {
	body, name, err := provider.RemoteLibrary(ctx, id)
	if err != nil {
		setBorrowNote(id, err.Error())
		return nil, err
	}
	var b Bundle
	if err := json.Unmarshal(body, &b); err != nil {
		err = fmt.Errorf("the remote's Library isn't readable: %w", err)
		setBorrowNote(id, err.Error())
		return nil, err
	}
	b.MCP = nil // its servers run there: the router's switch gives them
	r, err := Borrow(id, name, &b)
	if err != nil {
		setBorrowNote(id, err.Error())
		return nil, err
	}
	note := ""
	if len(r.Kept) > 0 {
		note = "kept as yours: " + strings.Join(r.Kept, ", ")
	}
	setBorrowNote(id, note)
	return r, nil
}

// UseRemoteLibraries reads the Library of every Remote magpie switched
// on.
func UseRemoteLibraries(ctx context.Context) {
	for _, id := range slices.Sorted(maps.Keys(BorrowedOn())) {
		cctx, cancel := context.WithTimeout(ctx, 2*provider.RemoteLibraryWait)
		if _, err := UseRemoteLibrary(cctx, id); err != nil {
			log.Printf("library: the Library of %s: %v", id, err)
		}
		cancel()
	}
}

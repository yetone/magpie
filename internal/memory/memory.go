// Package memory is what the agents know from earlier conversations, kept
// once for all of them: a preference the user stated in Claude Code is
// known to Codex the next day. Each memory is one short fact with where it
// holds (everywhere, a project, an agent), what kind of fact it is, the
// user's tags, and where it came from. The agents read and add to it
// through the memory MCP server (gateway/memory_mcp.go), and the ones the
// user picks are also given a digest of it in their instructions
// (library/instructions.go). The Library's Memory tab lists and edits it.
//
// It is memory.json in magpie's config folder: one writer at a time across
// magpies (filelock), written through a temp file, the last good
// generation kept beside it, and a file that can't be read is unknown,
// never empty, so nothing is written over it.
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/lastgood"
)

// The kinds of memory. A kind decides how long a memory stays true and
// where it goes in a digest, so it is a field of its own, not a tag.
const (
	Profile    = "profile"    // who the user is: role, languages, what they know
	Preference = "preference" // how the user wants things done
	Rule       = "rule"       // a rule learned from a correction, with why
	Fact       = "fact"       // something true of a project the code doesn't say
	Reference  = "reference"  // where something is: a host, a dashboard, a URL
	Procedure  = "procedure"  // how something is done: steps that worked
	Episode    = "episode"    // what happened or was decided, and when
)

// Kinds are the kinds in the order a digest gives them.
var Kinds = []string{Profile, Preference, Rule, Reference, Fact, Procedure, Episode}

// Where a memory came from.
const (
	FromUser     = "user"     // typed or edited by the user
	FromAgent    = "agent"    // saved by an agent through the MCP server
	FromImported = "imported" // read from an agent's own memory files
	FromInferred = "inferred" // distilled from a conversation
)

// A memory's state.
const (
	Active     = "active"
	Candidate  = "candidate"  // waiting for the user to keep or drop it
	Archived   = "archived"   // set aside: out of searches and digests, kept
	Superseded = "superseded" // replaced by SupersededBy
)

// Global is the scope of a memory that holds everywhere. A project's is
// "project:" and its key (ProjectOf), an agent's "agent:" and its id.
const Global = "global"

// MaxText is the longest a memory's text may be: one fact, not a document.
const MaxText = 2000

// Memory is one thing known.
type Memory struct {
	ID    string   `json:"id"`
	Text  string   `json:"text"`
	Kind  string   `json:"kind"`
	Scope string   `json:"scope"`
	Tags  []string `json:"tags,omitempty"`
	// Why is the reason behind a rule or preference, when one was given
	Why          string    `json:"why,omitempty"`
	Origin       string    `json:"origin"`
	Status       string    `json:"status"`
	SupersededBy string    `json:"supersededBy,omitempty"`
	Pinned       bool      `json:"pinned,omitempty"`
	Sources      []Source  `json:"sources,omitempty"`
	Created      time.Time `json:"created"`
	Updated      time.Time `json:"updated"`
	// Used is when a search last returned it, Uses how many times
	Used time.Time `json:"used,omitzero"`
	Uses int       `json:"uses,omitempty"`
}

// Source is where a memory was said or found.
type Source struct {
	Agent   string    `json:"agent,omitempty"`
	Session string    `json:"session,omitempty"`
	Dir     string    `json:"dir,omitempty"`  // the folder the agent worked in
	File    string    `json:"file,omitempty"` // an imported memory's file
	Quote   string    `json:"quote,omitempty"`
	At      time.Time `json:"at"`
}

// Project is a project memories are kept for: a git repository by its
// remote, or a folder, with the folders it was seen in.
type Project struct {
	Key   string   `json:"key"`
	Name  string   `json:"name"`
	Roots []string `json:"roots,omitempty"`
}

// file is memory.json.
type file struct {
	Version  int                 `json:"version"`
	Memories []*Memory           `json:"memories"`
	Projects map[string]*Project `json:"projects,omitempty"`
	// Digest is the text each scope's digest had when it was last
	// published (Publish): what the agents' instructions are given, which
	// changes only when the user changes memory or once a day, so an
	// agent's save doesn't rewrite every agent's instructions mid-session
	Digest    map[string]string `json:"digest,omitempty"`
	Published time.Time         `json:"published,omitzero"`
	// Dismissed are the texts (norm) of memories distilled from
	// conversations that the user deleted or archived, the last
	// maxDismissed: never proposed again
	Dismissed []string `json:"dismissed,omitempty"`
}

// maxDismissed is how many dismissed texts are kept.
const maxDismissed = 1000

// dismiss notes that the user threw m away, when it was distilled: the
// same text isn't proposed again.
func dismiss(f *file, m *Memory) {
	if m.Origin != FromInferred {
		return
	}
	n := norm(m.Text)
	if slices.Contains(f.Dismissed, n) {
		return
	}
	f.Dismissed = append(f.Dismissed, n)
	if len(f.Dismissed) > maxDismissed {
		f.Dismissed = f.Dismissed[len(f.Dismissed)-maxDismissed:]
	}
}

// Path is memory.json; a variable for tests.
var Path = func() string { return appdir.Join(appdir.Config(), "memory.json") }

var (
	mu = filelock.Mutex{Path: func() string { return Path() + ".lock" }}
	// now is the clock; a variable for tests
	now = time.Now
)

// ErrNotFound is a memory id that isn't there.
var ErrNotFound = errors.New("no memory with that id")

// read reads memory.json: empty when it isn't there, an error when it is
// and can't be read.
func read() (*file, error) {
	f := &file{Version: 1}
	b, err := lastgood.Read(Path(), lastgood.Into[file])
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, f); err != nil {
		return nil, err
	}
	f.Memories = slices.DeleteFunc(f.Memories, func(m *Memory) bool { return m == nil || m.ID == "" })
	return f, nil
}

func write(f *file) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := lastgood.Keep(p, lastgood.Into[file], 0o600); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return edit.WriteAtomic(p, b)
}

// change reads memory.json, lets fn change it and writes it, one magpie
// at a time. A file that can't be read is never written over.
func change(fn func(f *file) error) error {
	mu.Lock()
	defer mu.Unlock()
	f, err := read()
	if err != nil {
		return err
	}
	if err := fn(f); err != nil {
		return err
	}
	return write(f)
}

// view reads memory.json under the lock, for a read that is consistent
// with the last write.
func view() (*file, error) {
	mu.Lock()
	defer mu.Unlock()
	return read()
}

// All is every memory, in every state, newest change first, and the
// projects they are kept for.
func All() ([]*Memory, map[string]*Project, error) {
	f, err := view()
	if err != nil {
		return nil, nil, err
	}
	slices.SortStableFunc(f.Memories, func(a, b *Memory) int { return b.Updated.Compare(a.Updated) })
	if f.Projects == nil {
		f.Projects = map[string]*Project{}
	}
	return f.Memories, f.Projects, nil
}

// Get is the memory with id.
func Get(id string) (*Memory, error) {
	f, err := view()
	if err != nil {
		return nil, err
	}
	if m := find(f, id); m != nil {
		return m, nil
	}
	return nil, ErrNotFound
}

func find(f *file, id string) *Memory {
	for _, m := range f.Memories {
		if m.ID == id {
			return m
		}
	}
	return nil
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return "m_" + hex.EncodeToString(b)
}

// Input is a memory as a caller gives it: to Save a new one, or to Edit
// one (where a nil field is left as it is).
type Input struct {
	Text   *string   `json:"text,omitempty"`
	Kind   *string   `json:"kind,omitempty"`
	Scope  *string   `json:"scope,omitempty"`
	Tags   *[]string `json:"tags,omitempty"`
	Why    *string   `json:"why,omitempty"`
	Status *string   `json:"status,omitempty"`
	Pinned *bool     `json:"pinned,omitempty"`
}

// Check says what is wrong with a memory's fields, nil when nothing.
func check(m *Memory) error {
	switch {
	case strings.TrimSpace(m.Text) == "":
		return errors.New("a memory needs its text")
	case utf8.RuneCountInString(m.Text) > MaxText:
		return fmt.Errorf("a memory is one fact, up to %d characters: this one has %d", MaxText, utf8.RuneCountInString(m.Text))
	case !slices.Contains(Kinds, m.Kind):
		return fmt.Errorf("kind %q isn't one of %s", m.Kind, strings.Join(Kinds, ", "))
	case !validScope(m.Scope):
		return fmt.Errorf("scope %q isn't %q, project:<key> or agent:<id>", m.Scope, Global)
	case !slices.Contains([]string{Active, Candidate, Archived, Superseded}, m.Status):
		return fmt.Errorf("status %q isn't active, candidate, archived or superseded", m.Status)
	}
	return nil
}

func validScope(s string) bool {
	if s == Global {
		return true
	}
	for _, p := range []string{"project:", "agent:"} {
		if rest, ok := strings.CutPrefix(s, p); ok && strings.TrimSpace(rest) != "" && !strings.ContainsAny(rest, "\n\r") {
			return true
		}
	}
	return false
}

// CleanTags are tags as kept: trimmed, lower case, no "#", each once, in
// the order given.
func CleanTags(tags []string) []string {
	out := []string{}
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "#")))
		t = strings.Join(strings.Fields(t), "-")
		if t != "" && utf8.RuneCountInString(t) <= 40 && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

func apply(m *Memory, in Input) {
	if in.Text != nil {
		m.Text = strings.TrimSpace(*in.Text)
	}
	if in.Kind != nil {
		m.Kind = strings.ToLower(strings.TrimSpace(*in.Kind))
	}
	if in.Scope != nil {
		m.Scope = strings.TrimSpace(*in.Scope)
	}
	if in.Tags != nil {
		m.Tags = CleanTags(*in.Tags)
	}
	if in.Why != nil {
		m.Why = strings.TrimSpace(*in.Why)
	}
	if in.Status != nil {
		m.Status = *in.Status
		if m.Status != Superseded {
			m.SupersededBy = ""
		}
	}
	if in.Pinned != nil {
		m.Pinned = *in.Pinned
	}
}

// norm is a text as compared for sameness: case, spaces and the full stop
// at its end left out.
func norm(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.TrimRight(s, ".。!！ ")
}

// Save adds a memory from origin, with src where it came from (nil for
// none). A memory with the same text in the same scope, not archived, is
// not added again: it gets the source, the tags it lacked and the newer
// kind and why, and is returned with added false. A project scope's
// project is recorded when proj is given.
//
// One distilled from a conversation (FromInferred) is not added when the
// user threw the same text away before (dismissed), or set it aside in
// that scope: m is nil then, or the one set aside, and added false.
func Save(in Input, origin string, src *Source, proj *Project) (m *Memory, added bool, err error) {
	t := now().UTC()
	m = &Memory{Kind: Fact, Scope: Global, Origin: origin, Status: Active, Created: t, Updated: t}
	apply(m, in)
	if err := check(m); err != nil {
		return nil, false, err
	}
	if src != nil {
		s := *src
		if s.At.IsZero() {
			s.At = t
		}
		m.Sources = []Source{s}
	}
	err = change(func(f *file) error {
		if origin == FromInferred {
			n := norm(m.Text)
			if slices.Contains(f.Dismissed, n) {
				m = nil
				return errSkip
			}
			for _, o := range f.Memories {
				if o.Scope == m.Scope && (o.Status == Archived || o.Status == Superseded) && norm(o.Text) == n {
					m = o
					return errSkip
				}
			}
		}
		if proj != nil && proj.Key != "" && m.Scope == "project:"+proj.Key {
			notePro(f, proj)
		}
		for _, o := range f.Memories {
			if o.Scope != m.Scope || o.Status == Archived || o.Status == Superseded || norm(o.Text) != norm(m.Text) {
				continue
			}
			for _, tg := range m.Tags {
				if !slices.Contains(o.Tags, tg) {
					o.Tags = append(o.Tags, tg)
				}
			}
			if !o.Pinned && o.Origin != FromUser {
				o.Kind = m.Kind
				if m.Why != "" {
					o.Why = m.Why
				}
			}
			if o.Status == Candidate && m.Status == Active {
				o.Status = Active
			}
			o.Sources = keepSources(append(o.Sources, m.Sources...))
			o.Updated = t
			m = o
			return nil
		}
		m.ID = newID()
		f.Memories = append(f.Memories, m)
		added = true
		return nil
	})
	if errors.Is(err, errSkip) {
		return m, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return m, added, nil
}

// errSkip ends a change without writing it.
var errSkip = errors.New("nothing to change")

// keepSources keeps a memory's last 20 sources.
func keepSources(s []Source) []Source {
	if len(s) > 20 {
		s = s[len(s)-20:]
	}
	return s
}

func notePro(f *file, p *Project) {
	if f.Projects == nil {
		f.Projects = map[string]*Project{}
	}
	have := f.Projects[p.Key]
	if have == nil {
		have = &Project{Key: p.Key, Name: p.Name}
		f.Projects[p.Key] = have
	}
	if have.Name == "" {
		have.Name = p.Name
	}
	for _, r := range p.Roots {
		if r != "" && !slices.Contains(have.Roots, r) {
			have.Roots = append(have.Roots, r)
		}
	}
	if len(have.Roots) > 10 {
		have.Roots = have.Roots[len(have.Roots)-10:]
	}
}

// NoteProject records a project memories may be kept for, for its name to
// be shown and its folders known.
func NoteProject(p *Project) error {
	if p == nil || p.Key == "" {
		return nil
	}
	return change(func(f *file) error { notePro(f, p); return nil })
}

// Edit changes the memory with id. An edit by the user (byUser) makes it
// the user's: what is distilled or saved later never changes it.
func Edit(id string, in Input, byUser bool) (*Memory, error) {
	var out *Memory
	err := change(func(f *file) error {
		m := find(f, id)
		if m == nil {
			return ErrNotFound
		}
		out = m
		return editIn(f, m, in, byUser)
	})
	return out, err
}

// editIn applies in to m, held in f, as Edit does.
func editIn(f *file, m *Memory, in Input, byUser bool) error {
	c := *m
	apply(&c, in)
	if err := check(&c); err != nil {
		return err
	}
	if byUser && c.Status == Archived && m.Status != Archived {
		dismiss(f, m)
	}
	if byUser && (in.Text != nil || in.Kind != nil || in.Scope != nil || in.Why != nil) {
		c.Origin = FromUser
	}
	c.Updated = now().UTC()
	*m = c
	return nil
}

// SetStatus gives the user's status to the memories with ids, in one
// write: those to review kept at once, or archived or brought back, as
// Edit would one by one (RealZeroP27 on X). An id no longer there is
// passed over; n is how many were changed, and none found is ErrNotFound.
// A status that isn't one changes nothing.
func SetStatus(status string, ids ...string) (n int, err error) {
	err = change(func(f *file) error {
		for _, m := range f.Memories {
			if !slices.Contains(ids, m.ID) {
				continue
			}
			if err := editIn(f, m, Input{Status: &status}, true); err != nil {
				return err
			}
			n++
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
	if err != nil {
		n = 0
	}
	return n, err
}

// Forget sets the memory with id aside (archived): out of searches and
// digests, kept to be brought back.
func Forget(id string) (*Memory, error) {
	s := Archived
	return Edit(id, Input{Status: &s}, false)
}

// Remove deletes the memories with ids for good.
func Remove(ids ...string) error {
	return change(func(f *file) error {
		n := len(f.Memories)
		f.Memories = slices.DeleteFunc(f.Memories, func(m *Memory) bool {
			if slices.Contains(ids, m.ID) {
				dismiss(f, m)
				return true
			}
			return false
		})
		if len(f.Memories) == n {
			return ErrNotFound
		}
		return nil
	})
}

// touch notes that a search returned the memories with ids, at most once
// a minute each so a busy agent doesn't rewrite the file on every search.
func touch(ids []string) {
	if len(ids) == 0 {
		return
	}
	t := now().UTC()
	f, err := view()
	if err != nil {
		return
	}
	stale := false
	for _, m := range f.Memories {
		if slices.Contains(ids, m.ID) && t.Sub(m.Used) > time.Minute {
			stale = true
			break
		}
	}
	if !stale {
		return
	}
	change(func(f *file) error {
		for _, m := range f.Memories {
			if slices.Contains(ids, m.ID) && t.Sub(m.Used) > time.Minute {
				m.Used = t
				m.Uses++
			}
		}
		return nil
	})
}

// Modified is memory.json's modification time, zero when it isn't there:
// for a reader to tell whether it changed.
func Modified() time.Time {
	st, err := os.Stat(Path())
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// cache is memory.json as cached() last read it, and its modification time.
var cache struct {
	sync.Mutex
	at time.Time
	f  *file
}

// cached is memory.json as last read, read again when it changed: for the
// digest, which a library sync reads for every agent.
func cached() (*file, error) {
	cache.Lock()
	defer cache.Unlock()
	st, err := os.Stat(Path())
	if errors.Is(err, fs.ErrNotExist) {
		return &file{Version: 1}, nil
	}
	if err == nil && cache.f != nil && st.ModTime().Equal(cache.at) {
		return cache.f, nil
	}
	f, err := read()
	if err != nil {
		return nil, err
	}
	if st != nil {
		cache.at, cache.f = st.ModTime(), f
	}
	return f, nil
}

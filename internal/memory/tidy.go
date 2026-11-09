package memory

import (
	"slices"
	"strings"
	"time"
)

// Tidying is memory's nightly pass (memdistill's Dream): memories that
// say the same thing are merged into one, ones a newer memory replaced
// or whose time has passed are set aside, and candidates nobody reviewed
// for long are set aside too. What the user wrote and what is pinned is
// never changed: it can be the one others are merged into, nothing more.
// Every change is recorded so the whole pass can be undone (Untidy).

// Merge is memories to merge: IDs into one that says Text.
type Merge struct {
	IDs  []string
	Text string
	Kind string
}

// Stale is a memory to set aside, and why.
type Stale struct {
	ID  string
	Why string
}

// Plan is what a tidy does: the merges, the memories to set aside, and
// Before, when an unreviewed candidate distilled from a conversation is
// set aside if it hasn't changed since (zero: none).
type Plan struct {
	Merges []Merge
	Stale  []Stale
	Before time.Time
}

// What a Change did.
const (
	Merged     = "merge"      // IDs were superseded by Into
	SetAside   = "aside"      // IDs were archived
	Unreviewed = "unreviewed" // Why of a candidate set aside for its age
)

// Change is one thing a tidy did, with what it takes to undo it.
type Change struct {
	Action string `json:"action"`
	// Into is the memory merged into; New when the merge made it
	Into string `json:"into,omitempty"`
	New  bool   `json:"new,omitempty"`
	// IDs are the memories superseded or set aside, Was their states
	// before
	IDs []string `json:"ids"`
	Was []string `json:"was"`
	// Text is what Into says, or what was set aside
	Text string `json:"text"`
	Why  string `json:"why,omitempty"`
}

// Fixed is a memory a tidy never changes: the user's own, or pinned.
func Fixed(m *Memory) bool { return m.Pinned || m.Origin == FromUser }

func live(m *Memory) bool { return m != nil && (m.Status == Active || m.Status == Candidate) }

// Tidy does p, as far as it still holds: a memory gone, no longer in use
// or the user's own is left alone, and a merge across scopes or of two
// of the user's own isn't done. It returns what it did.
func Tidy(p Plan) ([]Change, error) {
	var out []Change
	err := change(func(f *file) error {
		t := now().UTC()
		for _, mg := range p.Merges {
			var ms []*Memory
			for _, id := range mg.IDs {
				m := find(f, id)
				if !live(m) || slices.Contains(ms, m) {
					continue
				}
				ms = append(ms, m)
			}
			if len(ms) < 2 || slices.ContainsFunc(ms, func(m *Memory) bool { return m.Scope != ms[0].Scope }) {
				continue
			}
			var fixed []*Memory
			for _, m := range ms {
				if Fixed(m) {
					fixed = append(fixed, m)
				}
			}
			if len(fixed) > 1 {
				continue
			}
			var into *Memory
			c := Change{Action: Merged}
			if len(fixed) == 1 {
				into = fixed[0]
			} else {
				text := strings.TrimSpace(mg.Text)
				if text == "" || len(text) > MaxText {
					continue
				}
				kind := mg.Kind
				if !slices.Contains(Kinds, kind) {
					kind = ms[0].Kind
				}
				into = &Memory{ID: newID(), Text: text, Kind: kind, Scope: ms[0].Scope, Origin: FromInferred, Status: Candidate, Created: ms[0].Created, Updated: t}
				for _, m := range ms {
					if m.Status == Active {
						into.Status = Active
					}
					if into.Why == "" {
						into.Why = m.Why
					}
					for _, tg := range m.Tags {
						if !slices.Contains(into.Tags, tg) {
							into.Tags = append(into.Tags, tg)
						}
					}
					into.Sources = append(into.Sources, m.Sources...)
					if m.Created.Before(into.Created) {
						into.Created = m.Created
					}
					if m.Used.After(into.Used) {
						into.Used = m.Used
					}
					into.Uses += m.Uses
				}
				slices.SortStableFunc(into.Sources, func(a, b Source) int { return a.At.Compare(b.At) })
				into.Sources = keepSources(into.Sources)
				f.Memories = append(f.Memories, into)
				c.New = true
			}
			c.Into, c.Text = into.ID, into.Text
			for _, m := range ms {
				if m == into {
					continue
				}
				c.IDs, c.Was = append(c.IDs, m.ID), append(c.Was, m.Status)
				m.Status, m.SupersededBy, m.Updated = Superseded, into.ID, t
			}
			out = append(out, c)
		}
		aside := func(m *Memory, why string) {
			out = append(out, Change{Action: SetAside, IDs: []string{m.ID}, Was: []string{m.Status}, Text: m.Text, Why: why})
			m.Status, m.Updated = Archived, t
		}
		for _, s := range p.Stale {
			if m := find(f, s.ID); live(m) && !Fixed(m) {
				aside(m, strings.TrimSpace(s.Why))
			}
		}
		if !p.Before.IsZero() {
			for _, m := range f.Memories {
				if m.Status == Candidate && m.Origin == FromInferred && !Fixed(m) && m.Updated.Before(p.Before) {
					aside(m, Unreviewed)
				}
			}
		}
		if len(out) == 0 {
			return errSkip
		}
		return nil
	})
	if err == errSkip {
		err = nil
	}
	return out, err
}

// Untidy undoes cs: each memory a tidy superseded or set aside, and still
// left so, goes back to the state it had, and a memory a merge made is
// removed, unless the user has made it theirs since (it stays then, with
// the ones it was made from back beside it). It returns how many memories
// came back.
func Untidy(cs []Change) (int, error) {
	n := 0
	err := change(func(f *file) error {
		t := now().UTC()
		for i := len(cs) - 1; i >= 0; i-- {
			c := cs[i]
			for k, id := range c.IDs {
				m := find(f, id)
				if m == nil || k >= len(c.Was) {
					continue
				}
				if c.Action == Merged && m.Status == Superseded && m.SupersededBy == c.Into || c.Action == SetAside && m.Status == Archived {
					m.Status, m.SupersededBy, m.Updated = c.Was[k], "", t
					n++
				}
			}
			if c.New {
				f.Memories = slices.DeleteFunc(f.Memories, func(m *Memory) bool { return m.ID == c.Into && !Fixed(m) })
			}
		}
		return nil
	})
	return n, err
}

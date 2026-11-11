package memory

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
)

// What memory suggests the user make more of (Suggest): a memory learned
// in two or more projects is one that holds everywhere, and a procedure
// the agents search for often is worth a skill of its own.
const (
	ToGlobal = "global"
	ToSkill  = "skill"
)

// skillUses is how often searches return a procedure before it is
// suggested as a skill: often enough that the agents reach for it, not
// one day's work.
const skillUses = 5

// alike is how many of two memories' words they share (Jaccard) before
// they count as saying the same thing in two projects, when their texts
// aren't the same. Short texts must be the same: four words in common of
// five is another fact as often as the same one.
const (
	alike      = 0.6
	alikeWords = 6
)

// maxDeclined is how many suggestions turned down are kept.
const maxDeclined = 500

// Suggestion is one thing memory suggests.
type Suggestion struct {
	Key  string   `json:"key"`  // what Decline takes
	To   string   `json:"to"`   // ToGlobal or ToSkill
	IDs  []string `json:"ids"`  // the memories it is about, the one kept first
	Text string   `json:"text"` // the text kept
	// Projects are the projects' keys a ToGlobal one was learned in
	Projects []string `json:"projects,omitempty"`
	Uses     int      `json:"uses,omitempty"` // a ToSkill one's
}

// Suggest is what memory suggests now, none the user turned down: the
// project memories that say the same in two or more projects, or one
// whose sources were in two or more, to make global; and the procedures
// returned skillUses times or more, not made a skill yet, to make one.
func Suggest() ([]Suggestion, error) {
	f, err := view()
	if err != nil {
		return nil, err
	}
	out := append(globalSuggestions(f), skillSuggestions(f)...)
	return slices.DeleteFunc(out, func(s Suggestion) bool { return slices.Contains(f.Declined, s.Key) }), nil
}

func projectOfScope(s string) string {
	k, ok := strings.CutPrefix(s, "project:")
	if !ok {
		return ""
	}
	return k
}

func globalSuggestions(f *file) []Suggestion {
	var pool []*Memory
	for _, m := range f.Memories {
		if m.Status == Active && projectOfScope(m.Scope) != "" {
			pool = append(pool, m)
		}
	}
	words := make([][]string, len(pool))
	for i, m := range pool {
		w := Words(m.Text)
		slices.Sort(w)
		words[i] = slices.Compact(w)
	}
	// memories that say the same in different projects, joined
	up := make([]int, len(pool))
	for i := range up {
		up[i] = i
	}
	var root func(int) int
	root = func(i int) int {
		for up[i] != i {
			up[i] = up[up[i]]
			i = up[i]
		}
		return i
	}
	for i := range pool {
		for j := i + 1; j < len(pool); j++ {
			if pool[i].Scope == pool[j].Scope {
				continue
			}
			if norm(pool[i].Text) == norm(pool[j].Text) || sameWords(words[i], words[j]) {
				up[root(j)] = root(i)
			}
		}
	}
	groups := map[int][]*Memory{}
	for i, m := range pool {
		groups[root(i)] = append(groups[root(i)], m)
	}
	var out []Suggestion
	for _, ms := range groups {
		ps := map[string]bool{}
		for _, m := range ms {
			ps[projectOfScope(m.Scope)] = true
			for _, s := range m.Sources {
				if k := projectAt(f, s.Dir); k != "" {
					ps[k] = true
				}
			}
		}
		if len(ps) < 2 {
			continue
		}
		slices.SortFunc(ms, keptFirst)
		s := Suggestion{To: ToGlobal, Text: ms[0].Text}
		for _, m := range ms {
			s.IDs = append(s.IDs, m.ID)
		}
		for k := range ps {
			s.Projects = append(s.Projects, k)
		}
		slices.Sort(s.Projects)
		s.Key = ToGlobal + ":" + strings.Join(slices.Sorted(slices.Values(s.IDs)), ",")
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Suggestion) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// sameWords is whether two sorted word sets share alike of their words,
// both long enough to tell.
func sameWords(a, b []string) bool {
	if len(a) < alikeWords || len(b) < alikeWords {
		return false
	}
	n := 0
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i] == b[j]:
			n++
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return float64(n) >= alike*float64(len(a)+len(b)-n)
}

// projectAt is the key of the project whose folder dir is in, or "".
func projectAt(f *file, dir string) string {
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(dir)
	best, at := "", 0
	for k, p := range f.Projects {
		for _, r := range p.Roots {
			r = filepath.Clean(r)
			if (dir == r || strings.HasPrefix(dir, r+string(filepath.Separator))) && len(r) > at {
				best, at = k, len(r)
			}
		}
	}
	return best
}

// keptFirst orders memories saying the same thing by which one stays
// when they are made one: the user's own or pinned, then the most used,
// then the latest changed.
func keptFirst(a, b *Memory) int {
	if Fixed(a) != Fixed(b) {
		if Fixed(a) {
			return -1
		}
		return 1
	}
	if a.Uses != b.Uses {
		return b.Uses - a.Uses
	}
	if c := b.Updated.Compare(a.Updated); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

func skillSuggestions(f *file) []Suggestion {
	var out []Suggestion
	for _, m := range f.Memories {
		if m.Status != Active || m.Kind != Procedure || m.Skill != "" || m.Uses < skillUses {
			continue
		}
		out = append(out, Suggestion{Key: ToSkill + ":" + m.ID, To: ToSkill, IDs: []string{m.ID}, Text: m.Text, Uses: m.Uses})
	}
	slices.SortFunc(out, func(a, b Suggestion) int { return b.Uses - a.Uses })
	return out
}

// Decline turns a suggestion down: it isn't made again. A ToGlobal one
// that another project's memory joins later is another suggestion.
func Decline(key string) error {
	if key == "" {
		return errors.New("name the suggestion")
	}
	return change(func(f *file) error {
		if slices.Contains(f.Declined, key) {
			return errSkip
		}
		f.Declined = append(f.Declined, key)
		if len(f.Declined) > maxDeclined {
			f.Declined = f.Declined[len(f.Declined)-maxDeclined:]
		}
		return nil
	})
}

// ErrNotSuggested is a suggestion taken that is no longer made.
var ErrNotSuggested = errors.New("that isn't suggested any more")

// Promote makes the project memories with ids one global memory, as the
// user asked: the one kept first (the user's own or pinned, the most
// used, the latest) holds everywhere from now, carrying the others' tags
// and sources, and the others are superseded by it. It is the user's
// change, so a sync never undoes it with an older one.
func Promote(ids []string) (*Memory, error) {
	if len(ids) == 0 {
		return nil, errors.New("name the memories")
	}
	var kept *Memory
	err := change(func(f *file) error {
		var ms []*Memory
		for _, id := range ids {
			m := find(f, id)
			if m == nil || m.Status != Active || projectOfScope(m.Scope) == "" {
				return ErrNotSuggested
			}
			ms = append(ms, m)
		}
		slices.SortFunc(ms, keptFirst)
		t := now().UTC()
		// the same text global already: that one is kept, and these go into it
		for _, g := range f.Memories {
			if g.Scope == Global && (g.Status == Active || g.Status == Candidate) && norm(g.Text) == norm(ms[0].Text) {
				g.Status = Active
				ms = append([]*Memory{g}, ms...)
				break
			}
		}
		k := ms[0]
		k.Scope = Global
		for _, o := range ms[1:] {
			for _, tg := range o.Tags {
				if !slices.Contains(k.Tags, tg) {
					k.Tags = append(k.Tags, tg)
				}
			}
			k.Sources = keepSources(append(k.Sources, o.Sources...))
			k.Uses += o.Uses
			if o.Used.After(k.Used) {
				k.Used = o.Used
			}
			k.Pinned = k.Pinned || o.Pinned
			o.Status, o.SupersededBy, o.Updated, o.Edited = Superseded, k.ID, t, t
		}
		k.Updated, k.Edited = t, t
		kept = k
		return nil
	})
	if err != nil {
		return nil, err
	}
	c := *kept
	return &c, nil
}

// MadeSkill notes that the procedure with id was made the skill called
// name: it is archived, since the skill holds it now, and isn't suggested
// again. Restored, it is searched again, still noted as that skill's.
func MadeSkill(id, name string) error {
	return change(func(f *file) error {
		m := find(f, id)
		if m == nil {
			return ErrNotFound
		}
		t := now().UTC()
		m.Skill = name
		m.Status = Archived
		m.Updated, m.Edited = t, t
		return nil
	})
}

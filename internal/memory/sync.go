package memory

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"slices"
	"time"
)

// keepRemoved is how long a deletion is remembered: a computer that
// syncs again within it doesn't bring the memory back. One away longer
// does, as a memory it still holds.
const keepRemoved = 180 * 24 * time.Hour

// removed notes that the memory with id was deleted for good, now.
func removed(f *file, id string) {
	if f.Removed == nil {
		f.Removed = map[string]time.Time{}
	}
	f.Removed[id] = now()
}

// Bundle is what of memory goes to another computer, in a backup or a
// sync: the memories in every state, the projects they are kept for, the
// distilled texts the user threw away and the memories deleted. The
// published digests and the vectors (memembed) are this computer's own,
// made again from the memories here.
type Bundle struct {
	Memories  []*Memory            `json:"memories"`
	Projects  map[string]*Project  `json:"projects,omitempty"`
	Dismissed []string             `json:"dismissed,omitempty"`
	Removed   map[string]time.Time `json:"removed,omitempty"`
	Declined  []string             `json:"declined,omitempty"`
}

// Collect is this computer's memory, to carry: nil when memory.json isn't
// there, an error when it is and can't be read (a failed read is unknown:
// a backup of it as nothing would say there was nothing).
func Collect() (*Bundle, error) {
	if _, err := os.Stat(Path()); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	f, err := view()
	if err != nil {
		return nil, err
	}
	return bundleOf(f), nil
}

func bundleOf(f *file) *Bundle {
	b := &Bundle{Memories: f.Memories, Projects: f.Projects, Dismissed: f.Dismissed, Removed: f.Removed, Declined: f.Declined}
	return Combine(b, nil) // in order, the old deletions dropped
}

// Hash is what tells a change in b: the memories, projects, dismissed
// texts and deletions, but not when a search last returned a memory nor
// how often (Used, Uses), which an agent's search changes and which
// alone is no change worth a sync. A nil b hashes as "".
func (b *Bundle) Hash() string {
	if b == nil {
		return ""
	}
	c := *b
	c.Memories = make([]*Memory, len(b.Memories))
	for i, m := range b.Memories {
		k := *m
		k.Used, k.Uses = time.Time{}, 0
		c.Memories[i] = &k
	}
	j, _ := json.Marshal(c)
	s := sha256.Sum256(j)
	return hex.EncodeToString(s[:])
}

// Combine is a and b together, either nil, each memory once:
//   - of the same memory on both sides, the one changed later, unless
//     that change wasn't the user's (an agent's save, a tidy) and the
//     other is pinned or the user's (Fixed) while it isn't: what the
//     user made theirs is never written over by what a machine did on
//     another computer before it saw that;
//   - when a search last returned it and how often, the most of both;
//   - one deleted on either side stays deleted, unless it was changed
//     after the deletion;
//   - the projects, dismissed texts, deletions and suggestions turned
//     down of both.
//
// It is the same whichever side is a: memories by id, projects by key.
func Combine(a, b *Bundle) *Bundle {
	if a == nil {
		a = &Bundle{}
	}
	if b == nil {
		b = &Bundle{}
	}
	out := &Bundle{Memories: []*Memory{}}
	cut := now().Add(-keepRemoved)
	for _, r := range []map[string]time.Time{a.Removed, b.Removed} {
		for id, t := range r {
			if t.Before(cut) {
				continue
			}
			if out.Removed == nil {
				out.Removed = map[string]time.Time{}
			}
			if t.After(out.Removed[id]) {
				out.Removed[id] = t
			}
		}
	}
	byID := map[string]*Memory{}
	for _, ms := range [][]*Memory{a.Memories, b.Memories} {
		for _, m := range ms {
			if m == nil || m.ID == "" {
				continue
			}
			if o := byID[m.ID]; o != nil {
				byID[m.ID] = pick(o, m)
			} else {
				c := *m
				byID[m.ID] = &c
			}
		}
	}
	for id, m := range byID {
		if t, ok := out.Removed[id]; ok && !m.Updated.After(t) {
			continue
		}
		out.Memories = append(out.Memories, m)
	}
	slices.SortFunc(out.Memories, func(x, y *Memory) int {
		return cmp.Or(x.Created.Compare(y.Created), cmp.Compare(x.ID, y.ID))
	})
	for _, ps := range []map[string]*Project{a.Projects, b.Projects} {
		for _, k := range slices.Sorted(maps.Keys(ps)) {
			p := ps[k]
			if p == nil {
				continue
			}
			if out.Projects == nil {
				out.Projects = map[string]*Project{}
			}
			o := out.Projects[k]
			if o == nil {
				c := *p
				c.Roots = slices.Clone(p.Roots)
				out.Projects[k] = &c
				continue
			}
			o.Name = max(o.Name, p.Name)
			o.Roots = append(o.Roots, p.Roots...)
		}
	}
	for _, p := range out.Projects {
		slices.Sort(p.Roots)
		p.Roots = slices.Compact(p.Roots)
	}
	// the dismissed texts in one order on every computer: the order they
	// were thrown away in is lost once two computers' are put together
	d := append(slices.Clone(a.Dismissed), b.Dismissed...)
	slices.Sort(d)
	if d = slices.Compact(d); len(d) > maxDismissed {
		d = d[len(d)-maxDismissed:]
	}
	if len(d) > 0 {
		out.Dismissed = d
	}
	n := append(slices.Clone(a.Declined), b.Declined...)
	slices.Sort(n)
	if n = slices.Compact(n); len(n) > maxDeclined {
		n = n[len(n)-maxDeclined:]
	}
	if len(n) > 0 {
		out.Declined = n
	}
	return out
}

// byUser is whether m's last change was the user's.
func byUser(m *Memory) bool { return !m.Edited.IsZero() && m.Edited.Equal(m.Updated) }

// pick is the one of two copies of a memory that stays (Combine), as a copy.
func pick(x, y *Memory) *Memory {
	newer, older := x, y
	if y.Updated.After(x.Updated) || y.Updated.Equal(x.Updated) && later(y, x) {
		newer, older = y, x
	}
	if !byUser(newer) && Fixed(older) && !Fixed(newer) {
		newer, older = older, newer
	}
	c := *newer
	if older.Used.After(c.Used) {
		c.Used = older.Used
	}
	c.Uses = max(c.Uses, older.Uses)
	return &c
}

// later breaks a tie of two copies changed at the same time, the same way
// on both computers.
func later(x, y *Memory) bool {
	jx, _ := json.Marshal(x)
	jy, _ := json.Marshal(y)
	return string(jx) > string(jy)
}

// Put merges b into memory here (Combine), writing memory.json only when
// that changes it. It reports whether it did; a file here that can't be
// read is an error, and nothing is written over it.
func Put(b *Bundle) (changed bool, err error) {
	if b == nil {
		return false, nil
	}
	err = change(func(f *file) error {
		here := bundleOf(f)
		m := Combine(here, b)
		if m.Hash() == here.Hash() && sameUses(m, here) {
			return errSkip
		}
		f.Memories, f.Projects, f.Dismissed, f.Removed, f.Declined = m.Memories, m.Projects, m.Dismissed, m.Removed, m.Declined
		changed = true
		return nil
	})
	if errors.Is(err, errSkip) {
		err = nil
	}
	return changed, err
}

// sameUses is whether a and b, the same memories, were used alike.
func sameUses(a, b *Bundle) bool {
	return slices.EqualFunc(a.Memories, b.Memories, func(x, y *Memory) bool {
		return x.Used.Equal(y.Used) && x.Uses == y.Uses
	})
}

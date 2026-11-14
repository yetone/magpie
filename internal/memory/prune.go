package memory

import (
	"slices"
	"time"
)

// Retention: what nobody acted on doesn't stay forever. A candidate left
// as it was for Keep is archived; an archived or superseded memory left
// so for Keep more is removed. What the user wrote or pinned (Fixed) is
// never removed, whatever its state, and a removal is recorded (Removed)
// so a sync from another computer doesn't bring it back. A distilled
// text removed is dismissed too: it is never proposed again.

// Keep is how long a candidate waits to be reviewed, and how long an
// archived or superseded memory is kept, before Prune moves it on.
const Keep = 30 * 24 * time.Hour

// Pruned is what one Prune did.
type Pruned struct {
	Archived int // candidates set aside
	Removed  int // archived or superseded memories removed
}

// Prune applies retention now. It writes nothing when nothing is due.
func Prune() (Pruned, error) {
	var p Pruned
	err := change(func(f *file) error {
		t := now()
		cut := t.Add(-Keep)
		f.Memories = slices.DeleteFunc(f.Memories, func(m *Memory) bool {
			if Fixed(m) || !m.Updated.Before(cut) {
				return false
			}
			switch m.Status {
			case Candidate:
				m.Status, m.Updated = Archived, t
				p.Archived++
			case Archived, Superseded:
				dismiss(f, m)
				removed(f, m.ID)
				p.Removed++
				return true
			}
			return false
		})
		if p == (Pruned{}) {
			return errSkip
		}
		return nil
	})
	if err == errSkip {
		err = nil
	}
	return p, err
}

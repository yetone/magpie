package memdistill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/filelock"
	"github.com/yetone/magpie/internal/memory"
)

// Dreaming is the nightly tidy: once a day, after DreamHour, a model is
// shown each scope's memories and names the ones that say the same thing
// (merged into one) and the ones no longer true (set aside); candidates
// distilled from conversations and left unreviewed for Unreviewed are set
// aside too. What the user wrote or pinned is never changed
// (memory.Tidy). What the last tidy did is kept in State.Changes, to be
// undone (Undo).

const (
	// DreamHour is the local hour a tidy is due from each day
	DreamHour = 3
	// Unreviewed is how long a distilled candidate waits for review before
	// a tidy sets it aside
	Unreviewed = 30 * 24 * time.Hour
	// perAsk caps the memories one request is shown
	perAsk = 120
)

var dreaming atomic.Bool

// Dreaming says a tidy is going in this process (Running says so too).
func Dreaming() bool { return dreaming.Load() }

// SetDream switches the nightly tidy on or off. Switched on, the first is
// due at the next DreamHour; Tidy now does one at once.
func SetDream(on bool) (State, error) {
	return change(func(s *State) {
		if on && !s.Dream {
			s.DreamSince = now().UTC()
		}
		s.Dream = on
	})
}

// Dreams says a tidy is due at t: switched on, and none done since the
// last DreamHour passed (local time), nor since it was switched on.
func Dreams(s State, t time.Time) bool {
	if !s.Dream {
		return false
	}
	t = t.Local()
	at := time.Date(t.Year(), t.Month(), t.Day(), DreamHour, 0, 0, 0, time.Local)
	if t.Before(at) {
		at = at.AddDate(0, 0, -1)
	}
	return s.Dreamt.Before(at) && s.DreamSince.Before(at)
}

// Dream tidies memory now, switched on or not, with the model picked or
// auto. A request that fails changes nothing; the error is kept and the
// tidy is tried again on the next tick.
func Dream(ctx context.Context, auto string, complete Complete) ([]memory.Change, error) {
	if !running.TryLock() {
		return nil, ErrRunning
	}
	defer running.Unlock()
	busy.Store(true)
	defer busy.Store(false)
	dreaming.Store(true)
	defer dreaming.Store(false)
	unlock, held := filelock.Lock(Path()+".run", 0)
	if !held {
		return nil, ErrRunning
	}
	defer unlock()
	st, err := Load()
	if err != nil {
		return nil, err
	}
	model := st.Model
	if model == "" {
		model = auto
	}
	plan, err := ask(ctx, model, st.Effort, complete)
	var cs []memory.Change
	if err == nil {
		plan.Before = now().Add(-Unreviewed)
		cs, err = memory.Tidy(plan)
	}
	_, serr := change(func(s *State) {
		if err != nil {
			s.DreamError = err.Error()
			return
		}
		s.Dreamt, s.DreamModel, s.DreamError, s.Changes, s.Undone = now().UTC(), model, "", cs, false
	})
	return cs, errors.Join(err, serr)
}

// Undo puts back what the last tidy changed, as far as the user hasn't
// changed it since, once.
func Undo() (int, error) {
	if !running.TryLock() {
		return 0, ErrRunning
	}
	defer running.Unlock()
	st, err := Load()
	if err != nil || st.Undone || len(st.Changes) == 0 {
		return 0, err
	}
	n, err := memory.Untidy(st.Changes)
	if err != nil {
		return n, err
	}
	_, err = change(func(s *State) { s.Undone = true })
	return n, err
}

// ask shows the model each scope's memories that could change, perAsk at
// a time, and gathers what it names. A scope with fewer than two, or with
// only the user's own, isn't sent.
func ask(ctx context.Context, model, effort string, complete Complete) (memory.Plan, error) {
	var p memory.Plan
	all, _, err := memory.All()
	if err != nil {
		return p, err
	}
	scopes := map[string][]*memory.Memory{}
	var order []string
	for _, m := range all {
		if m.Status != memory.Active && m.Status != memory.Candidate {
			continue
		}
		if _, ok := scopes[m.Scope]; !ok {
			order = append(order, m.Scope)
		}
		scopes[m.Scope] = append(scopes[m.Scope], m)
	}
	for _, sc := range order {
		ms := scopes[sc]
		if len(ms) < 2 || !slices.ContainsFunc(ms, func(m *memory.Memory) bool { return !memory.Fixed(m) }) {
			continue
		}
		slices.SortStableFunc(ms, func(a, b *memory.Memory) int {
			if c := slices.Index(memory.Kinds, a.Kind) - slices.Index(memory.Kinds, b.Kind); c != 0 {
				return c
			}
			return a.Created.Compare(b.Created)
		})
		for i := 0; i < len(ms); i += perAsk {
			chunk := ms[i:min(i+perAsk, len(ms))]
			if len(chunk) < 2 {
				continue
			}
			if model == "" {
				return p, errors.New("no model to tidy memory with: pick one")
			}
			if ctx.Err() != nil {
				return p, ctx.Err()
			}
			reply, err := complete(ctx, model, effort, dreamPrompt, listing(chunk))
			if err != nil {
				return p, fmt.Errorf("%s: %w", model, err)
			}
			mg, st := plan(reply, chunk)
			p.Merges, p.Stale = append(p.Merges, mg...), append(p.Stale, st...)
		}
	}
	return p, nil
}

// listing is the memories as the model is shown them, one a line, each
// under a short name (n1, n2…) in place of its id.
func listing(ms []*memory.Memory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Today: %s\n\nNotes:\n", now().Format("2006-01-02"))
	for i, m := range ms {
		fmt.Fprintf(&b, "n%d | %s | %s | used %d× | ", i+1, m.Kind, m.Created.Format("2006-01-02"), m.Uses)
		if memory.Fixed(m) {
			b.WriteString("fixed | ")
		}
		b.WriteString(oneLine(m.Text))
		if m.Why != "" {
			b.WriteString(" — " + oneLine(m.Why))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// plan reads the reply's JSON object, its short names turned back into
// the memories' ids; a name not shown is left out, and a reply without
// the object is nothing to do.
func plan(reply string, ms []*memory.Memory) ([]memory.Merge, []memory.Stale) {
	i, j := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if i < 0 || j < i {
		return nil, nil
	}
	var r struct {
		Merge []struct {
			IDs  []string `json:"ids"`
			Text string   `json:"text"`
			Kind string   `json:"kind"`
		} `json:"merge"`
		Stale []struct {
			ID  string `json:"id"`
			Why string `json:"why"`
		} `json:"stale"`
	}
	if json.Unmarshal([]byte(reply[i:j+1]), &r) != nil {
		return nil, nil
	}
	id := func(n string) string {
		var k int
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "n%d", &k); err != nil || k < 1 || k > len(ms) || fmt.Sprintf("n%d", k) != strings.TrimSpace(n) {
			return ""
		}
		return ms[k-1].ID
	}
	var mg []memory.Merge
	for _, m := range r.Merge {
		var ids []string
		for _, n := range m.IDs {
			if x := id(n); x != "" {
				ids = append(ids, x)
			}
		}
		if len(ids) >= 2 {
			mg = append(mg, memory.Merge{IDs: ids, Text: m.Text, Kind: strings.ToLower(strings.TrimSpace(m.Kind))})
		}
	}
	var st []memory.Stale
	for _, s := range r.Stale {
		if x := id(s.ID); x != "" {
			st = append(st, memory.Stale{ID: x, Why: cut(strings.TrimSpace(s.Why), 200)})
		}
	}
	return mg, st
}

// dreamPrompt names neither the app nor any vendor: it goes to the
// model's vendor as written.
const dreamPrompt = `You tidy a list of notes kept about a user and their projects, for a coding assistant to read in later conversations. Each line is: name | kind | date written | how often it was used | "fixed" when the user wrote or pinned it | text — why.

Find two things:
- merge: notes that say the same thing, or where one says all another does. Give their names and one text that keeps everything they say, short, in the language they are written in, and its kind. A fixed note's words can't change: when one is in a merge, it is the note kept, and the others go into it.
- stale: notes no longer true: one a later note contradicts, one about a date that has passed, one about a one-off task that is done. A decision, a direction or a preference stays even when the work it came from is done. Never a fixed note.

When unsure, leave a note as it is. Most notes stay.

Answer with a JSON object alone:
{"merge": [{"ids": ["n1", "n4"], "text": "the merged note", "kind": "preference"}], "stale": [{"id": "n7", "why": "a few words"}]}
When there is nothing to do, answer {"merge": [], "stale": []}.`

package memory

// Stats are memory's day-by-day counts for the Memory tab's numbers and
// trend (the owner, 11-25: how fast memory is made, recalled, updated and
// cleaned). They are this computer's own, in memory-stats.json beside the
// memory folder: counts only, never a memory's text, never in a backup or
// sync (another computer counts what it does itself).
//
// What is counted, each on the local day it happens:
//
//   - Created: a memory added, by its origin (user, agent, inferred,
//     imported). A memory saved again with the same text isn't a new one:
//     it is an update. A memory a tidy merge makes isn't either: it is the
//     merge, an update.
//   - Updated: a memory's text, kind, scope, why, tags or pin changed, by
//     anyone; the same text saved again (it gains a source); a tidy merge
//     or a promotion to global, once each.
//   - Cleaned: a memory leaving use (in use or to review, to archived,
//     superseded or deleted), by who did it: tidy (the nightly tidy's
//     merges and set-asides), expiry (retention, Prune), you (the Memory
//     tab), agent (memory_forget). Deleting one already set aside isn't
//     counted again.
//   - Searches, Found, Recalled: an agent's memory_search with words
//     (Search; the tab's own search box is Rank and isn't counted), the
//     ones that returned at least one memory, and the memories returned.
//
// Nothing was counted before this file was first written (Since). The
// days before it take Created from the memory files' own dates
// (Backfilled), and have no other counts.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filelock"
)

// Who set a memory aside, for Day.Cleaned.
const (
	ByTidy   = "tidy"
	ByExpiry = "expiry"
	ByYou    = "you"
	ByAgent  = "agent"
)

// Day is one day's counts.
type Day struct {
	Created  map[string]int `json:"created,omitempty"`
	Updated  int            `json:"updated,omitempty"`
	Cleaned  map[string]int `json:"cleaned,omitempty"`
	Searches int            `json:"searches,omitempty"`
	Found    int            `json:"found,omitempty"`
	Recalled int            `json:"recalled,omitempty"`
}

func (d *Day) empty() bool {
	return d == nil || len(d.Created) == 0 && d.Updated == 0 && len(d.Cleaned) == 0 && d.Searches == 0
}

func (d *Day) add(o *Day) {
	if o == nil {
		return
	}
	for k, n := range o.Created {
		if d.Created == nil {
			d.Created = map[string]int{}
		}
		d.Created[k] += n
	}
	for k, n := range o.Cleaned {
		if d.Cleaned == nil {
			d.Cleaned = map[string]int{}
		}
		d.Cleaned[k] += n
	}
	d.Updated += o.Updated
	d.Searches += o.Searches
	d.Found += o.Found
	d.Recalled += o.Recalled
}

func (d *Day) created(origin string) {
	if d.Created == nil {
		d.Created = map[string]int{}
	}
	d.Created[origin]++
}

func (d *Day) cleaned(by string) {
	if d.Cleaned == nil {
		d.Cleaned = map[string]int{}
	}
	d.Cleaned[by]++
}

// statsFile is memory-stats.json.
type statsFile struct {
	Version int             `json:"version"`
	Since   string          `json:"since"`
	Days    map[string]*Day `json:"days"`
}

// keepDays is how many days of counts are kept.
const keepDays = 400

func statsPath() string { return filepath.Join(filepath.Dir(Path()), "memory-stats.json") }

var statsMu = filelock.Mutex{Path: func() string { return statsPath() + ".lock" }}

func dayOf(t time.Time) string { return t.Format("2006-01-02") }

// readStats is memory-stats.json: none when it isn't there, an error when
// it can't be read (never written over then).
func readStats() (*statsFile, error) {
	s := &statsFile{Version: 1, Days: map[string]*Day{}}
	b, err := os.ReadFile(statsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Days == nil {
		s.Days = map[string]*Day{}
	}
	return s, nil
}

// count adds d to today's counts. A count that can't be written is let
// go: it is never worth failing what was counted.
func count(d *Day) {
	if d.empty() {
		return
	}
	statsMu.Lock()
	defer statsMu.Unlock()
	s, err := readStats()
	if err != nil {
		return
	}
	t := now()
	day := dayOf(t)
	if s.Since == "" {
		s.Since = day
	}
	if s.Days[day] == nil {
		s.Days[day] = &Day{}
	}
	s.Days[day].add(d)
	cut := dayOf(t.AddDate(0, 0, -keepDays))
	for k := range s.Days {
		if k < cut {
			delete(s.Days, k)
		}
	}
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(statsPath()), 0o700)
	edit.WriteAtomic(statsPath(), b)
}

// tally is the counts a change makes, kept on f and counted once it is
// written (change).
func (f *file) tally() *Day {
	if f.counts == nil {
		f.counts = &Day{}
	}
	return f.counts
}

// leaves counts m leaving use by who, when it was in use.
func (f *file) leaves(m *Memory, by string) {
	if live(m) {
		f.tally().cleaned(by)
	}
}

// StatDay is one day of a Report.
type StatDay struct {
	Date string `json:"day"`
	Day
	// Recorded is a day counted (on or after Since); Backfilled one
	// before, whose Created comes from the memory files' dates
	Recorded   bool `json:"recorded"`
	Backfilled bool `json:"backfilled,omitempty"`
}

// Report is the last days of counts, oldest first, today last, and
// memory as it is now.
type Report struct {
	Days  []StatDay `json:"days"`
	Since string    `json:"since,omitempty"`
	// Active and Candidates are the memories in use and to review now
	Active     int `json:"active"`
	Candidates int `json:"candidates"`
	// Used are the memories in use that a search returned within the
	// days asked for
	Used int `json:"used"`
}

// Stats is the last n days' counts (1 to keepDays).
func Stats(n int) (*Report, error) {
	n = max(1, min(n, keepDays))
	f, err := view()
	if err != nil {
		return nil, err
	}
	statsMu.Lock()
	s, err := readStats()
	statsMu.Unlock()
	if err != nil {
		return nil, err
	}
	t := now()
	first := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).AddDate(0, 0, -(n - 1))
	r := &Report{Since: s.Since}
	// what the files say was made each day, for the days before Since:
	// a memory a tidy merge made (it has its first member's date, and
	// members superseded by it) is that member's, already counted
	made := map[string]map[string]int{}
	into := map[string]time.Time{}
	for _, m := range f.Memories {
		if m.SupersededBy != "" {
			if at, ok := into[m.SupersededBy]; !ok || m.Created.Before(at) {
				into[m.SupersededBy] = m.Created
			}
		}
	}
	for _, m := range f.Memories {
		switch m.Status {
		case Active:
			r.Active++
			if !m.Used.IsZero() && !m.Used.Before(first) {
				r.Used++
			}
		case Candidate:
			r.Candidates++
		}
		if at, ok := into[m.ID]; ok && at.Equal(m.Created) {
			continue
		}
		d := dayOf(m.Created.In(t.Location()))
		if made[d] == nil {
			made[d] = map[string]int{}
		}
		made[d][m.Origin]++
	}
	for i := range n {
		day := dayOf(first.AddDate(0, 0, i))
		sd := StatDay{Date: day}
		switch {
		case s.Since != "" && day >= s.Since:
			sd.Recorded = true
			if d := s.Days[day]; d != nil {
				sd.Day.add(d)
			}
		case len(made[day]) > 0:
			sd.Backfilled = true
			sd.Created = made[day]
		}
		r.Days = append(r.Days, sd)
	}
	return r, nil
}

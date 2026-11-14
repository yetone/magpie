package maskrec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// A record is made only for a request masking changed, so an agent whose
// requests carried nothing to mask had none, and the page couldn't tell
// "masked, nothing found" from "not masked" (eiyuuzaki2333 on X: Pi's
// calls were on the Gateway page, only dsh's on the Masking page).
// Checked counts each agent's turns by day while masking is on: the ones
// masking looked over before they left, and the ones that went as
// written to a model the user set to go unmasked. A count holds no text.
//
// A day's counts are a file of their own beside its records,
// checked-<day>.json, written over whole (temp and rename) at most every
// checkedEvery while turns come in, kept and cleared as the records are.

// checkedEvery is how long a count waits in memory before its day's file
// is written again.
const checkedEvery = 5 * time.Second

// Looked is an agent's turns over some days, as Checked counted them.
type Looked struct {
	Name string `json:"name"`
	// Looked: turns masking looked over before they left; Masked: those
	// it changed, each with a record
	Looked int `json:"looked"`
	Masked int `json:"masked"`
	// Whole: turns sent as written to a model the user set to go unmasked
	Whole int `json:"whole,omitempty"`
}

// tally is an agent's counts in a day's file.
type tally struct {
	Looked int `json:"looked,omitempty"`
	Masked int `json:"masked,omitempty"`
	Whole  int `json:"whole,omitempty"`
}

var checked struct {
	mu     sync.Mutex
	days   map[string]map[string]*tally // day → agent → counts, as read and since
	dirty  map[string]bool
	timer  *time.Timer
	pruned time.Time
}

// Checked counts one turn of agent's that went to a vendor while masking
// was on: masked says masking looked it over (changed says it changed
// something, and the turn has a record), else it went as written to a
// model the user set to go unmasked. A turn with no agent named is
// counted as "other".
func Checked(agent string, masked, changed bool) {
	if agent == "" {
		agent = "other"
	}
	d := clock().Local().Format(dayForm)
	checked.mu.Lock()
	defer checked.mu.Unlock()
	c := checkedDay(d)[agent]
	if c == nil {
		c = &tally{}
		checked.days[d][agent] = c
	}
	switch {
	case !masked:
		c.Whole++
	case changed:
		c.Looked++
		c.Masked++
	default:
		c.Looked++
	}
	if inTest() {
		return
	}
	if checked.dirty == nil {
		checked.dirty = map[string]bool{}
	}
	checked.dirty[d] = true
	if checked.timer == nil {
		checked.timer = time.AfterFunc(checkedEvery, flushChecked)
	}
}

// checkedDay is day d's counts, read from its file the first time;
// checked.mu is held.
func checkedDay(d string) map[string]*tally {
	if checked.days == nil {
		checked.days = map[string]map[string]*tally{}
	}
	if m := checked.days[d]; m != nil {
		return m
	}
	m := map[string]*tally{}
	if !inTest() {
		// a file that can't be read or parsed counts from nothing: it is
		// written over with what is counted from now, never a record
		if b, err := os.ReadFile(checkedPath(Dir(), d)); err == nil {
			_ = json.Unmarshal(b, &m)
			for k, v := range m {
				if v == nil {
					delete(m, k)
				}
			}
		}
	}
	checked.days[d] = m
	return m
}

func checkedPath(dir, d string) string { return filepath.Join(dir, "checked-"+d+".json") }

// flushChecked writes each day counted since it was last written.
func flushChecked() {
	checked.mu.Lock()
	defer checked.mu.Unlock()
	checked.timer = nil
	dir := Dir()
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	for d := range checked.dirty {
		b, err := json.Marshal(checked.days[d])
		if err == nil && writeWhole(checkedPath(dir, d), b) == nil {
			delete(checked.dirty, d)
		}
	}
	if now := clock(); now.Sub(checked.pruned) > time.Hour {
		checked.pruned = now
		pruneChecked(dir, now)
	}
}

// writeWhole writes b to path through a temp file renamed over it, so a
// reader never has half of it.
func writeWhole(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".checked-*")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if e := f.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// pruneChecked drops the days' counts older than keepDays.
func pruneChecked(dir string, now time.Time) {
	oldest := now.Local().AddDate(0, 0, -keepDays).Format(dayForm)
	for _, f := range checkedFiles(dir) {
		d := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "checked-"), ".json")
		if d < oldest {
			os.Remove(f)
			delete(checked.days, d)
		}
	}
}

func checkedFiles(dir string) []string {
	es, _ := os.ReadDir(dir)
	var out []string
	for _, e := range es {
		n := e.Name()
		if d, ok := strings.CutPrefix(n, "checked-"); ok {
			if d, ok := strings.CutSuffix(d, ".json"); ok {
				if _, err := time.Parse(dayForm, d); err == nil {
					out = append(out, filepath.Join(dir, n))
				}
			}
		}
	}
	return out
}

// clearChecked deletes the counts, on disk and in memory, as Clear does
// the records; the files that can't be removed are its errors.
func clearChecked() []error {
	checked.mu.Lock()
	defer checked.mu.Unlock()
	var errs []error
	for _, f := range checkedFiles(Dir()) {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	checked.days, checked.dirty = nil, nil
	return errs
}

// lookedOver is each agent's counts over days, the most first.
func lookedOver(days []string) []Looked {
	checked.mu.Lock()
	defer checked.mu.Unlock()
	by := map[string]*Looked{}
	for _, d := range days {
		for name, c := range checkedDay(d) {
			l := by[name]
			if l == nil {
				l = &Looked{Name: name}
				by[name] = l
			}
			l.Looked += c.Looked
			l.Masked += c.Masked
			l.Whole += c.Whole
		}
	}
	out := []Looked{}
	for _, l := range by {
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Looked+a.Whole != b.Looked+b.Whole {
			return a.Looked+a.Whole > b.Looked+b.Whole
		}
		return a.Name < b.Name
	})
	return out
}

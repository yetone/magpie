package maskrec

import (
	"os"
	"path/filepath"
	"sort"
	"time"
)

// dayAgg is what a day's file holds, summed: read once, then kept up as
// Add writes to it.
type dayAgg struct {
	size      int64
	requests  int
	n         int
	warned    int // places a value went as written, the user told
	blocked   int // requests held back
	hours     [24]int
	kinds     map[string]int // each place counted
	vals      map[string]*valAgg
	agents    map[string]map[string]int // agent → placeholder → places
	providers map[string]map[string]int
}

type valAgg struct {
	kind, by, head string
	len            int
	n, requests    int
	last           time.Time
}

func newDay() *dayAgg {
	return &dayAgg{kinds: map[string]int{}, vals: map[string]*valAgg{}, agents: map[string]map[string]int{}, providers: map[string]map[string]int{}}
}

func (a *dayAgg) add(r Record) {
	a.requests++
	a.n += r.N
	a.hours[r.Time.Local().Hour()] += r.N
	if r.Blocked {
		a.blocked++
	}
	for _, v := range r.Values {
		if v.Action == "warn" { // not masked: told, not kept as one
			a.warned += v.N
			continue
		}
		a.kinds[v.Kind] += v.N
		x := a.vals[v.Placeholder]
		if x == nil {
			x = &valAgg{kind: v.Kind, by: v.By, head: v.Head, len: v.Len}
			a.vals[v.Placeholder] = x
		} else if x.by == "known" && v.By != "known" {
			x.by = v.By
		}
		x.n += v.N
		x.requests++
		if r.Time.After(x.last) {
			x.last = r.Time
		}
		addTo(a.agents, r.Agent, v)
		addTo(a.providers, r.Provider, v)
	}
}

func addTo(m map[string]map[string]int, name string, v Value) {
	if name == "" {
		return
	}
	if m[name] == nil {
		m[name] = map[string]int{}
	}
	m[name][v.Placeholder] += v.N
}

// day is the sum of path, read again only when it has changed size.
func day(path string) *dayAgg {
	fi, err := os.Stat(path)
	if err != nil {
		return newDay()
	}
	if store.days == nil {
		store.days = map[string]*dayAgg{}
	}
	if a := store.days[path]; a != nil && a.size == fi.Size() {
		return a
	}
	a := newDay()
	a.size = read(path, a.add)
	store.days[path] = a
	return a
}

// Day is one day's count.
type Day struct {
	Day      string `json:"day"`
	Requests int    `json:"requests"`
	N        int    `json:"n"`        // values swapped, each place counted
	Distinct int    `json:"distinct"` // different values
}

// Count is a kind's, an agent's or a provider's share.
type Count struct {
	Name     string `json:"name"`
	N        int    `json:"n"`
	Distinct int    `json:"distinct"`
}

// Seen is one different value, as the record has it.
type Seen struct {
	Placeholder string    `json:"p"`
	Kind        string    `json:"kind"`
	By          string    `json:"by"`
	Head        string    `json:"head,omitempty"`
	Len         int       `json:"len"`
	N           int       `json:"n"`
	Requests    int       `json:"requests"`
	Last        time.Time `json:"last"`
}

// Summary is what masking did over some days.
type Summary struct {
	Days      []Day          `json:"days"` // oldest first, each day of them
	Requests  int            `json:"requests"`
	N         int            `json:"n"`
	Distinct  int            `json:"distinct"`
	Warned    int            `json:"warned,omitempty"`  // places a value went as written, the user told
	Blocked   int            `json:"blocked,omitempty"` // requests held back
	Hours     [24]int        `json:"hours"`             // places, by the hour of the day
	Kinds     []Count        `json:"kinds"`
	By        map[string]int `json:"by"` // different values by the finder that found each
	Agents    []Count        `json:"agents"`
	Providers []Count        `json:"providers"`
	Values    []Seen         `json:"values"` // the most masked first, at most topValues
	Since     string         `json:"since,omitempty"`
	// Checked: each agent's turns masking looked over, or that went as
	// written to a model set to go unmasked, nothing masked or not
	Checked []Looked `json:"checked"`
}

const topValues = 60

// Sum is the summary of the days days up to today.
func Sum(days int) Summary {
	store.mu.Lock()
	defer store.mu.Unlock()
	if days <= 0 {
		days = 7
	}
	now := clock().Local()
	dir := Dir()
	out := Summary{By: map[string]int{}}
	if fs := files(dir); len(fs) > 0 {
		out.Since = fs[0].day
	}
	var dayNames []string
	vals := map[string]*Seen{}
	kinds := map[string]*Count{}
	kindVals := map[string]map[string]bool{}
	who := map[string]map[string]int{}
	whom := map[string]map[string]int{}
	for i := days - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i).Format(dayForm)
		dayNames = append(dayNames, d)
		var a *dayAgg
		if inTest() {
			a = memDay(d)
		} else {
			a = day(filepath.Join(dir, d+".jsonl"))
		}
		out.Days = append(out.Days, Day{Day: d, Requests: a.requests, N: a.n, Distinct: len(a.vals)})
		out.Requests += a.requests
		out.N += a.n
		out.Warned += a.warned
		out.Blocked += a.blocked
		for h, n := range a.hours {
			out.Hours[h] += n
		}
		for k, n := range a.kinds {
			if kinds[k] == nil {
				kinds[k] = &Count{Name: k}
				kindVals[k] = map[string]bool{}
			}
			kinds[k].N += n
		}
		for p, v := range a.vals {
			s := vals[p]
			if s == nil {
				s = &Seen{Placeholder: p, Kind: v.kind, By: v.by, Head: v.head, Len: v.len}
				vals[p] = s
			} else if s.By == "known" && v.by != "known" {
				s.By = v.by
			}
			s.N += v.n
			s.Requests += v.requests
			if v.last.After(s.Last) {
				s.Last = v.last
			}
			kindVals[v.kind][p] = true
		}
		merge(who, a.agents)
		merge(whom, a.providers)
	}
	out.Distinct = len(vals)
	for _, s := range vals {
		out.By[s.By]++
		out.Values = append(out.Values, *s)
	}
	sort.Slice(out.Values, func(i, j int) bool {
		a, b := out.Values[i], out.Values[j]
		if a.N != b.N {
			return a.N > b.N
		}
		return a.Placeholder < b.Placeholder
	})
	if len(out.Values) > topValues {
		out.Values = out.Values[:topValues]
	}
	for k, c := range kinds {
		c.Distinct = len(kindVals[k])
		out.Kinds = append(out.Kinds, *c)
	}
	out.Kinds = ranked(out.Kinds)
	out.Agents = counts(who)
	out.Providers = counts(whom)
	out.Checked = lookedOver(dayNames)
	return out
}

func merge(into, from map[string]map[string]int) {
	for name, ps := range from {
		if into[name] == nil {
			into[name] = map[string]int{}
		}
		for p, n := range ps {
			into[name][p] += n
		}
	}
}

func counts(m map[string]map[string]int) []Count {
	var out []Count
	for name, ps := range m {
		c := Count{Name: name, Distinct: len(ps)}
		for _, n := range ps {
			c.N += n
		}
		out = append(out, c)
	}
	return ranked(out)
}

func ranked(cs []Count) []Count {
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].N != cs[j].N {
			return cs[i].N > cs[j].N
		}
		return cs[i].Name < cs[j].Name
	})
	if cs == nil {
		cs = []Count{}
	}
	return cs
}

// memDay is day d of the records in memory, for a test binary that writes
// none to disk.
func memDay(d string) *dayAgg {
	a := newDay()
	for _, r := range store.snips {
		if r.Time.Local().Format(dayForm) == d {
			r.Snips = nil
			a.add(r)
		}
	}
	return a
}

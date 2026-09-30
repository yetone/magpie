package sessions

import (
	"fmt"
	"sort"
	"time"
)

// Rollup is Stats summed under a model and a folder ("" for every one),
// as the app's Sessions view sums them: for the CLI and the TUI.
type Rollup struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Model  string `json:"model,omitempty"`
	Folder string `json:"folder,omitempty"`
	Tokens
	Cost     float64  `json:"cost"`     // USD at the effective price, of the priced models
	Unpriced []string `json:"unpriced"` // models that spent tokens and have no known price
	// Active is the seconds the sessions were at work; -1 under a model,
	// as active time isn't kept by model.
	Active   int64      `json:"active"`
	DaysUsed int        `json:"days_used"` // dates with usage
	Days     []DayTotal `json:"days"`      // every date From…To, empty ones too
	Models   []Share    `json:"models"`    // under the folder, the most tokens first
	Folders  []Share    `json:"folders"`   // under the model, the most tokens first
}

// DayTotal is one date's usage under a Rollup's filters.
type DayTotal struct {
	Date string `json:"date"`
	Tokens
	Cost   float64 `json:"cost"`
	Active int64   `json:"active"` // seconds; -1 under a model
}

// Share is one model's or folder's part of a Rollup.
type Share struct {
	Name string `json:"name"`
	Tokens
	Cost float64 `json:"cost"`
}

// Duration is active seconds in hours and minutes: 3h 12m, 40m, <1m;
// — when not kept.
func Duration(sec int64) string {
	m := (sec + 30) / 60
	switch {
	case sec < 0:
		return "—"
	case sec == 0:
		return "0"
	case m < 1:
		return "<1m"
	case m < 60:
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh %dm", m/60, m%60)
}

// Spent is the tokens a Rollup counts: input and output, as the app does.
func (t Tokens) Spent() int { return t.Input + t.Output }

// Sum rolls the stats up under a model and a folder, "" for every one.
func (s Stats) Sum(model, folder string) Rollup {
	r := Rollup{From: s.From, To: s.To, Model: model, Folder: folder, Unpriced: []string{}, Days: []DayTotal{}, Models: []Share{}, Folders: []Share{}}
	at := map[string]int{}
	if first, err := time.ParseInLocation(time.DateOnly, s.From, time.Local); err == nil {
		last, _ := time.ParseInLocation(time.DateOnly, s.To, time.Local)
		for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
			at[d.Format(time.DateOnly)] = len(r.Days)
			r.Days = append(r.Days, DayTotal{Date: d.Format(time.DateOnly)})
		}
	}
	models, folders := map[string]*Share{}, map[string]*Share{}
	share := func(m map[string]*Share, name string, u Usage) {
		if m[name] == nil {
			m[name] = &Share{Name: name}
		}
		m[name].add(u.Tokens)
		m[name].Cost += u.Cost
	}
	unpriced := map[string]bool{}
	for _, d := range s.Days {
		i, ok := at[d.Date]
		if !ok {
			continue
		}
		used := false
		for _, u := range d.Usage {
			if folder == "" || u.Cwd == folder {
				share(models, u.Model, u)
			}
			if model == "" || u.Model == model {
				share(folders, u.Cwd, u)
			}
			if (model != "" && u.Model != model) || (folder != "" && u.Cwd != folder) {
				continue
			}
			used = true
			r.add(u.Tokens)
			r.Cost += u.Cost
			r.Days[i].add(u.Tokens)
			r.Days[i].Cost += u.Cost
			if !u.Priced {
				unpriced[u.Model] = true
			}
		}
		if used {
			r.DaysUsed++
		}
		for _, a := range d.Active {
			if folder == "" || a.Cwd == folder {
				r.Days[i].Active += a.Seconds
				r.Active += a.Seconds
			}
		}
	}
	if model != "" {
		r.Active = -1
		for i := range r.Days {
			r.Days[i].Active = -1
		}
	}
	for m := range unpriced {
		r.Unpriced = append(r.Unpriced, m)
	}
	sort.Strings(r.Unpriced)
	r.Models, r.Folders = ranked(models), ranked(folders)
	return r
}

// ranked is the shares, the most tokens first.
func ranked(m map[string]*Share) []Share {
	out := []Share{}
	for _, s := range m {
		if s.Spent() > 0 || s.CacheRead > 0 {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Spent() != out[j].Spent() {
			return out[i].Spent() > out[j].Spent()
		}
		return out[i].Name < out[j].Name
	})
	return out
}

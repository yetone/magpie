package usageapi

import (
	"fmt"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// One day of usage as a plugin's page reads it (usage:day): the day's
// totals, each agent's and, within each, its models', and the days before
// it that had calls, for a streak or an average day. A page that draws a
// day's statement (@magpie-community/page-day-card) reads it; nothing in it
// is a key, an account, a session or a prompt.

// DayAgents is how many agents a day lists, the most tokens first, and
// DayModels how many of an agent's models: a page draws a card, not a table.
const (
	DayAgents = 12
	DayModels = 12
)

// DayTotals are the counts of a day, an agent or a model.
type DayTotals struct {
	Calls      int     `json:"calls"`
	Errors     int     `json:"errors"`
	Input      int     `json:"input"`
	Output     int     `json:"output"`
	CacheRead  int     `json:"cacheRead"`
	CacheWrite int     `json:"cacheWrite"`
	Reasoning  int     `json:"reasoning"`
	Cost       float64 `json:"cost"` // USD at the effective price, of the priced calls
	// Unpriced are calls with tokens but no known price, not in Cost
	Unpriced int `json:"unpriced"`
	// CacheUnknown is the prompt's tokens of calls whose vendor counts no
	// cache, which a hit rate leaves out (#3464)
	CacheUnknown int `json:"cacheUnknown"`
}

func dayTotals(t usage.Totals) DayTotals {
	return DayTotals{
		Calls: t.Calls, Errors: t.Errors, Input: t.Input, Output: t.Output,
		CacheRead: t.CacheRead, CacheWrite: t.CacheWrite, Reasoning: t.Reasoning,
		Cost: t.Cost, Unpriced: t.Unpriced, CacheUnknown: t.CacheUnknown,
	}
}

// DayModel is a model's share of an agent's day.
type DayModel struct {
	Model        string `json:"model"`
	Provider     string `json:"provider"`     // its id
	ProviderName string `json:"providerName"` // as the Usage page names it
	Icon         string `json:"icon"`         // the provider's, as the Usage page draws it
	DayTotals
}

// DayAgent is an agent's day, and its models'.
type DayAgent struct {
	ID   string `json:"id"` // "" for calls that named no agent
	Name string `json:"name"`
	Icon string `json:"icon"`
	DayTotals
	Models []DayModel `json:"models"`
	// MoreModels is how many of its models DayModels left out; their
	// counts are in the agent's
	MoreModels int `json:"moreModels,omitempty"`
}

// DayCount is a day that had calls.
type DayCount struct {
	Date   string  `json:"date"`
	Calls  int     `json:"calls"`
	Tokens int     `json:"tokens"` // in, out and through the cache
	Cost   float64 `json:"cost"`
}

// Day is a day's usage.
type Day struct {
	Date  string `json:"date"`  // YYYY-MM-DD, in the zone asked for
	Today string `json:"today"` // the zone's today
	DayTotals
	Agents     []DayAgent `json:"agents"`
	MoreAgents int        `json:"moreAgents,omitempty"`
	// Days are the days with calls up to Date, oldest first, over the
	// heatmap's span (usage.HeatmapWeeks)
	Days []DayCount `json:"days"`
}

// DayOf is the day date (YYYY-MM-DD; "" for today) in zone (nil is this
// computer's). A day after today is an error.
func DayOf(date string, zone *time.Location) (Day, error) {
	if zone == nil {
		zone = time.Local
	}
	today := usage.Clock().In(zone).Format(time.DateOnly)
	if date == "" {
		date = today
	}
	d, err := time.ParseInLocation(time.DateOnly, date, zone)
	if err != nil {
		return Day{}, fmt.Errorf("no date %q: YYYY-MM-DD", date)
	}
	date = d.Format(time.DateOnly)
	if date > today {
		return Day{}, fmt.Errorf("%s is after today, %s", date, today)
	}
	p, _ := usage.RangeOf(date, date)
	f := usage.Filter{Zone: zone}
	all := usage.QueryPage(p, f, 0, 1)
	out := Day{Date: date, Today: today, DayTotals: dayTotals(all.Sum), Agents: []DayAgent{}, Days: []DayCount{}}

	agents := clients()
	face := providerFaces(provider.All())
	for i, s := range all.By["agent"] {
		if i == DayAgents {
			out.MoreAgents = len(all.By["agent"]) - DayAgents
			break
		}
		a := DayAgent{ID: s.ID, Name: s.ID, Icon: "generic", DayTotals: dayTotals(s.Totals), Models: []DayModel{}}
		if c := agents[s.ID]; c != nil {
			a.Name, a.Icon = c.Name, c.Icon
		}
		af := f
		af.Agent = s.ID
		if s.ID == "" {
			// a filter of "" keeps every agent: the calls that named none
			// are this group's own, and are listed by model without it
			af.Agent = ""
		}
		var models []usage.Share
		if s.ID != "" || len(all.By["agent"]) == 1 {
			models = usage.QueryPage(p, af, 0, 1).By["modelAt"]
		}
		for j, m := range models {
			if j == DayModels {
				a.MoreModels = len(models) - DayModels
				break
			}
			prov, model, _ := strings.Cut(m.ID, "/")
			pf := face(prov)
			a.Models = append(a.Models, DayModel{Model: model, Provider: prov, ProviderName: pf.Name, Icon: pf.Icon, DayTotals: dayTotals(m.Totals)})
		}
		out.Agents = append(out.Agents, a)
	}

	for _, h := range usage.HeatmapOf(f).Days {
		if h.Date <= date && h.Calls > 0 {
			out.Days = append(out.Days, DayCount{Date: h.Date, Calls: h.Calls, Tokens: h.Tokens, Cost: h.Cost})
		}
	}
	return out, nil
}

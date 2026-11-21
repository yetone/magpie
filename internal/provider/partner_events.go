package provider

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/settings"
)

// A partner pays to be listed, so how often it is seen and picked is
// counted for it: per day (UTC), per partner, how many times the add sheet
// showed it, its row was opened, its key page was opened, and a provider
// was added from it. Nothing else is kept, and the counts go with the
// day's stats event (internal/stats), only for days that have ended, under
// the same switches: off with the stats, or with what magpie is used with.

// What is counted for a partner.
const (
	PartnerShown  = "shown"  // the add sheet (the Plugins page) listed it
	PartnerOpened = "opened" // its row was opened
	PartnerKeys   = "keys"   // its key page was opened
	PartnerAdded  = "added"  // a provider was added from it (its plugin installed)
)

// partnerCaps is the most counted of each a day: a count, not a log of
// what one user did, and a page drawn in a loop can't swell it.
var partnerCaps = map[string]int{PartnerShown: 20, PartnerOpened: 20, PartnerKeys: 20, PartnerAdded: 5}

// partnerDays are the days kept unsent at most.
const partnerDays = 14

// PartnerCount is one day's count of one kind for one partner.
type PartnerCount struct {
	Day   string `json:"day"` // 2006-01-02, UTC
	ID    string `json:"id"`
	What  string `json:"what"`
	Count int    `json:"count"`
	// Slot is where the partner is listed: "plugins" (the Plugins page),
	// "providers" (the add sheet), or "" for one no longer listed.
	Slot string `json:"slot,omitempty"`
}

// PageCount is one day's count of how often a page was opened.
type PageCount struct {
	Day   string `json:"day"` // 2006-01-02, UTC
	Page  string `json:"page"`
	Count int    `json:"count"`
}

type partnerCounts struct {
	// Days is day → partner → what → count.
	Days map[string]map[string]map[string]int `json:"days"`
	// Pages is day → page → times opened.
	Pages map[string]map[string]int `json:"pages,omitempty"`
}

// The pages counted, and the most counted of each a day. The Plugins page
// is where the plugin partners are listed, so how often it is opened is
// what their places are worth.
const PagePlugins = "plugins"

var pageCaps = map[string]int{PagePlugins: 20}

var partnerCountMu sync.Mutex

func partnerCountFile() string { return filepath.Join(appdir.Cache(), "partner-counts.json") }

// partnerCounting reports whether the user lets it be counted: the stats
// on, and what magpie is used with sent with them. internal/stats reads
// the same switches.
func partnerCounting() bool {
	for _, k := range []string{"DO_NOT_TRACK", "MAGPIE_NO_STATS"} {
		if v := os.Getenv(k); v != "" && v != "0" && v != "false" {
			return false
		}
	}
	s := settings.Load()
	return !s.NoStats && !s.NoUsageStats
}

// livePartner reports whether id is a partner listed now, the add sheet's
// or the Plugins page's: only those are counted, never a provider of the
// user's own.
func livePartner(id string) bool {
	for _, p := range listedPartners() {
		if p.ID == id {
			return true
		}
	}
	return false
}

// countPluginPartner counts an install of pkg for the partner whose
// plugin it is, if one is listed now.
func countPluginPartner(pkg string) {
	for _, p := range PluginPartners() {
		if p.Plugin == pkg {
			CountPartner(PartnerAdded, p.ID)
		}
	}
}

// CountPartner counts what for each of the partners ids, today.
func CountPartner(what string, ids ...string) {
	limit := partnerCaps[what]
	if limit == 0 || !partnerCounting() {
		return
	}
	var live []string
	for _, id := range ids {
		if livePartner(id) && !slices.Contains(live, id) {
			live = append(live, id)
		}
	}
	if len(live) == 0 {
		return
	}
	partnerCountMu.Lock()
	defer partnerCountMu.Unlock()
	c := readPartnerCounts()
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	for d := range c.Days {
		if t, err := time.Parse("2006-01-02", d); err != nil || now.Sub(t) > partnerDays*24*time.Hour {
			delete(c.Days, d)
		}
	}
	if c.Days[day] == nil {
		c.Days[day] = map[string]map[string]int{}
	}
	changed := false
	for _, id := range live {
		if c.Days[day][id] == nil {
			c.Days[day][id] = map[string]int{}
		}
		if c.Days[day][id][what] < limit {
			c.Days[day][id][what]++
			changed = true
		}
	}
	if changed {
		writePartnerCounts(c)
	}
}

// CountPage counts an opening of page, today, under the partners'
// switches.
func CountPage(page string) {
	limit := pageCaps[page]
	if limit == 0 || !partnerCounting() {
		return
	}
	partnerCountMu.Lock()
	defer partnerCountMu.Unlock()
	c := readPartnerCounts()
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	for d := range c.Pages {
		if t, err := time.Parse("2006-01-02", d); err != nil || now.Sub(t) > partnerDays*24*time.Hour {
			delete(c.Pages, d)
		}
	}
	if c.Pages[day] == nil {
		c.Pages[day] = map[string]int{}
	}
	if c.Pages[day][page] >= limit {
		return
	}
	c.Pages[day][page]++
	writePartnerCounts(c)
}

// PageCounts are the page counts of the days before today, to be sent.
func PageCounts(now time.Time) []PageCount {
	partnerCountMu.Lock()
	defer partnerCountMu.Unlock()
	c := readPartnerCounts()
	today := now.UTC().Format("2006-01-02")
	var out []PageCount
	for day, pages := range c.Pages {
		if day >= today {
			continue
		}
		for page, n := range pages {
			out = append(out, PageCount{Day: day, Page: page, Count: n})
		}
	}
	slices.SortFunc(out, func(a, b PageCount) int {
		return cmp.Or(cmp.Compare(a.Day, b.Day), cmp.Compare(a.Page, b.Page))
	})
	return out
}

// partnerSlot is where the partner id is listed now: "plugins",
// "providers", or "" when it isn't.
func partnerSlot(id string) string {
	for _, p := range listedPartners() {
		if p.ID == id {
			if p.IsPlugin() {
				return "plugins"
			}
			return "providers"
		}
	}
	return ""
}

// PartnerCounts are the counts of the days before today, to be sent.
func PartnerCounts(now time.Time) []PartnerCount {
	partnerCountMu.Lock()
	defer partnerCountMu.Unlock()
	c := readPartnerCounts()
	today := now.UTC().Format("2006-01-02")
	var out []PartnerCount
	slots := map[string]string{}
	for day, ids := range c.Days {
		if day >= today {
			continue
		}
		for id := range ids {
			if _, ok := slots[id]; !ok {
				slots[id] = partnerSlot(id)
			}
		}
		for id, whats := range ids {
			for what, n := range whats {
				out = append(out, PartnerCount{Day: day, ID: id, What: what, Count: n, Slot: slots[id]})
			}
		}
	}
	slices.SortFunc(out, func(a, b PartnerCount) int {
		if a.Day != b.Day {
			return cmp.Compare(a.Day, b.Day)
		}
		if a.ID != b.ID {
			return cmp.Compare(a.ID, b.ID)
		}
		return cmp.Compare(a.What, b.What)
	})
	return out
}

// PartnerCountsSent forgets the days that were sent: those before now's.
func PartnerCountsSent(now time.Time) {
	partnerCountMu.Lock()
	defer partnerCountMu.Unlock()
	c := readPartnerCounts()
	today := now.UTC().Format("2006-01-02")
	sent := false
	for d := range c.Days {
		if d < today {
			delete(c.Days, d)
			sent = true
		}
	}
	for d := range c.Pages {
		if d < today {
			delete(c.Pages, d)
			sent = true
		}
	}
	if sent {
		writePartnerCounts(c)
	}
}

func readPartnerCounts() partnerCounts {
	var c partnerCounts
	if b, err := os.ReadFile(partnerCountFile()); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	if c.Days == nil {
		c.Days = map[string]map[string]map[string]int{}
	}
	if c.Pages == nil {
		c.Pages = map[string]map[string]int{}
	}
	return c
}

func writePartnerCounts(c partnerCounts) {
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(partnerCountFile()), 0o755)
	tmp := partnerCountFile() + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, partnerCountFile())
	}
}

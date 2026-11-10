package provider

// How many of its credits an account used each day (#568, #3321). A
// vendor's meter tells only what the cycle has used so far against what it
// grants ("500.52 / 5600"), so each reading of it is kept beside the one
// before, and what was used in between is counted on the day of the later
// one. That is the whole of it while magpie reads the meter (whenever the
// Usage page, the tray or routing asks, a minute apart at most); what is
// used while it doesn't is counted on the day it next does.
//
// Which windows are credits is told by their shape, not by the vendor: a
// count of used against granted (amount of limit, or a display "x / y")
// in credits (積分, credits) or money ($, usd, ¥), from its unit, the
// display's own word or symbol, or else its name. A share alone, tokens,
// requests or a balance with nothing granted count nothing. Each such
// window is counted on its own, by provider, account and window name, so
// a card's several pools (Qoder's Add-on credits, Kiro's Free trial) never
// mix. A pool's breakdown set aside (CodeArts' buckets under its total)
// is told by what the readings used: one set aside counts unless the
// card's counted windows in its unit used at least as much over the same
// span, which makes it their credits again; a pool set aside on its own
// (WorkBuddy's Extra credits, which it spends while the plan's stand
// still: jiabubu) counts as any other, and so do the pools set aside that
// together grant what a counted window does, its whole (poolsOfWhole). Of one allowance seen over several
// spans (Open Design's 5 hours and week) only the longest counts.
//
// A reading that has used less than the one before is a cycle started
// again or a credit pack gone: what was used between the two can't be
// told apart from what went, so nothing is counted for it and the new
// reading is where counting goes on from. A pack added (a check-in's
// credits, one bought) only grows what is granted, which counts nothing.
// A reading that failed, or one kept from before, is no reading.
//
// Kept in credits-daily.json, by provider, account and window name, the
// readings and the days' sums only — no token, nothing of the vendor's but
// the two figures. WorkBuddy's, kept by provider and account alone before
// #3321, are its Credits window's.

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/lastgood"
	"github.com/yetone/magpie/internal/lazyre"
)

// DailyCredits is what one of an account's credits windows went to day
// by day.
type DailyCredits struct {
	// Since is the day magpie first read the account's credits: the days
	// before it aren't known, not unused.
	Since string `json:"since"`
	// Days are the days of the last creditsShown with credits used, oldest
	// first.
	Days []DayCredits `json:"days"`
	// Window is the card's window they are of ("Credits", "Add-on
	// credits"), and Unit a currency's code when it counts money ("usd"),
	// "" when credits.
	Window string `json:"window,omitempty"`
	Unit   string `json:"unit,omitempty"`
}

// DayCredits is one day's credits used ("2006-01-02", the local day).
type DayCredits struct {
	Day  string  `json:"day"`
	Used float64 `json:"used"`
}

const (
	creditsKept  = 400 // days kept in the file
	creditsShown = 120 // days a card is told of
)

type creditsReading struct {
	Used  float64   `json:"used"`
	Total float64   `json:"total"`
	At    time.Time `json:"at"`
}

type creditsAccount struct {
	Since string             `json:"since"`
	Last  creditsReading     `json:"last"`
	Days  map[string]float64 `json:"days,omitempty"`
	// Part is set on a window set aside that the last reading it used
	// anything in took for another window's credits broken down (see
	// noteDailyCredits); before it used any, on one set aside beside a
	// window in its unit that isn't.
	Part bool `json:"part,omitempty"`
}

// creditsMu keeps two readings in one magpie from writing the file at once;
// each reads it afresh, so another magpie's (the CLI's) is kept too.
var creditsMu sync.Mutex

func creditsPath() string { return filepath.Join(filepath.Dir(Path()), "credits-daily.json") }

func parseCredits(b []byte) (map[string]*creditsAccount, error) {
	accts := map[string]*creditsAccount{}
	if err := json.Unmarshal(b, &accts); err != nil || accts == nil {
		return map[string]*creditsAccount{}, nil
	}
	return accts, nil
}

// creditWindow is a window of a card that counts credits (or money):
// used of total, in unit ("credits", or a currency's code in lower case).
type creditWindow struct {
	name, unit  string
	used, total float64
	aside       bool
	span        time.Duration
}

// creditUnit is what a window's unit, or a display's word or symbol, says
// it counts: "credits", a currency ("usd", "cny", "eur"), or "" for
// anything else (tokens, requests, ACUs) and nothing said.
func creditUnit(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "":
		return ""
	case "$", "usd", "us$", "dollar", "dollars":
		return "usd"
	case "¥", "￥", "cny", "rmb", "元", "yuan":
		return "cny"
	case "€", "eur", "euro", "euros":
		return "eur"
	}
	if strings.Contains(s, "credit") || strings.Contains(s, "积分") || strings.Contains(s, "積分") || strings.Contains(s, "点数") {
		return "credits"
	}
	return ""
}

// creditsDisplay is "used / total" with what vendors put about it: a
// currency sign, thousands' commas, a compact K/M/B/万/亿, an exponent
// (Qoder's 1.5e+06), and a unit word after; a note after a " · " (CodeArts'
// "· 300 即将过期", JoyCode's "· 剩余 …") is cut before it is read.
var creditsDisplay = lazyre.New(`(?i)^\s*([$¥￥€])?\s*([\d.,]*\d(?:e[+-]?\d+)?)\s*([kmb]\b|万|亿)?\s*/\s*([$¥￥€])?\s*([\d.,]*\d(?:e[+-]?\d+)?)\s*([kmb]\b|万|亿)?\s*(.*?)\s*$`)

func creditsNumber(n, scale string) (float64, bool) {
	if strings.Count(n, ".") > 1 {
		return 0, false
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(n, ",", ""), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	switch strings.ToLower(scale) {
	case "k":
		v *= 1e3
	case "m":
		v *= 1e6
	case "b":
		v *= 1e9
	case "万":
		v *= 1e4
	case "亿":
		v *= 1e8
	}
	return v, true
}

// displayCredits reads a display "used / total": the two figures and the
// unit its symbol or word names ("" when it names none).
func displayCredits(d string) (used, total float64, unit string, ok bool) {
	if i := strings.Index(d, "·"); i >= 0 {
		d = d[:i]
	}
	m := creditsDisplay.FindStringSubmatch(d)
	if m == nil {
		return 0, 0, "", false
	}
	u, ok1 := creditsNumber(m[2], m[3])
	t, ok2 := creditsNumber(m[5], m[6])
	if !ok1 || !ok2 {
		return 0, 0, "", false
	}
	sign := m[1]
	if sign == "" {
		sign = m[4]
	}
	if sign != "" {
		unit = creditUnit(sign)
	} else if m[7] != "" {
		unit = creditUnit(m[7])
		if unit == "" {
			unit = "-" // a word, and not credits: tokens, requests
		}
	}
	return u, t, unit, true
}

// creditOf is w as a credits window, when it is one: a count of used
// against granted, in credits or money.
func creditOf(w QuotaWindow) (creditWindow, bool) {
	if w.Unlimited || w.Text || w.Family != "" {
		return creditWindow{}, false
	}
	c := creditWindow{name: w.Name, aside: w.Aside, span: w.Span}
	shown := ""
	if w.Limit > 0 {
		c.used, c.total = w.Amount, w.Limit
	} else {
		u, t, unit, ok := displayCredits(w.Display)
		if !ok {
			return creditWindow{}, false // a share alone
		}
		c.used, c.total, shown = u, t, unit
	}
	switch {
	case w.Unit != "":
		c.unit = creditUnit(w.Unit)
	case shown == "" && w.Limit > 0:
		// amount of limit and no unit: the display may still say it
		if _, _, unit, ok := displayCredits(w.Display); ok {
			shown = unit
		}
		fallthrough
	default:
		if shown == "-" {
			return creditWindow{}, false
		}
		c.unit = shown
		if c.unit == "" {
			c.unit = creditUnit(w.Name)
		}
	}
	if c.unit == "" || c.total <= 0 || c.used < 0 || math.IsNaN(c.used) || math.IsInf(c.used, 0) || math.IsInf(c.total, 0) {
		return creditWindow{}, false
	}
	return c, true
}

// creditWindows are q's windows counted by the day, from a reading just
// made: none of a card that failed or one kept from before.
//
// Those set aside are among them: whether one is a pool of its own or the
// breakdown of another is told by what each reading used (see
// noteDailyCredits), not by its being set aside.
func creditWindows(q SubscriptionQuota) []creditWindow {
	if q.Error != "" || q.AsOf != nil {
		return nil // a reading kept from before isn't a new one
	}
	var all []creditWindow
	for _, w := range q.Windows {
		if c, ok := creditOf(w); ok && !slices.ContainsFunc(all, func(x creditWindow) bool { return x.name == c.name }) {
			all = append(all, c)
		}
	}
	// one allowance over several spans (5 hours, a week): the longest's
	// count alone, which a rolling short one's drops don't cut into
	for i := 0; i < len(all); i++ {
		for j := range all {
			if j != i && all[i].span > 0 && all[j].unit == all[i].unit && all[j].span > all[i].span {
				all = slices.Delete(all, i, i+1)
				i--
				break
			}
		}
	}
	return all
}

func creditsKey(q SubscriptionQuota, window string) string {
	return q.Provider + "|" + strings.ToLower(q.User) + "|" + window
}

// creditsOldKey is where an account's credits were kept before #3321,
// WorkBuddy's alone, its one Credits window's.
func creditsOldKey(q SubscriptionQuota) string {
	return q.Provider + "|" + strings.ToLower(q.User)
}

// noteDailyCredits keeps the readings of the credits windows among qs,
// made at now, and counts what each used since its last.
func noteDailyCredits(qs []SubscriptionQuota, now time.Time) {
	creditsMu.Lock()
	defer creditsMu.Unlock()
	path := creditsPath()
	accts := map[string]*creditsAccount{}
	if b, err := os.ReadFile(path); err == nil {
		accts, _ = parseCredits(b)
	}
	day := now.Local().Format("2006-01-02")
	changed := false
	for _, q := range qs {
		ws := creditWindows(q)
		pools := poolsOfWhole(ws)
		type step struct {
			c creditWindow
			a *creditsAccount
			d float64
		}
		var steps []step
		// what the card's windows used since their last readings, by unit:
		// those counted (n) and those set aside (aside)
		n, aside := map[string]float64{}, map[string]float64{}
		for _, c := range ws {
			key := creditsKey(q, c.name)
			a := accts[key]
			if old := accts[creditsOldKey(q)]; a == nil && old != nil && c.name == "Credits" {
				a = old // kept before the window was named: carried over
				accts[key] = a
				delete(accts, creditsOldKey(q))
				changed = true
			}
			if a == nil {
				// one set aside beside a counted window in its unit is
				// taken for that window's breakdown until it uses
				// credits that window doesn't; one of a whole's pools
				// never is
				part := c.aside && !pools[c.name] && slices.ContainsFunc(ws, func(x creditWindow) bool { return !x.aside && x.unit == c.unit })
				a = &creditsAccount{Since: day, Part: part}
				accts[key] = a
				a.Last = creditsReading{Used: c.used, Total: c.total, At: now}
				changed = true
				continue
			}
			if c.used == a.Last.Used && c.total == a.Last.Total {
				continue
			}
			d := max(c.used-a.Last.Used, 0)
			if c.aside {
				aside[c.unit] += d
			} else {
				n[c.unit] += d
			}
			steps = append(steps, step{c, a, d})
		}
		for _, s := range steps {
			c, a, d := s.c, s.a, s.d
			if c.aside && d > 0 {
				// A window set aside is a pool of its own (WorkBuddy's
				// Extra credits, Kiro's bonuses, jiabubu) unless the
				// counted windows in its unit used at least as much in
				// the same span: then it is their credits broken down
				// (CodeArts' buckets under its 总积分), counted there.
				a.Part = !pools[c.name] && n[c.unit] > 0 && aside[c.unit] <= n[c.unit]+0.005
				if a.Part {
					d = 0
				}
			}
			if d > 0 {
				if a.Days == nil {
					a.Days = map[string]float64{}
				}
				a.Days[day] = math.Round((a.Days[day]+d)*100) / 100
			}
			a.Last = creditsReading{Used: c.used, Total: c.total, At: now}
			changed = true
		}
	}
	if !changed {
		return
	}
	cut := now.Local().AddDate(0, 0, -creditsKept).Format("2006-01-02")
	for _, a := range accts {
		for d := range a.Days {
			if d < cut {
				delete(a.Days, d)
			}
		}
	}
	b, err := json.MarshalIndent(accts, "", "  ")
	if err != nil {
		return
	}
	// parseCredits reads a file that doesn't parse as no days: it is kept
	// aside before it is written over, and not written over when it can't be
	if lastgood.KeepUnparsed(path, lastgood.Into[map[string]*creditsAccount]) != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = writeFileAtomic(path, b)
}

// poolsOfWhole are the windows set aside that a counted window is the
// whole of: together, in its unit, they grant what it grants (WorkBuddy's
// Credits and Extra credits under its Total credits, plugin 0.2.24 on).
// Each is a pool of its own and counts its own days beside the whole's,
// so the pools' days add up to the whole's (yapechan on Discord: 今天 0
// for 积分 and 额外积分 beside 34.06 for 积分合计, the pools taken for the
// whole's breakdown and counted there). A bucket that is only part of
// what the whole grants (CodeArts' 基础积分 under its 总积分) stays its
// breakdown.
func poolsOfWhole(ws []creditWindow) map[string]bool {
	out := map[string]bool{}
	for _, whole := range ws {
		if whole.aside {
			continue
		}
		var of []string
		sum := 0.0
		for _, c := range ws {
			if c.aside && c.unit == whole.unit {
				of = append(of, c.name)
				sum += c.total
			}
		}
		if len(of) > 1 && math.Abs(sum-whole.total) <= 0.005 {
			for _, name := range of {
				out[name] = true
			}
		}
	}
	return out
}

// withDailyCredits gives each card in qs the days of its credits windows,
// as kept: Daily the first's, MoreDaily the rest's.
func withDailyCredits(qs []SubscriptionQuota, now time.Time) []SubscriptionQuota {
	if !slices.ContainsFunc(qs, func(q SubscriptionQuota) bool { return slices.ContainsFunc(q.Windows, isCreditWindow) }) {
		return qs
	}
	// read only: the parse is shared until the file changes
	accts, err := filememo.Read("credits-daily", creditsPath(), parseCredits)
	if err != nil {
		return qs
	}
	cut := now.Local().AddDate(0, 0, -creditsShown).Format("2006-01-02")
	out := slices.Clone(qs)
	for i, q := range out {
		var all []*DailyCredits
		var ws []creditWindow
		for _, w := range q.Windows {
			if c, ok := creditOf(w); ok {
				ws = append(ws, c)
			}
		}
		pools := poolsOfWhole(ws)
		for _, w := range q.Windows {
			c, ok := creditOf(w)
			if !ok || slices.ContainsFunc(all, func(d *DailyCredits) bool { return d.Window == c.name }) {
				continue
			}
			a := accts[creditsKey(q, c.name)]
			if a == nil && c.name == "Credits" {
				a = accts[creditsOldKey(q)] // not yet carried over
			}
			if a == nil {
				continue
			}
			d := &DailyCredits{Since: a.Since, Days: []DayCredits{}, Window: c.name}
			if c.unit != "credits" {
				d.Unit = c.unit
			}
			for day, used := range a.Days {
				if day >= cut && used > 0 {
					d.Days = append(d.Days, DayCredits{Day: day, Used: used})
				}
			}
			if a.Part && !pools[c.name] && len(d.Days) == 0 {
				continue // another window's breakdown, counted there
			}
			slices.SortFunc(d.Days, func(x, y DayCredits) int { return strings.Compare(x.Day, y.Day) })
			all = append(all, d)
		}
		if len(all) > 0 {
			out[i].Daily = all[0]
		}
		if len(all) > 1 {
			out[i].MoreDaily = all[1:]
		}
	}
	return out
}

func isCreditWindow(w QuotaWindow) bool { _, ok := creditOf(w); return ok }

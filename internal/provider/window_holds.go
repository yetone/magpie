package provider

// What a whole subscription window holds, as the Usage page's account
// cards say beside its share used: what magpie routed through the account
// since the window began, over the share the vendor says is used. Package
// usage reckons it (WithWindowHolds), from its log; here are the figures and
// the window's bounds.

import (
	"slices"
	"strings"
	"time"
)

// WindowHolds is a window's whole allowance reckoned from magpie's own
// calls: Tokens of input and output (cache traffic aside, as the Usage
// page counts tokens unless its pick is With cache, when the card adds
// Routed's cache over Used itself) and, when every call's model has an API list price,
// Cost at those prices (cache reads and writes at the vendor's own rates).
// Routed is what magpie routed through the account in the window so far,
// up to when the share was read, and Used that share. A call made with the
// account outside magpie is in Used but not in Routed, so the estimate is
// then lower than the window really holds.
//
// BaseCost is Cost with every call at its model's base rates, no API
// long-context premium (catalog.Price.Tiers) (#5861, NorethSea): a plan's
// own use has none (OpenAI bills no long-context surcharge in Codex), so
// two cycles whose calls' sizes differ compare by it. It is there only
// when some call was over a tier's size, else it is Cost.
type WindowHolds struct {
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost,omitempty"`
	BaseCost float64 `json:"baseCost,omitempty"`
	Priced   bool    `json:"priced"`
	Used     float64 `json:"used"`
	Routed   struct {
		Calls      int     `json:"calls"`
		Tokens     int64   `json:"tokens"`
		CacheRead  int64   `json:"cacheRead,omitempty"`
		CacheWrite int64   `json:"cacheWrite,omitempty"`
		Cost       float64 `json:"cost,omitempty"`
		BaseCost   float64 `json:"baseCost,omitempty"`
	} `json:"routed"`
	Since time.Time `json:"since"`
	// PrevAt is set when this is the last window's whole, carried over a
	// reset until the new window has enough data (Musikrv77852 on X): the
	// time that figure was last reckoned. Since is then that window's start.
	PrevAt *time.Time `json:"prevAt,omitempty"`
}

// HoldsFloor is the least share used a window's whole is reckoned from:
// below it a percent the vendor rounds is too coarse to scale up.
const HoldsFloor = 5.0

// Why a window's whole isn't told (QuotaWindow.HoldsWhy), for the GUI to
// say "not enough data" and why rather than leave it out (#2045).
const (
	HoldsLow        = "low"     // under HoldsFloor used
	HoldsNoClock    = "clock"   // no reset clock, so no known start
	HoldsBeforeLog  = "log"     // began before magpie's log does
	HoldsNoneRouted = "none"    // magpie routed nothing it counts in it
	HoldsCredits    = "credits" // a call in it may have been paid from credits
)

// Bounds is when w, read at readAt, began and ends: its reset, and that
// less how long it runs; a month's window began on the same day a month
// before. ok is false when either isn't known.
func (w QuotaWindow) Bounds(readAt time.Time) (start, end time.Time, ok bool) {
	switch {
	case w.ResetsAt != nil:
		end = *w.ResetsAt
	case w.ResetSecs > 0:
		end = readAt.Add(time.Duration(w.ResetSecs) * time.Second)
	default:
		return time.Time{}, time.Time{}, false
	}
	span := windowSpan(w)
	if span <= 0 {
		return time.Time{}, time.Time{}, false
	}
	if span == 30*24*time.Hour {
		return end.AddDate(0, -1, 0), end, true
	}
	return end.Add(-span), end, true
}

// Counts is whether a call to model counts against w: every model, or
// only the ones its scope names ("opus", a plugin's list).
func (w QuotaWindow) Counts(model string) bool {
	return Limit{Model: w.Model, matches: w.matches}.applies(strings.ToLower(model))
}

// PoolWindow is whether w is one of an Antigravity pool's own windows
// ("Gemini" · 5 hours, · 7 days), the allowance its models share, not a
// model's own window drawing on it.
func (w QuotaWindow) PoolWindow() bool { return w.Pool != "" && w.Family == "" }

// LineName is the name w's readings are kept under (quota-history.json)
// and its line drawn by: a pool's window with its pool's name before its
// own ("Gemini · 5 hours"), as every pool has a "5 hours".
func (w QuotaWindow) LineName() string {
	if w.Pool != "" {
		return w.Pool + " · " + w.Name
	}
	return w.Name
}

// CountsIn is whether a call to model counts against w, one of ws, the
// windows of its account's card: a pool's window counts its models' calls
// (PoolMembers), by the model's own id or the one its effort word is a
// level of (gemini-3.8-flash is gemini-3.8-flash-medium's); any other as
// Counts says.
func (w QuotaWindow) CountsIn(ws []QuotaWindow, model string) bool {
	if !w.PoolWindow() {
		return w.Counts(model)
	}
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	for _, o := range PoolMembers(ws, w) {
		id := strings.ToLower(o.Model)
		if id == m || strings.ToLower(EffortFamily(id)) == m || EffortFamily(m) == EffortFamily(id) {
			return true
		}
	}
	return false
}

// PoolMembers are the per-model windows among ws whose models draw on
// pool window w's allowance: the ones its pool's name was given to
// (googleAccount.pools), else, when none was (a pool read with only its
// 7 days, as an Antigravity Starter plan's), the ones of a family its
// name says ("Claude & GPT": Claude, GPT-OSS) and no other pool took. A
// pool named for one model ("Gemini 4 Argon") has only the models it was
// given.
func PoolMembers(ws []QuotaWindow, w QuotaWindow) []QuotaWindow {
	var given, family []QuotaWindow
	words := strings.FieldsFunc(strings.ToLower(w.Pool), func(r rune) bool { return r == ' ' || r == '&' })
	for _, o := range ws {
		if o.Family == "" || o.Model == "" {
			continue
		}
		if o.Pool == w.Pool {
			given = append(given, o)
			continue
		}
		f, _, _ := strings.Cut(strings.ToLower(o.Family), "-")
		if o.Pool == "" && !versioned(w.Pool) && f != "" && slices.Contains(words, f) {
			family = append(family, o)
		}
	}
	if len(given) > 0 {
		return given
	}
	return family
}

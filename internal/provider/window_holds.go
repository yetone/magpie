package provider

// What a whole subscription window holds, as the Usage page's account
// cards say beside its share used: what magpie routed through the account
// since the window began, over the share the vendor says is used. Package
// usage reckons it (WithWindowHolds), from its log; here are the figures and
// the window's bounds.

import (
	"strings"
	"time"
)

// WindowHolds is a window's whole allowance reckoned from magpie's own
// calls: Tokens of input and output (cache traffic aside, as the Usage
// page counts tokens) and, when every call's model has an API list price,
// Cost at those prices (cache reads and writes at the vendor's own rates).
// Routed is what magpie routed through the account in the window so far,
// up to when the share was read, and Used that share. A call made with the
// account outside magpie is in Used but not in Routed, so the estimate is
// then lower than the window really holds.
type WindowHolds struct {
	Tokens int64   `json:"tokens"`
	Cost   float64 `json:"cost,omitempty"`
	Priced bool    `json:"priced"`
	Used   float64 `json:"used"`
	Routed struct {
		Calls      int     `json:"calls"`
		Tokens     int64   `json:"tokens"`
		CacheRead  int64   `json:"cacheRead,omitempty"`
		CacheWrite int64   `json:"cacheWrite,omitempty"`
		Cost       float64 `json:"cost,omitempty"`
	} `json:"routed"`
	Since time.Time `json:"since"`
}

// HoldsFloor is the least share used a window's whole is reckoned from:
// below it a percent the vendor rounds is too coarse to scale up.
const HoldsFloor = 5.0

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

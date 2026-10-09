package provider

// What was left of each window over time (#651): each reading of an
// account's windows that magpie makes anyway (the subscriptions', a minute
// apart at most while something asks; the plans' bought with a key) is kept
// as a point — when it was read, the percent left, when the window started
// and when it starts again — so the Usage page can draw the cycle's curve
// against an even burn, and the tray its current cycle's line.
//
// Kept in quota-history.json, by provider and account name, and by window:
// the figures only — no token, no prompt, nothing else of the vendor's. A
// reading kept from before (AsOf) or failed isn't a new one; a window with no
// limit, or one set aside, has nothing to draw. Points come at least
// quotaHistGap apart within a cycle (a nearer one moves the last instead),
// and a run of the same figure is its first and last point only. A cycle
// ends where the window starts again: its reset passed, or moved further
// than the time between two readings (a rolling window's moves with it).
// Points older than quotaHistKept go, and a window keeps quotaHistMax.
//
// Other computers' points come in through the usage sync (davsync), merged
// by MergeQuotaHistory: the union of both, sorted, a point within
// quotaHistSame of the next one dropped for it.

import (
	"encoding/json"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/filememo"
)

const (
	quotaHistKept = 45 * 24 * time.Hour
	quotaHistMax  = 6000
	quotaHistGap  = 5 * time.Minute
	quotaHistSame = 30 * time.Second
	// a reset moved this much past the time between two readings is a
	// window started again, not a reset read a little differently
	quotaHistSlack = 5 * time.Minute
)

// QuotaPoint is one reading of a window: at At, Left percent was left;
// the window began at Start and starts again at ResetsAt, when told.
type QuotaPoint struct {
	At       time.Time  `json:"at"`
	Left     float64    `json:"left"`
	Start    *time.Time `json:"start,omitempty"`
	ResetsAt *time.Time `json:"resetsAt,omitempty"`
}

// QuotaLine is one window's points, oldest first.
type QuotaLine struct {
	Name   string       `json:"name"`
	Points []QuotaPoint `json:"points"`
}

// QuotaHistory is one account's windows over time. User is lowercased.
type QuotaHistory struct {
	Provider string      `json:"provider"`
	User     string      `json:"user"`
	Lines    []QuotaLine `json:"lines"`
}

// quotaHist is the file: account ("provider|user") → window → points.
type quotaHist map[string]map[string][]QuotaPoint

var quotaHistMu sync.Mutex

// lastNoted is each account's newest noted reading's time, kept per history
// file and in memory only: a batch whose readings are all older notes
// nothing new for that file (#1358).
var (
	lastNoted     = map[string]time.Time{}
	lastNotedPath string
)

func quotaHistPath() string { return filepath.Join(filepath.Dir(Path()), "quota-history.json") }

func parseQuotaHist(b []byte) (quotaHist, error) {
	h := quotaHist{}
	if err := json.Unmarshal(b, &h); err != nil || h == nil {
		return quotaHist{}, nil
	}
	return h, nil
}

// readQuotaHist is the history file's read; tests count the reads.
var readQuotaHist = func() quotaHist {
	if b, err := os.ReadFile(quotaHistPath()); err == nil {
		h, _ := parseQuotaHist(b)
		return h
	}
	return quotaHist{}
}

func writeQuotaHist(h quotaHist) {
	b, err := json.Marshal(h)
	if err != nil {
		return
	}
	path := quotaHistPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = writeFileAtomic(path, b)
}

var spanWords = regexp.MustCompile(`(?i)(\d+)\s*-?\s*(h|hr|hour|d|day|w|wk|week|month)s?\b`)

// windowSpan is how long a window runs: as its vendor said, else as its
// name says ("5 hours", "7d", "Weekly"), else 0, not known.
func windowSpan(w QuotaWindow) time.Duration {
	if w.Span > 0 {
		return w.Span
	}
	unit := map[string]time.Duration{"h": time.Hour, "hr": time.Hour, "hour": time.Hour, "d": 24 * time.Hour, "day": 24 * time.Hour,
		"w": 7 * 24 * time.Hour, "wk": 7 * 24 * time.Hour, "week": 7 * 24 * time.Hour, "month": 30 * 24 * time.Hour}
	if m := spanWords.FindStringSubmatch(w.Name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
			return time.Duration(n) * unit[strings.ToLower(m[2])]
		}
	}
	switch n := strings.ToLower(w.Name); {
	case strings.Contains(n, "daily"):
		return 24 * time.Hour
	case strings.Contains(n, "weekly"):
		return 7 * 24 * time.Hour
	case strings.Contains(n, "monthly"):
		return 30 * 24 * time.Hour
	}
	return 0
}

// newCycle says whether b, read after a, is of a window started again.
func newCycle(a, b QuotaPoint) bool {
	if a.ResetsAt == nil && b.ResetsAt == nil {
		return b.Left > a.Left+5 // filled again
	}
	if a.ResetsAt == nil || b.ResetsAt == nil { // begun, or over
		return true
	}
	if !a.ResetsAt.After(b.At) { // its reset has passed
		return true
	}
	moved := b.ResetsAt.Sub(*a.ResetsAt)
	return moved > b.At.Sub(a.At)+quotaHistSlack || moved < -quotaHistSlack
}

// pointOf is what w, read at at, says, if it is something to draw; a
// reset told in seconds counts from now, when the windows were made.
func pointOf(w QuotaWindow, at, now time.Time) (QuotaPoint, bool) {
	if w.Unlimited || w.Aside || w.Family != "" || w.Name == "" || math.IsNaN(w.Used) {
		return QuotaPoint{}, false
	}
	p := QuotaPoint{At: at.UTC().Truncate(time.Second), Left: math.Round(max(0, min(100, 100-w.Used))*100) / 100}
	reset := w.ResetsAt
	if reset == nil && w.ResetSecs > 0 {
		r := now.Add(time.Duration(w.ResetSecs) * time.Second)
		reset = &r
	}
	if reset != nil {
		r := reset.UTC().Truncate(time.Second)
		p.ResetsAt = &r
		if span := windowSpan(w); span > 0 {
			s := r.Add(-span)
			p.Start = &s
		}
	}
	return p, true
}

// addPoint puts n after pts, the last moved to it when near or the same.
func addPoint(pts []QuotaPoint, n QuotaPoint) []QuotaPoint {
	k := len(pts)
	if k > 0 && !n.At.After(pts[k-1].At) {
		return pts // not newer than what is kept
	}
	if k >= 2 {
		p1, p2 := pts[k-2], pts[k-1]
		if !newCycle(p1, p2) && !newCycle(p2, n) {
			flat := math.Abs(n.Left-p2.Left) < 0.05 && math.Abs(p2.Left-p1.Left) < 0.05
			if flat || n.At.Sub(p1.At) < quotaHistGap {
				pts[k-1] = n
				return pts
			}
		}
	}
	if k >= 1 && n.At.Sub(pts[k-1].At) < quotaHistSame && !newCycle(pts[k-1], n) {
		pts[k-1] = n
		return pts
	}
	return append(pts, n)
}

// prune drops what is older than kept and what is past quotaHistMax.
func (h quotaHist) prune(now time.Time) {
	cut := now.Add(-quotaHistKept)
	for key, ws := range h {
		for name, pts := range ws {
			i, _ := slices.BinarySearchFunc(pts, cut, func(p QuotaPoint, t time.Time) int { return p.At.Compare(t) })
			pts = pts[i:]
			if len(pts) > quotaHistMax {
				pts = pts[len(pts)-quotaHistMax:]
			}
			if len(pts) == 0 {
				delete(ws, name)
			} else {
				ws[name] = slices.Clip(pts)
			}
		}
		if len(ws) == 0 {
			delete(h, key)
		}
	}
}

func quotaHistKey(provider, user string) string { return provider + "|" + strings.ToLower(user) }

// noteQuotaHistory keeps what each window among qs says, at when it was
// read (ReadAt), or now: a reading handed back again from a cache (a
// Claude account's, kept until Claude Code tells it again) keeps its time
// and so isn't taken for a new one.
func noteQuotaHistory(qs []SubscriptionQuota, now time.Time) {
	quotaHistMu.Lock()
	defer quotaHistMu.Unlock()
	path := quotaHistPath()
	if lastNotedPath != path {
		// another history file: the noted times say nothing about it
		lastNoted, lastNotedPath = map[string]time.Time{}, path
	}
	// A batch of cached readings notes nothing new: every reading's time is
	// no newer than what was last noted for its account, so the file need
	// not be read and parsed for that batch again (#1358).
	skip := true
	for _, q := range qs {
		if q.Error != "" || q.AsOf != nil || q.Provider == "" {
			continue // a reading kept from before isn't a new one
		}
		at := now
		if q.ReadAt != nil && !q.ReadAt.After(now) {
			at = *q.ReadAt
		}
		key := quotaHistKey(q.Provider, q.User)
		if lastNoted[key].Before(at) {
			lastNoted[key] = at
			skip = false
		}
	}
	if skip {
		return
	}
	var h quotaHist
	changed := false
	for _, q := range qs {
		if q.Error != "" || q.AsOf != nil || q.Provider == "" {
			continue // a reading kept from before isn't a new one
		}
		at := now
		if q.ReadAt != nil && !q.ReadAt.After(now) {
			at = *q.ReadAt
		}
		for _, w := range q.Windows {
			p, ok := pointOf(w, at, now)
			if !ok {
				continue
			}
			if h == nil {
				h = readQuotaHist()
			}
			key := quotaHistKey(q.Provider, q.User)
			if h[key] == nil {
				h[key] = map[string][]QuotaPoint{}
			}
			name := w.Name
			if w.Pool != "" { // Antigravity's pools each have a 5-hour window
				name = w.Pool + " · " + w.Name
			}
			before := h[key][name]
			k := len(before)
			var last QuotaPoint
			if k > 0 {
				last = before[k-1]
			}
			after := addPoint(before, p)
			if len(after) != k || k > 0 && after[k-1] != last {
				changed = true
			}
			h[key][name] = after
		}
	}
	if !changed {
		return
	}
	h.prune(now)
	writeQuotaHist(h)
}

// mergeQuotaHist puts b's points into a: the union of both, a point within
// quotaHistSame of the next one (of the same cycle) dropped for it.
func mergeQuotaHist(a, b quotaHist) (changed bool) {
	for key, ws := range b {
		if a[key] == nil {
			a[key] = map[string][]QuotaPoint{}
		}
		for name, pts := range ws {
			old := a[key][name]
			all := slices.Concat(old, pts)
			slices.SortStableFunc(all, func(x, y QuotaPoint) int { return x.At.Compare(y.At) })
			out := all[:0:0]
			for i, p := range all {
				if i+1 < len(all) && all[i+1].At.Sub(p.At) < quotaHistSame && !newCycle(p, all[i+1]) {
					continue // the newer of two near the same
				}
				out = append(out, p)
			}
			if !slices.EqualFunc(old, out, samePoint) {
				a[key][name] = out
				changed = true
			}
		}
	}
	return changed
}

func samePoint(x, y QuotaPoint) bool {
	eq := func(a, b *time.Time) bool { return a == nil && b == nil || a != nil && b != nil && a.Equal(*b) }
	return x.At.Equal(y.At) && x.Left == y.Left && eq(x.Start, y.Start) && eq(x.ResetsAt, y.ResetsAt)
}

// QuotaHistoryData is this computer's history as kept, for the usage sync
// to share.
func QuotaHistoryData() []byte {
	quotaHistMu.Lock()
	defer quotaHistMu.Unlock()
	b, _ := os.ReadFile(quotaHistPath())
	return b
}

// MergeQuotaHistory merges another computer's history (as QuotaHistoryData
// gave it there) into this one's.
func MergeQuotaHistory(data []byte, now time.Time) error {
	var b quotaHist
	if err := json.Unmarshal(data, &b); err != nil {
		return err
	}
	quotaHistMu.Lock()
	defer quotaHistMu.Unlock()
	h := readQuotaHist()
	if !mergeQuotaHist(h, b) {
		return nil
	}
	h.prune(now)
	writeQuotaHist(h)
	return nil
}

// QuotaHistories is every account's windows' points since since, the
// accounts and windows in name order; provider and user, when given, pick
// one provider's, or one account's.
func QuotaHistories(since time.Time, provider, user string) []QuotaHistory {
	h, err := filememo.Read("quota-history", quotaHistPath(), parseQuotaHist)
	if err != nil {
		return []QuotaHistory{}
	}
	out := []QuotaHistory{}
	for _, key := range slices.Sorted(maps.Keys(h)) {
		p, u, _ := strings.Cut(key, "|")
		if provider != "" && p != provider || user != "" && u != strings.ToLower(user) {
			continue
		}
		a := QuotaHistory{Provider: p, User: u, Lines: []QuotaLine{}}
		for _, name := range slices.Sorted(maps.Keys(h[key])) {
			pts := h[key][name]
			i, _ := slices.BinarySearchFunc(pts, since, func(p QuotaPoint, t time.Time) int { return p.At.Compare(t) })
			if i > 0 {
				i-- // the one before, so a line reaches the range's edge
			}
			if i < len(pts) {
				a.Lines = append(a.Lines, QuotaLine{Name: name, Points: slices.Clone(pts[i:])})
			}
		}
		if len(a.Lines) > 0 {
			out = append(out, a)
		}
	}
	return out
}

// QuotaHistorySince is the start of a range days long (as a query gives
// it) to now: all that is kept when not a number of days.
func QuotaHistorySince(days string, now time.Time) time.Time {
	n, err := strconv.ParseFloat(days, 64)
	if err != nil || n <= 0 || n*24*float64(time.Hour) > float64(quotaHistKept) {
		return now.Add(-quotaHistKept)
	}
	return now.Add(-time.Duration(n * 24 * float64(time.Hour)))
}

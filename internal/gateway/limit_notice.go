package gateway

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/steady"
)

// A rate limit or quota refusal is shown on its account's or key's row
// while it rests (RestsUnder, RestOf). A rest can end long before the
// limit does: a Retry-After is trusted an hour at most (longestWait), a
// 429 that names no time rests a minute, and a restart forgets every rest
// but the credit ladder's. The red line went with it, and the vendor's
// "try again in 6 hours" with it (@eachg0 on X). Each such refusal is
// kept as a notice by the key it rested by: until the time the vendor
// named passes, or, when it named none, until that key answers a request
// again (served), or its rest is lifted (liftedLocked: Try now, its
// windows renewed, a probe answered). Notices are kept in
// limit-notices.json beside providers.json, so a restart keeps them too.
// They change nothing about routing.

// LimitNotice is a rate limit or quota refusal shown on its row.
type LimitNotice struct {
	Key    string    `json:"key"`
	Why    string    `json:"why"`
	Status int       `json:"status"`
	Error  string    `json:"error,omitempty"`
	Rate   bool      `json:"rate,omitempty"`
	Model  string    `json:"model,omitempty"`
	At     time.Time `json:"at"`             // when it was refused
	Until  time.Time `json:"until,omitzero"` // when the vendor said it lifts; zero when it didn't
}

// noticeKeep: a notice with no time of its own is let go this long after
// it was given, a key or account never asked again since included.
const noticeKeep = longestMonthly

var limitNotices = struct {
	sync.Mutex
	m map[string]LimitNotice
}{m: map[string]LimitNotice{}}

var noticesSaving struct {
	sync.Mutex
	path, last string
	loaded     map[string]bool
}

func noticesPath() string { return filepath.Join(filepath.Dir(restsPath()), "limit-notices.json") }

// live says n is still to be shown at now.
func (n LimitNotice) live(now time.Time) bool {
	if !n.Until.IsZero() {
		return now.Before(n.Until)
	}
	return now.Sub(n.At) < noticeKeep
}

// limitEnd is when a refusal's limit lifts, as the vendor said it, not
// cut to how long magpie rests on its word; zero when it said nothing.
func limitEnd(r Rest, header http.Header, body []byte, now time.Time) time.Time {
	var end time.Time
	switch r.By {
	case "resets", "retry-after", "window":
		end = r.Until
	}
	if d := max(retryAfterSaid(header, now), resetsNoted(header, now), resetsAt(body, now)); d > 0 {
		if t := now.Add(min(d, longestMonthly)); t.After(end) {
			end = t
		}
	}
	return end
}

// noteLimit keeps r, a rate limit or quota refusal just rested, as its
// key's notice. A refusal naming no time keeps the time an earlier one
// named, while that stands.
func noteLimit(r Rest, header http.Header, body []byte, now time.Time) {
	if r.Key == "" || r.Why != failRate && r.Why != failQuota {
		return
	}
	n := LimitNotice{Key: r.Key, Why: r.Why, Status: r.Status, Error: r.Error, Rate: r.Rate, Model: r.Model, At: now, Until: limitEnd(r, header, body, now)}
	limitNotices.Lock()
	if prev, ok := limitNotices.m[r.Key]; ok && n.Until.IsZero() && prev.Until.After(now) {
		n.Until = prev.Until
	}
	limitNotices.m[r.Key] = n
	limitNotices.Unlock()
	saveNotices()
}

// dropNotice lets key's notice go, and says whether there was one.
func dropNotice(key string) bool {
	limitNotices.Lock()
	defer limitNotices.Unlock()
	_, ok := limitNotices.m[key]
	delete(limitNotices.m, key)
	return ok
}

// noticesDropped: a notice was let go under restingUntil's lock
// (liftedLocked), where no file is written; saveDropped writes it after.
var noticesDropped atomic.Bool

// saveDropped writes the notices once one was let go under the lock.
func saveDropped() {
	if noticesDropped.Swap(false) {
		saveNotices()
	}
}

// clearNotice lets key's notice go after it answered a request.
func clearNotice(key string) {
	if dropNotice(key) {
		saveNotices()
	}
}

// LimitNoticeOf is the notice of what rests by key, while it stands.
func LimitNoticeOf(key string) (LimitNotice, bool) {
	limitNotices.Lock()
	defer limitNotices.Unlock()
	n, ok := limitNotices.m[key]
	if !ok || !n.live(time.Now()) {
		return LimitNotice{}, false
	}
	return n, true
}

// LimitNoticesUnder are the notices standing of what rests by a key
// starting with prefix, as RestsUnder has the rests, oldest first.
func LimitNoticesUnder(prefix string) []LimitNotice {
	limitNotices.Lock()
	defer limitNotices.Unlock()
	now := time.Now()
	var out []LimitNotice
	for k, n := range limitNotices.m {
		if strings.HasPrefix(k, prefix) && n.live(now) {
			out = append(out, n)
		}
	}
	slices.SortFunc(out, func(a, b LimitNotice) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out
}

// saveNotices writes the notices standing to limit-notices.json through
// a file renamed over it, when they changed. A file another magpie wrote
// before this one read it is read first, so its notices aren't lost.
func saveNotices() {
	path := noticesPath()
	noticesSaving.Lock()
	defer noticesSaving.Unlock()
	loadNoticesLocked(path)
	now := time.Now()
	out := map[string]LimitNotice{}
	limitNotices.Lock()
	for k, n := range limitNotices.m {
		if n.live(now) {
			out[k] = n
		} else {
			delete(limitNotices.m, k)
		}
	}
	limitNotices.Unlock()
	b, err := json.Marshal(out)
	if err != nil || noticesSaving.path == path && noticesSaving.last == string(b) {
		return
	}
	if len(out) == 0 {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			noticesSaving.path, noticesSaving.last = path, string(b)
			return
		}
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	target, err := edit.Target(path)
	if err != nil {
		return
	}
	tmp := target + ".magpie-tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	if steady.Rename(tmp, target) != nil {
		os.Remove(tmp)
		return
	}
	noticesSaving.path, noticesSaving.last = path, string(b)
}

// loadNotices reads the notices a magpie before this one kept, once for
// each file. A notice this process has already is kept over the file's;
// one ended since, or a file that can't be read or parsed, adds nothing.
func loadNotices() {
	noticesSaving.Lock()
	defer noticesSaving.Unlock()
	loadNoticesLocked(noticesPath())
}

func loadNoticesLocked(path string) {
	if noticesSaving.loaded[path] {
		return
	}
	if noticesSaving.loaded == nil {
		noticesSaving.loaded = map[string]bool{}
	}
	noticesSaving.loaded[path] = true
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var saved map[string]LimitNotice
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	now := time.Now()
	limitNotices.Lock()
	defer limitNotices.Unlock()
	for k, n := range saved {
		if _, ok := limitNotices.m[k]; ok || n.At.After(now) || !n.live(now) {
			continue
		}
		n.Key = k
		limitNotices.m[k] = n
	}
}

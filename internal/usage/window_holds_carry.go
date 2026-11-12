package usage

// A window's whole across its reset (Musikrv77852 on X): the week's reset
// leaves nothing used, so nothing to scale, and the card lost its figure
// until the new week was used past provider.HoldsFloor. The last whole
// told for each account's window is kept (window-holds.json) and shown,
// marked as the previous window's (WindowHolds.PrevAt), until the new
// window tells its own. It is every provider's, as the reckoning is.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

type carried struct {
	Holds provider.WindowHolds `json:"holds"`
	End   time.Time            `json:"end"` // when the window it was reckoned in ended
	At    time.Time            `json:"at"`  // when it was last reckoned
}

var carry struct {
	sync.Mutex
	path    string
	m       map[string]carried
	dirty   bool
	wroteAt time.Time
}

func carryPath() string { return filepath.Join(filepath.Dir(provider.Path()), "window-holds.json") }

// carryLoad is the kept figures; a file that doesn't parse is no figures
// (they are only a fallback), and is written over when the next is told.
func carryLoad() map[string]carried {
	if p := carryPath(); carry.m == nil || carry.path != p {
		carry.path, carry.m, carry.dirty = p, map[string]carried{}, false
		if b, err := os.ReadFile(p); err == nil {
			var m map[string]carried
			if json.Unmarshal(b, &m) == nil && m != nil {
				carry.m = m
			}
		}
	}
	return carry.m
}

func carrySave(now time.Time) {
	if !carry.dirty || now.Sub(carry.wroteAt) < 30*time.Second {
		return
	}
	b, err := json.Marshal(carry.m)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(carry.path), 0o700)
	tmp, err := os.CreateTemp(filepath.Dir(carry.path), ".window-holds-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), carry.path) != nil {
		os.Remove(tmp.Name())
		return
	}
	carry.dirty, carry.wroteAt = false, now
}

// carryHolds is qs with each window that tells its whole noted, and each
// that can't yet (HoldsLow, HoldsNoneRouted) given the whole of an earlier
// window of the same account, if one was told. qs is left as it is.
func carryHolds(qs []provider.SubscriptionQuota, now time.Time) []provider.SubscriptionQuota {
	carry.Lock()
	defer carry.Unlock()
	m := carryLoad()
	out := qs
	cloned := map[int]bool{}
	for i, q := range qs {
		if q.User == "" || q.Error != "" || q.From != "" {
			continue
		}
		at := readAtOf(q, now)
		for j, w := range q.Windows {
			if w.Holds == nil && w.HoldsWhy != provider.HoldsLow && w.HoldsWhy != provider.HoldsNoneRouted {
				continue
			}
			_, end, ok := w.Bounds(at)
			if !ok {
				continue
			}
			k := q.Provider + "\\x00" + strings.ToLower(q.User) + "\\x00" + w.LineName()
			if w.Holds != nil {
				h := *w.Holds
				if old, had := m[k]; !had || old.End != end || old.Holds.Tokens != h.Tokens || old.Holds.Cost != h.Cost {
					m[k], carry.dirty = carried{Holds: h, End: end, At: at}, true
				}
				continue
			}
			c, had := m[k]
			// one of an earlier window: the end moved by more than a
			// jitter in the vendor's clock
			if !had || end.Sub(c.End) <= max(time.Minute, windowGap(w)/2) {
				continue
			}
			h := c.Holds
			prev := c.At
			h.PrevAt = &prev
			if !cloned[i] {
				out = append([]provider.SubscriptionQuota(nil), out...)
				out[i].Windows = append([]provider.QuotaWindow(nil), q.Windows...)
				cloned[i] = true
			}
			out[i].Windows[j].Holds, out[i].Windows[j].HoldsWhy = &h, ""
		}
	}
	carrySave(now)
	return out
}

// windowGap is how long w runs, from its span or name.
func windowGap(w provider.QuotaWindow) time.Duration {
	if w.Span > 0 {
		return w.Span
	}
	start, end, ok := w.Bounds(time.Time{})
	if !ok {
		return 0
	}
	return end.Sub(start)
}

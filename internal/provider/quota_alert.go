package provider

// Usage alerts (#368): a notification when a subscription's or plan's window
// has reached the share of it the user chose, or a balance has fallen to the
// amount they chose (settings.UsageAlert, settings.BalanceAlert). Each is
// said once: a window once each time it runs, a balance once until it is
// topped up past the amount again. What was said is kept in a file, so a
// restart doesn't say it again.

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// QuotaAlert is one thing to tell the user: a window used past the share
// (Window set) or a balance at or under the amount (Balance set).
type QuotaAlert struct {
	Provider string
	Name     string // the card's name, as the Usage page shows it
	User     string // the account or key, when the provider has several
	Window   string // the window's name
	Used     float64
	ResetsAt *time.Time
	Balance  string // the balance as the vendor tells it
}

// alertMark is what was said of one window or balance: when, and for a
// window the reset it was said before, which is how a new run of the window
// is told from the same one read again.
type alertMark struct {
	At    time.Time  `json:"at"`
	Until *time.Time `json:"until,omitempty"`
}

// alertSameRun is how far apart two readings of a window's reset may be and
// still be the same run of it: Codex's is the reading's time plus the
// seconds left, and moves a little from one reading to the next.
const alertSameRun = 10 * time.Minute

// alertKeep is how long the mark of a window or balance no longer read
// (signed out, a key removed) is kept.
const alertKeep = 40 * 24 * time.Hour

// dueAlerts is what the readings qs call for with a window alert at pct
// percent used (0 off) and a balance alert at bal (0 off), and the marks
// brought up to date with it. A reading that failed, or is one kept from
// before (AsOf), neither alerts nor clears: nothing new is known.
func dueAlerts(qs []SubscriptionQuota, marks map[string]alertMark, pct int, bal float64, now time.Time) []QuotaAlert {
	var out []QuotaAlert
	seen := map[string]bool{}
	for _, q := range qs {
		if q.Error != "" || q.AsOf != nil {
			for k := range marks {
				if strings.HasPrefix(k, alertPrefix(q)) {
					seen[k] = true
				}
			}
			continue
		}
		// the pools' own windows stand in for the models' drawing on them,
		// so a pool is told of once, not once per model
		for _, w := range PooledWindows(q.Windows) {
			if w.Aside && !(w.Pool != "" && w.Span == 7*24*time.Hour) {
				continue // using it up doesn't stop the account
			}
			key := alertPrefix(q) + "w|" + w.Name + "|" + w.Model
			seen[key] = true
			resets := w.ResetsAt
			if resets == nil && w.ResetSecs > 0 {
				t := now.Add(time.Duration(w.ResetSecs) * time.Second)
				resets = &t
			}
			if pct <= 0 || w.Used < float64(pct) {
				delete(marks, key)
				continue
			}
			if m, ok := marks[key]; ok && sameRun(m.Until, resets) {
				continue
			}
			marks[key] = alertMark{At: now, Until: resets}
			out = append(out, QuotaAlert{Provider: q.Provider, Name: q.Name, User: q.User, Window: w.Name, Used: w.Used, ResetsAt: resets})
		}
		if q.Balance == "" {
			continue
		}
		key := alertPrefix(q) + "b"
		seen[key] = true
		n, ok := BalanceNumber(q.Balance)
		if !ok {
			continue
		}
		if bal <= 0 || n > bal {
			delete(marks, key)
			continue
		}
		if _, ok := marks[key]; ok {
			continue
		}
		marks[key] = alertMark{At: now}
		out = append(out, QuotaAlert{Provider: q.Provider, Name: q.Name, User: q.User, Balance: q.Balance})
	}
	// one not read at all now is kept a while, in case it is read again
	for k, m := range marks {
		if !seen[k] && now.Sub(m.At) > alertKeep {
			delete(marks, k)
		}
	}
	return out
}

// alertPrefix begins the keys of one card's marks: its provider and account.
func alertPrefix(q SubscriptionQuota) string { return q.Provider + "|" + q.User + "|" }

// sameRun is whether a window marked as resetting at was and one read now
// resetting at now are one run of it. A window that says no reset is taken
// for the same run until its use falls under the share.
func sameRun(was, now *time.Time) bool {
	if was == nil || now == nil {
		return was == nil && now == nil
	}
	d := was.Sub(*now)
	return d < alertSameRun && d > -alertSameRun
}

var balanceNum = regexp.MustCompile(`-?\d[\d,]*(?:\.\d+)?`)

// BalanceNumber is the amount a balance as a vendor tells it holds: "$12.34",
// "¥1,024.50", "CNY 8", "12.3k credits". A share ("45%"), or text with no
// number, is none.
func BalanceNumber(s string) (float64, bool) {
	if strings.Contains(s, "%") {
		return 0, false
	}
	loc := balanceNum.FindStringIndex(s)
	if loc == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(strings.ReplaceAll(s[loc[0]:loc[1]], ",", ""), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, false
	}
	switch rest := strings.TrimSpace(s[loc[1]:]); {
	case strings.HasPrefix(rest, "k") || strings.HasPrefix(rest, "K"):
		n *= 1e3
	case strings.HasPrefix(rest, "M"):
		n *= 1e6
	}
	return n, true
}

func quotaAlertPath() string { return filepath.Join(filepath.Dir(Path()), "usage-alerts.json") }

// quotaAlertMu keeps the file to one writer in this magpie.
var quotaAlertMu sync.Mutex

func readAlertMarks(path string) map[string]alertMark {
	m := map[string]alertMark{}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

// checkAlerts reads what is left everywhere and says what is due, keeping
// the marks in path.
func checkAlerts(path string, qs []SubscriptionQuota, pct int, bal float64, now time.Time) []QuotaAlert {
	quotaAlertMu.Lock()
	defer quotaAlertMu.Unlock()
	marks := readAlertMarks(path)
	before, _ := json.Marshal(marks)
	out := dueAlerts(qs, marks, pct, bal, now)
	if after, _ := json.Marshal(marks); string(after) != string(before) {
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, append(after, '\n'), 0o600)
	}
	return out
}

// quotaAlertEvery is how often the allowances are read for the alerts: the
// vendors' private endpoints limit how often they are asked, and the
// windows move slowly.
const quotaAlertEvery = 5 * time.Minute

// WatchQuotas reads every allowance and balance while an alert is set, and
// hands send each one due, until ctx ends. wake, when it gets a value,
// has it look at once (an alert just turned on).
func WatchQuotas(ctx context.Context, wake <-chan struct{}, send func(QuotaAlert)) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-wake:
		}
		if s := settings.Load(); s.UsageAlert > 0 || s.BalanceAlert > 0 {
			cx, cancel := context.WithTimeout(ctx, time.Minute)
			qs := Quotas(cx)
			cancel()
			for _, a := range checkAlerts(quotaAlertPath(), qs, s.UsageAlert, s.BalanceAlert, time.Now()) {
				log.Printf("usage alert: %s %s %s %.0f%% %s", a.Name, a.User, a.Window, a.Used, a.Balance)
				send(a)
			}
		}
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(quotaAlertEvery)
	}
}

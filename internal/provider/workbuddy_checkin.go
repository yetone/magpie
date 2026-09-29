package provider

// WorkBuddy's daily check-in (签到): while its event runs, a WorkBuddy
// (China) account is given a few credits a day for pressing 签到 in the
// app, more on a streak. While the setting is on, whichever magpie runs the
// gateway presses it for each signed-in account once a Beijing day, as
// WorkBuddy does: it asks the event's status, and claims when the event is
// on and today's isn't in yet.
//
// Once a day: what came of it is kept in workbuddy-checkin.json, by site
// and account id (no name, no token), so a restart doesn't ask again. An
// answer — claimed, already in, not eligible, the event over — holds for
// the rest of the day; a request that didn't get one (the network, a
// refused token) is tried again after wbCheckinRetry.
//
// Only the Chinese build has the event: WorkBuddy AI ships with it off.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

const (
	// wbCheckinEvery is how often the accounts are looked at.
	wbCheckinEvery = 30 * time.Minute
	// wbCheckinRetry is how long a check-in that got no answer waits
	// before it is tried again the same day.
	wbCheckinRetry = 30 * time.Minute
)

// WorkBuddy's check-in answers, the codes its claim refuses with.
const (
	wbCheckinAlready    = 1001 // today's is in already
	wbCheckinIneligible = 1002 // the account can't take part
	wbCheckinEnded      = 1003 // the event is over
)

// What came of a day's check-in, WorkBuddyCheckin.Outcome.
const (
	CheckinClaimed    = "claimed"    // claimed by magpie
	CheckinDone       = "done"       // in already, pressed in WorkBuddy or before
	CheckinIneligible = "ineligible" // the account can't take part
	CheckinInactive   = "inactive"   // no event running, or it has ended
	CheckinFailed     = "failed"     // no answer; tried again later that day
)

// beijing is the day the event counts in, UTC+8 all year; fixed, so it
// needs no time zone database (Windows has none Go can read without one).
var beijing = time.FixedZone("CST", 8*60*60)

// wbCheckinDay is the Beijing day ("2006-01-02") t falls on.
func wbCheckinDay(t time.Time) string { return t.In(beijing).Format("2006-01-02") }

// WorkBuddyCheckin is what came of a WorkBuddy account's check-in on Day
// (Beijing): the credits it gave and the streak it made, or why none.
type WorkBuddyCheckin struct {
	User    string    `json:"user,omitempty"` // as listed now; never kept
	Day     string    `json:"day"`
	At      time.Time `json:"at"`
	Outcome string    `json:"outcome"`
	Credit  float64   `json:"credit,omitempty"`
	Streak  int       `json:"streak,omitempty"`
	Msg     string    `json:"msg,omitempty"`
	// Asked is true of one checked in on this run, not one read back.
	Asked bool `json:"-"`
}

// Checked says whether the day's credits are in, claimed now or before.
func (r WorkBuddyCheckin) Checked() bool {
	return r.Outcome == CheckinClaimed || r.Outcome == CheckinDone
}

// settled says whether r answers for day: it is that day's, and not a
// failure due another try (at now; soon, at once).
func (r WorkBuddyCheckin) settled(day string, now time.Time, soon bool) bool {
	if r.Day != day {
		return false
	}
	return r.Outcome != CheckinFailed || !soon && now.Sub(r.At) < wbCheckinRetry
}

// wbCheckinStatus is the event as an account sees it.
type wbCheckinStatus struct {
	Active         bool  `json:"active"`
	TodayCheckedIn bool  `json:"today_checked_in"`
	StreakDays     wbNum `json:"streak_days"`
	DailyCredit    wbNum `json:"daily_credit"`
	TodayCredit    wbNum `json:"today_credit"`
}

// wbCheckinClaim is what a claim gave.
type wbCheckinClaim struct {
	Credit        wbNum  `json:"credit"`
	StreakDays    wbNum  `json:"streak_days"`
	IsStreakDay   bool   `json:"is_streak_day"`
	CreditDelayed bool   `json:"credit_delayed"`
	Message       string `json:"message"`
}

func wbCheckinPath() string { return filepath.Join(filepath.Dir(Path()), "workbuddy-checkin.json") }

// wbCheckinKey is an account's entry in the file: its site and id.
func wbCheckinKey(a wbAccount) string { return a.site.id + "|" + a.creds.UID }

func readCheckins(path string) map[string]WorkBuddyCheckin {
	st := map[string]WorkBuddyCheckin{}
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

// wbCheckinMu keeps two check-ins in one magpie (the loop and a press of
// the CLI's) from asking for one account at once.
var wbCheckinMu sync.Mutex

// wbCheckiner is the check-in with what it reads given, for tests.
type wbCheckiner struct {
	path     string
	now      func() time.Time
	accounts func() []wbAccount
}

func newWBCheckiner() wbCheckiner {
	return wbCheckiner{path: wbCheckinPath(), now: time.Now, accounts: func() []wbAccount { return wbLogins(wbCN) }}
}

// checkinNow checks each account in use in for today that isn't yet, and
// keeps what came of it; soon, a failure is tried again without waiting
// out wbCheckinRetry. It says how every account stands, those it didn't
// ask as last kept.
func (c wbCheckiner) checkinNow(ctx context.Context, soon bool) []WorkBuddyCheckin {
	wbCheckinMu.Lock()
	defer wbCheckinMu.Unlock()
	st := readCheckins(c.path)
	now := c.now()
	day := wbCheckinDay(now)
	var out []WorkBuddyCheckin
	changed := false
	for _, a := range c.accounts() {
		if !a.On || a.creds.UID == "" {
			continue
		}
		key := wbCheckinKey(a)
		prev, seen := st[key]
		if seen && prev.settled(day, now, soon) {
			prev.User = a.User
			out = append(out, prev)
			continue
		}
		r := wbCheckin(ctx, a)
		r.Day, r.At = day, now
		st[key], changed = r, true
		r.User, r.Asked = a.User, true
		out = append(out, r)
	}
	if changed {
		if b, err := json.MarshalIndent(st, "", "  "); err == nil {
			if err := writePrivate(c.path, append(b, '\n')); err != nil {
				log.Printf("workbuddy check-in: %v", err)
			}
		}
	}
	return out
}

// wbCheckin checks a in for the day as WorkBuddy's 签到 does: the event's
// status first, and a claim only while it runs and today's isn't in.
func wbCheckin(ctx context.Context, a wbAccount) WorkBuddyCheckin {
	creds, err := wbFresh(ctx, a)
	if err != nil {
		return WorkBuddyCheckin{Outcome: CheckinFailed, Msg: err.Error()}
	}
	h := wbAuthHeaders(a.site, creds)
	var st wbCheckinStatus
	if err := wbCall(ctx, http.MethodPost, a.site.api()+"/v2/billing/meter/checkin-activity-status", h, map[string]any{}, &st); err != nil {
		return wbCheckinRefused(err)
	}
	switch {
	case !st.Active:
		return WorkBuddyCheckin{Outcome: CheckinInactive}
	case st.TodayCheckedIn:
		return WorkBuddyCheckin{Outcome: CheckinDone, Credit: float64(st.TodayCredit), Streak: int(st.StreakDays)}
	}
	var got wbCheckinClaim
	if err := wbCall(ctx, http.MethodPost, a.site.api()+"/v2/billing/meter/daily-checkin", h, map[string]any{}, &got); err != nil {
		r := wbCheckinRefused(err)
		if r.Outcome == CheckinDone {
			r.Streak = int(st.StreakDays)
		}
		return r
	}
	return WorkBuddyCheckin{Outcome: CheckinClaimed, Credit: float64(got.Credit), Streak: int(got.StreakDays), Msg: got.Message}
}

// wbCheckinRefused is what an error from the event's API comes to: one of
// its answers for the day, or a failure to try again.
func wbCheckinRefused(err error) WorkBuddyCheckin {
	var we *wbError
	if errors.As(err, &we) {
		switch we.code {
		case wbCheckinAlready:
			return WorkBuddyCheckin{Outcome: CheckinDone, Msg: we.msg}
		case wbCheckinIneligible:
			return WorkBuddyCheckin{Outcome: CheckinIneligible, Msg: we.msg}
		case wbCheckinEnded:
			return WorkBuddyCheckin{Outcome: CheckinInactive, Msg: we.msg}
		}
	}
	return WorkBuddyCheckin{Outcome: CheckinFailed, Msg: err.Error()}
}

// CheckInWorkBuddy checks each WorkBuddy (China) account in use in for
// today now, those not in yet, and says how each stands.
func CheckInWorkBuddy(ctx context.Context) []WorkBuddyCheckin {
	return newWBCheckiner().checkinNow(ctx, true)
}

// WorkBuddyCheckins is each WorkBuddy (China) account's last check-in, as
// kept, by its name now; asks nothing.
func WorkBuddyCheckins() []WorkBuddyCheckin {
	st := readCheckins(wbCheckinPath())
	var out []WorkBuddyCheckin
	for _, a := range wbLogins(wbCN) {
		if r, ok := st[wbCheckinKey(a)]; ok && a.On {
			r.User = a.User
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].User < out[j].User })
	return out
}

// HasWorkBuddy says whether a WorkBuddy (China) account is signed in.
func HasWorkBuddy() bool { return len(wbLogins(wbCN)) > 0 }

// KeepWorkBuddyCheckedIn checks the WorkBuddy accounts in each day while
// settings say to: two minutes after it starts, every wbCheckinEvery after
// that, and as the Beijing day turns, until ctx ends.
func KeepWorkBuddyCheckedIn(ctx context.Context) {
	keepCheckedIn(ctx, newWBCheckiner(), func() bool { return settings.Load().WorkBuddyCheckin })
}

// keepCheckedIn runs c while on says to. The clock is looked at every
// minute: a machine that slept through the day's turn notices on waking.
func keepCheckedIn(ctx context.Context, c wbCheckiner, on func() bool) {
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	var last time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		// the wall clock, which goes on while the machine sleeps
		now := c.now().Round(0)
		if on() && (last.IsZero() || now.Sub(last) >= wbCheckinEvery || wbCheckinDay(now) != wbCheckinDay(last)) {
			last = now
			cx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			for _, r := range c.checkinNow(cx, false) {
				switch {
				case !r.Asked:
				case r.Outcome == CheckinClaimed:
					log.Printf("workbuddy check-in: %s +%g, a %d-day streak", r.User, r.Credit, r.Streak)
				case r.Outcome == CheckinFailed:
					log.Printf("workbuddy check-in: %s: %s", r.User, r.Msg)
				default:
					log.Printf("workbuddy check-in: %s: %s", r.User, r.Outcome)
				}
			}
			cancel()
		}
		t.Reset(time.Minute)
	}
}

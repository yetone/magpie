package provider

// A daily check-in, whichever vendor's: WorkBuddy's (workbuddy_checkin.go)
// and Trae CN's (trae_checkin.go). Each account in use is checked in once
// a Beijing day, except Qoder's campaigns and ZCode's gift plans,
// rechecked every 30 minutes but for a captcha or own-app answer, which
// waits on the user, and an answer that may be the vendor's day before
// (provisional, #3326), asked again every 30 minutes until a claim or an answer from noon on.
// A press asks the vendor again for a done, inactive or ineligible one.
// What came of it is kept in the vendor's file by its key for reuse
// (see workbuddy_checkin.go for when a failure is tried again).

import (
	"cmp"
	"context"
	"encoding/json"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/settings"
)

// checkinAcct is an account checked in each day.
type checkinAcct struct {
	User     string
	On       bool
	key      string // its entry in the file; "" for one that can't be told apart
	provider string // the provider its card is under, which the user can switch off
	do       func(context.Context) WorkBuddyCheckin
}

// checkiner checks the accounts it lists in, keeping what came of it at
// path; mu keeps two check-ins (the loop and a press) from asking for one
// account at once.
type checkiner struct {
	path     string
	now      func() time.Time
	mu       *sync.Mutex
	by       string // the vendor, WorkBuddyCheckin.By; "" for WorkBuddy
	label    string // the vendor in the log
	recheck  bool   // its campaigns can change before the Beijing day ends
	accounts func() []checkinAcct
	// only, of a press for one account (CheckInAccount), is its key: the
	// others are neither asked nor said, and it is asked even while left
	// out of the daily check-in, whose switch it leaves as it is
	only string
}

// checkinRunner is a check-in the daily loop runs.
type checkinRunner interface {
	checkinNow(ctx context.Context, soon bool) []WorkBuddyCheckin
	clock() time.Time
}

func (c checkiner) clock() time.Time { return c.now() }

// checkinNow checks each account in use in for today that isn't yet, and
// keeps what came of it; soon, a failure is tried again without waiting
// out wbCheckinRetry. It says how every account stands, those it didn't
// ask as last kept.
func (c checkiner) checkinNow(ctx context.Context, soon bool) []WorkBuddyCheckin {
	if discoveryOff() {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	day := wbCheckinDay(now)
	st, err := readCheckins(c.path)
	if err != nil {
		// unknown, never empty: nothing is asked or written, the file
		// is left as it is, and each account says why
		return c.unread(now, err)
	}
	var out []WorkBuddyCheckin
	changed := false
	// one account signed in twice (the built-in and the plugin) is
	// checked in once
	asked := map[string]bool{}
	// and an account the user left out is not asked at all, by the loop
	// or a press (#1902)
	off := checkinOff()
	for _, a := range c.accounts() {
		if !a.On || a.key == "" || asked[a.key] || !c.reaches(a.key, off) {
			continue
		}
		// nor, by the loop, one whose provider is switched off or removed
		// (leftAlone); a press still checks it in
		if !soon && leftAlone(a.provider) {
			continue
		}
		asked[a.key] = true
		prev, seen := st[a.key]
		settled := prev.settled(day, now, soon)
		// a press asks the vendor again for a done, inactive or
		// ineligible magpie saw no claim for: answered from the record,
		// a press never reached the vendor (#3326). A claim, and an
		// answer the user acts on (own-app, captcha), stand.
		if soon && prev.unclaimedAnswer() {
			settled = false
		}
		// and the loop asks again an answer that may be the vendor's
		// day before (#3326), unless the vendor said when its day turns
		// (ValidUntil, held to in settled)
		if settled && prev.provisional() && prev.ValidUntil.IsZero() {
			settled = now.Sub(prev.At) < wbCheckinEvery
		}
		if c.recheck {
			// a captcha or own-app answer waits on the user, in the
			// vendor's app, so the loop leaves it for the day: asked
			// again every 30 minutes, ZCode's claim only answered 3007
			// again, and the reporter's accounts went on to 3012
			// (plugins#503). A press still asks.
			// A failure held to its until waits it out (heldUntil).
			waits := prev.Outcome == CheckinCaptcha || prev.Outcome == CheckinOwnApp || now.Before(prev.heldUntil())
			settled = settled && !soon && (waits || now.Sub(prev.At) < wbCheckinEvery)
			if prev.Checked() && !prev.ValidUntil.IsZero() {
				settled = settled && now.Before(prev.ValidUntil)
			}
		}
		if seen && settled {
			prev.User, prev.By = a.User, c.by
			out = append(out, prev.shown())
			continue
		}
		// through the account's own proxy, else its provider's, as its
		// usage is read (loginQuota), never the global one past them: a
		// provider set to direct whose check-in went out through the
		// global proxy was refused there (#2777)
		r := a.do(ViaLogin(ctx, a.provider, a.User))
		r.Day, r.At = day, now
		if r.Offline && seen && prev.Day == day && prev.Offline {
			r.Tries = prev.Tries + 1
		}
		switch {
		case r.Outcome == CheckinClaimed:
			r.Claimed = now
		case seen && prev.claimedOn(day):
			// a done after magpie's own claim is that claim
			r.Claimed = cmp.Or(prev.Claimed, prev.At)
		}
		st[a.key], changed = r, true
		r.User, r.By, r.Asked = a.User, c.by, true
		out = append(out, r.shown())
	}
	if changed {
		if b, err := json.MarshalIndent(st, "", "  "); err == nil {
			if err := writePrivate(c.path, append(b, '\n')); err != nil {
				log.Printf("%s check-in: %v", c.label, err)
			} else {
				wroteCheckins(c.path, st)
			}
		}
	}
	return out
}

// unread is how the accounts checkinNow would look at stand while its file
// can't be read (err, logged by readCheckins): each failed with err, at
// now, so the loop tries again after wbCheckinRetry and a press says why;
// none was asked.
func (c checkiner) unread(now time.Time, err error) []WorkBuddyCheckin {
	var out []WorkBuddyCheckin
	listed := map[string]bool{}
	off := checkinOff()
	for _, a := range c.accounts() {
		if !a.On || a.key == "" || listed[a.key] || !c.reaches(a.key, off) {
			continue
		}
		listed[a.key] = true
		out = append(out, WorkBuddyCheckin{User: a.User, By: c.by, Day: wbCheckinDay(now), At: now, Outcome: CheckinFailed, Msg: err.Error()})
	}
	return out
}

// reaches says whether a run of c asks the account at key: one the user
// left out (off) only by a press for it alone (only).
func (c checkiner) reaches(key string, off map[string]bool) bool {
	if c.only != "" {
		return key == c.only
	}
	return !off[key]
}

// checkinOff are the accounts left out of their vendor's daily check-in
// (settings.CheckinOff), by their entry in its file.
func checkinOff() map[string]bool {
	off := map[string]bool{}
	for _, k := range settings.Load().CheckinOff {
		off[k] = true
	}
	return off
}

// CheckinAccount is an account a daily check-in reaches, for Settings: its
// entry in its vendor's file (Key, which settings.CheckinOff names), the
// vendor (By, as WorkBuddyCheckin's), and whether it is checked in while
// its vendor's check-in is on (#1902, magicLaLa: 根据每个账号设置是否开启
// 自动签到).
type CheckinAccount struct {
	Key  string `json:"key"`
	By   string `json:"by"`
	User string `json:"user"`
	On   bool   `json:"on"`
}

// CheckinAccounts are the accounts in use that each vendor's check-in
// reaches, WorkBuddy's, Trae CN's, MiniMax Code's, Qoder's and the
// plugins' that check in themselves, each once; asks nothing.
func CheckinAccounts() []CheckinAccount {
	cs := checkiners()
	off := checkinOff()
	var out []CheckinAccount
	seen := map[string]bool{}
	for _, c := range cs {
		var mine []CheckinAccount
		for _, a := range c.accounts() {
			if !a.On || a.key == "" || seen[c.by+"\x00"+a.key] {
				continue
			}
			seen[c.by+"\x00"+a.key] = true
			mine = append(mine, CheckinAccount{Key: a.key, By: c.by, User: a.User, On: !off[a.key]})
		}
		sort.SliceStable(mine, func(i, j int) bool { return mine[i].User < mine[j].User })
		out = append(out, mine...)
	}
	return out
}

// checkiners are every vendor's check-in: WorkBuddy's, Trae CN's, MiniMax
// Code's, Qoder's and each plugin's that checks in itself, with its name
// (a plugin's provider's, for WorkBuddyCheckin.Vendor; "" for a built-in).
func checkiners() []checkiner {
	cs, _ := namedCheckiners()
	return cs
}

func namedCheckiners() ([]checkiner, []string) {
	cs := []checkiner{
		newWBCheckiner().checkiner(),
		newTraeCheckiner(traeCheckinAccounts),
		newMiniMaxCheckiner(miniMaxCheckinAccounts),
		newQoderCheckiner(qoderCheckinAccounts, time.Now),
	}
	names := make([]string, len(cs))
	for _, p := range pluginCheckinProviders() {
		cs = append(cs, p.checkiner(time.Now))
		names = append(names, p.Name)
	}
	return cs, names
}

// CheckInAccount checks one account in for today now, as a press of Check
// in now does for its vendor's (CheckinAccount's By and Key): asked again
// unless claimed today, even while left out of the daily check-in, and
// no switch changed (xiaowu_95660: 手动签到一次). It says how the account
// stands, or nothing for an account no check-in reaches.
func CheckInAccount(ctx context.Context, by, key string) []WorkBuddyCheckin {
	if key == "" {
		return nil
	}
	cs, names := namedCheckiners()
	for i, c := range cs {
		if c.by != by {
			continue
		}
		c.only = key
		rs := c.checkinNow(ctx, true)
		for j := range rs {
			if names[i] != "" {
				rs[j].Vendor = names[i]
			}
		}
		return rs
	}
	return nil
}

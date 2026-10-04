package provider

// A daily check-in, whichever vendor's: WorkBuddy's (workbuddy_checkin.go)
// and Trae CN's (trae_checkin.go). Each account in use is checked in once
// a Beijing day; what came of it is kept in the vendor's file by its key,
// so a restart doesn't ask again (see workbuddy_checkin.go for when a
// failure is tried again).

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"
)

// checkinAcct is an account checked in each day.
type checkinAcct struct {
	User string
	On   bool
	key  string // its entry in the file; "" for one that can't be told apart
	do   func(context.Context) WorkBuddyCheckin
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
	accounts func() []checkinAcct
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
	c.mu.Lock()
	defer c.mu.Unlock()
	st := readCheckins(c.path)
	now := c.now()
	day := wbCheckinDay(now)
	var out []WorkBuddyCheckin
	changed := false
	// one account signed in twice (the built-in and the plugin) is
	// checked in once
	asked := map[string]bool{}
	for _, a := range c.accounts() {
		if !a.On || a.key == "" || asked[a.key] {
			continue
		}
		asked[a.key] = true
		prev, seen := st[a.key]
		if seen && prev.settled(day, now, soon) {
			prev.User, prev.By = a.User, c.by
			out = append(out, prev)
			continue
		}
		r := a.do(ctx)
		r.Day, r.At = day, now
		if r.Offline && seen && prev.Day == day && prev.Offline {
			r.Tries = prev.Tries + 1
		}
		st[a.key], changed = r, true
		r.User, r.By, r.Asked = a.User, c.by, true
		out = append(out, r)
	}
	if changed {
		if b, err := json.MarshalIndent(st, "", "  "); err == nil {
			if err := writePrivate(c.path, append(b, '\n')); err != nil {
				log.Printf("%s check-in: %v", c.label, err)
			}
		}
	}
	return out
}

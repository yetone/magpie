package provider

// One GLM Coding Plan seat added twice, as a key provider and as a
// signed-in ZCode account, is shown once, on the account's card (#90,
// f02d37bb; scoped to GLM by #953): two cards would count its allowance
// twice, and routing goes by what a card has left. The two are told to
// be one seat by their windows' resets (seatMatch), which a failed read
// or a window not used yet doesn't have. So once they matched, that is
// remembered (seats.json, beside quotas.json): a read that fails is
// unknown, not "another seat", and doesn't bring the key's card back
// for a minute (#1515, aindijrncom). Only windows that disagree, or the
// account signed out, do. The account's card is then named for the key
// plan it stands in for (Seat), unless the user named it themselves.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// seat is the signed-in account a key's plan was found to be.
type seat struct {
	Provider string `json:"provider"`
	User     string `json:"user,omitempty"` // the account, in lower case
}

var seatMemo struct {
	sync.Mutex
	path string          // the file m was read from; another (a test's) is read afresh
	m    map[string]seat // by planSeatKey
}

func seatsPath() string { return filepath.Join(filepath.Dir(Path()), "seats.json") }

// planSeatKey names a key's plan card: its provider and key label.
func planSeatKey(p SubscriptionQuota) string { return p.Provider + "/" + accountKey(p.User) }

// seatsLocked is the memo, read from disk the first time; the caller
// holds seatMemo.
func seatsLocked() map[string]seat {
	if path := seatsPath(); seatMemo.m == nil || seatMemo.path != path {
		seatMemo.path, seatMemo.m = path, map[string]seat{}
		if b, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(b, &seatMemo.m)
		}
	}
	return seatMemo.m
}

func writeSeatsLocked() {
	b, err := json.MarshalIndent(seatMemo.m, "", "  ")
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(seatMemo.path), 0o700)
	_ = writeFileAtomic(seatMemo.path, b)
}

// seatMatch says whether a key's plan a and the account's card b are one
// seat: 1 when their windows' resets agree (at least one, as sameAccount
// asks), -1 when they don't or b can't be a GLM seat, 0 when it can't be
// told: either failed to read, a is not known to be GLM's (a card kept
// from disk), or no window of both has a reset yet.
func seatMatch(a, b SubscriptionQuota) int {
	if b.Provider != "zcode" && b.Provider != "zcode-plugin" {
		return -1
	}
	if !a.glmPlan || a.Error != "" || b.Error != "" {
		return 0
	}
	matched := false
	for _, x := range a.Windows {
		for _, y := range b.Windows {
			if x.Aside || y.Aside || x.Span == 0 || x.Span != y.Span || x.ResetsAt == nil || y.ResetsAt == nil {
				continue
			}
			if !x.ResetsAt.Equal(*y.ResetsAt) {
				return -1
			}
			matched = true
		}
	}
	if matched {
		return 1
	}
	return 0
}

// sameAccount is seatMatch's "the same seat", read afresh.
func sameAccount(a, b SubscriptionQuota) bool { return seatMatch(a, b) == 1 }

// seatName is how the account card standing in for plan p names the
// seat: the key provider's name, and the key's when it has several.
func seatName(p SubscriptionQuota) string {
	if p.User != "" && p.User != p.Name {
		return p.Name + " · " + p.User
	}
	return p.Name
}

// seatCards is the plans not already on an account's card (shown) and
// subs with each such card's Seat set (named). A plan is on an account's
// card when they read as one seat now, or did before and the account is
// still signed in while nothing read now tells them apart. Neither
// argument is changed: both are caches'.
func seatCards(plans, subs []SubscriptionQuota) (shown, named []SubscriptionQuota) {
	named = slices.Clone(subs)
	seatMemo.Lock()
	defer seatMemo.Unlock()
	memo, changed := seatsLocked(), false
	for _, p := range plans {
		if p.From != "" {
			shown = append(shown, p)
			continue
		}
		k := planSeatKey(p)
		at := -1
		for i, s := range named {
			if s.From == "" && seatMatch(p, s) == 1 {
				at = i
				break
			}
		}
		if at >= 0 {
			if s := (seat{named[at].Provider, accountKey(named[at].User)}); memo[k] != s {
				memo[k], changed = s, true
			}
		} else if m, ok := memo[k]; ok {
			at = slices.IndexFunc(named, func(s SubscriptionQuota) bool {
				return s.From == "" && s.Provider == m.Provider && accountKey(s.User) == m.User
			})
			if at >= 0 && seatMatch(p, named[at]) < 0 {
				// the windows tell them apart now: another seat after all
				delete(memo, k)
				changed, at = true, -1
			}
		}
		if at < 0 {
			shown = append(shown, p)
			continue
		}
		if named[at].Seat == "" {
			named[at].Seat = seatName(p)
		}
	}
	if changed {
		writeSeatsLocked()
	}
	return shown, named
}

// notShown is the plans not already on an account's card (seatCards).
func notShown(plans, subs []SubscriptionQuota) []SubscriptionQuota {
	shown, _ := seatCards(plans, subs)
	return shown
}

// SeatNameOf is the name of the key plan the account user of the
// subscription id was found to stand in for, "" when none: the plan's
// provider as it is named now, and its key's name when it has several.
func SeatNameOf(id, user string) string {
	seatMemo.Lock()
	memo := seatsLocked()
	var plans []string
	for k, s := range memo {
		if s.Provider == id && s.User == accountKey(user) {
			plans = append(plans, k)
		}
	}
	seatMemo.Unlock()
	slices.Sort(plans)
	for _, k := range plans {
		pid, key, _ := strings.Cut(k, "/")
		p, err := Find(pid)
		if err != nil || p.Off {
			continue
		}
		label := ""
		for _, x := range p.Keys {
			if accountKey(x.Name) == key && key != "" {
				label = x.Name
			}
		}
		if accountKey(p.KeyName) == key && key != "" {
			label = p.KeyName
		}
		return seatName(SubscriptionQuota{Name: p.Name, User: label})
	}
	return ""
}

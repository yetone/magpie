package provider

// Trae CN's daily check-in (每日签到, #694: 100 credits a day): Trae CN is
// served only by its community plugin, @magpie-community/opencode-trae-auth
// (provider "trae-cn"; no built-in, no mover). While the setting is on,
// whichever magpie runs the gateway presses it for each of the plugin's
// accounts once a Beijing day, as Trae's IDE does: it asks the check-in's
// status on api.trae.cn, and claims when it is on and today's isn't in.
// Both go through the plugin's fetch (0.1.4 and later), which sends them
// as the account (its Cloud-IDE-JWT, renewed when near its end, and its
// device id).
//
// What came of it is kept in trae-checkin.json by account id, as
// WorkBuddy's is (checkin.go): an answer holds for the day, a request that
// got none is tried again later. Trae counts a check-in by device as well
// as by account (9095: this device is in for today); that is an answer
// for the day, and magpie doesn't send another device id to get round it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// TraeCNID is the Trae CN plugin's provider.
const TraeCNID = "trae-cn"

// traeCheckinURL is Trae CN's check-in page: /status, then /claim.
var traeCheckinURL = "https://api.trae.cn/trae/api/v2/ug/checkin_credits"

// Trae CN's check-in answers, the codes it refuses with.
const (
	traeSignedOut     = 1001 // the token isn't taken
	traeRateLimited   = 9074 // asked too often; tried again later
	traeDeviceChecked = 9095 // this device is in for today
)

// traeCheckinStatus is the check-in as an account sees it.
type traeCheckinStatus struct {
	Code         int     `json:"code"`
	Message      string  `json:"message"`
	Enable       bool    `json:"enable"`
	CheckedIn    bool    `json:"checked_in"`
	Credits      float64 `json:"credits"`
	ExtraCredits float64 `json:"extra_credits"`
}

// traeAccount is a Trae CN account checked in: via sends a request as it.
type traeAccount struct {
	User string
	On   bool
	uid  string
	card string
	via  func(*http.Request) (*http.Response, error)
}

func traeCheckinPath() string { return filepath.Join(filepath.Dir(Path()), "trae-checkin.json") }

func traeCheckinKey(a traeAccount) string {
	if a.uid == "" {
		return ""
	}
	return TraeCNID + "|" + a.uid
}

var traeCheckinMu sync.Mutex

func newTraeCheckiner(accounts func() []traeAccount) checkiner {
	return checkiner{path: traeCheckinPath(), now: time.Now, mu: &traeCheckinMu, by: "trae", label: "trae", accounts: func() []checkinAcct {
		var out []checkinAcct
		for _, a := range accounts() {
			out = append(out, checkinAcct{User: a.User, On: a.On, key: traeCheckinKey(a), do: func(ctx context.Context) WorkBuddyCheckin { return traeCheckin(ctx, a) }})
		}
		return out
	}}
}

// traeCheckin checks a in for the day as Trae's IDE does: the status
// first, and a claim only while it is on and today's isn't in.
func traeCheckin(ctx context.Context, a traeAccount) WorkBuddyCheckin {
	var st traeCheckinStatus
	if err := traeCall(ctx, a, "/status", &st); err != nil {
		return wbCheckinFailed(err)
	}
	if st.Code != 0 {
		return traeRefused(st.Code, st.Message)
	}
	switch {
	case !st.Enable:
		return WorkBuddyCheckin{Outcome: CheckinInactive}
	case st.CheckedIn:
		return WorkBuddyCheckin{Outcome: CheckinDone, Credit: st.Credits}
	}
	var got traeCheckinStatus
	if err := traeCall(ctx, a, "/claim", &got); err != nil {
		return wbCheckinFailed(err)
	}
	if got.Code != 0 {
		return traeRefused(got.Code, got.Message)
	}
	credit := got.Credits
	if credit == 0 {
		credit = st.Credits
	}
	return WorkBuddyCheckin{Outcome: CheckinClaimed, Credit: credit + got.ExtraCredits}
}

// traeRefused is what a code Trae answered with comes to: an answer for
// the day, or a failure to try again.
func traeRefused(code int, msg string) WorkBuddyCheckin {
	switch code {
	case traeDeviceChecked:
		return WorkBuddyCheckin{Outcome: CheckinIneligible, Msg: orStr(msg, "this device has checked in today")}
	case traeSignedOut:
		return WorkBuddyCheckin{Outcome: CheckinFailed, Msg: "Trae CN's sign-in has expired — sign in again"}
	case traeRateLimited:
		return WorkBuddyCheckin{Outcome: CheckinFailed, Msg: orStr(msg, "asked too often")}
	}
	return WorkBuddyCheckin{Outcome: CheckinFailed, Msg: fmt.Sprintf("code %d: %s", code, msg)}
}

func orStr(s, or string) string {
	if strings.TrimSpace(s) == "" {
		return or
	}
	return s
}

// traeCall posts {} to the check-in's path as a and reads its answer.
func traeCall(ctx context.Context, a traeAccount, path string, dst *traeCheckinStatus) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, traeCheckinURL+path, strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := a.via(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if strings.EqualFold(res.Header.Get("X-Magpie-Sign-In"), "expired") || res.StatusCode == http.StatusUnauthorized {
		return errors.New("Trae CN's sign-in has expired — sign in again")
	}
	if err := json.Unmarshal(b, dst); err != nil || res.StatusCode < 200 || res.StatusCode >= 300 {
		if res.StatusCode == http.StatusBadRequest && err != nil {
			// a plugin before 0.1.4 serves chat completions only
			return errors.New("the Trae CN plugin can't check in: update it to 0.1.4 or later")
		}
		if err == nil && dst.Code != 0 {
			return nil // Trae's own answer, in an error status
		}
		return &accountStatusError{status: res.StatusCode}
	}
	return nil
}

// CheckInTrae checks each Trae CN account in use in for today now, those
// not in yet, and says how each stands.
func CheckInTrae(ctx context.Context) []WorkBuddyCheckin {
	return newTraeCheckiner(traeCheckinAccounts).checkinNow(ctx, true)
}

// TraeCheckins is each Trae CN account's last check-in, as kept, by its
// name now; asks nothing.
func TraeCheckins() []WorkBuddyCheckin {
	st := readCheckins(traeCheckinPath())
	var out []WorkBuddyCheckin
	listed := map[string]bool{}
	for _, a := range traeCheckinAccounts() {
		key := traeCheckinKey(a)
		if r, ok := st[key]; ok && a.On && key != "" && !listed[key] {
			listed[key] = true
			r.User, r.By = a.User, "trae"
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].User < out[j].User })
	return out
}

// HasTrae says whether a Trae CN account is signed in.
func HasTrae() bool { return len(traeCheckinAccounts()) > 0 }

// traeCards are the usage cards of accts, for WithCheckins.
func traeCards(accts []traeAccount) []checkinCard {
	var out []checkinCard
	for _, a := range accts {
		if key := traeCheckinKey(a); a.On && key != "" {
			out = append(out, checkinCard{User: a.User, card: a.card, key: key})
		}
	}
	return out
}

// traeCheckinAccounts are the Trae CN plugin's accounts, each sent through
// the plugin's fetch, which signs it as the account.
func traeCheckinAccounts() []traeAccount {
	pp, ok := PluginOf(TraeCNID)
	if !ok {
		return nil
	}
	auths := plugin.Auths(pp.ID)
	card := PluginID(pp.ID)
	var out []traeAccount
	for _, l := range pluginLogins(pp) {
		key := l.acct.Key
		out = append(out, traeAccount{User: l.User, On: l.On, uid: str(auths[key]["uid"]), card: card,
			via: func(req *http.Request) (*http.Response, error) {
				h := map[string]string{}
				for k, vs := range req.Header {
					h[strings.ToLower(k)] = vs[0]
				}
				var body []byte
				if req.Body != nil {
					body, _ = io.ReadAll(req.Body)
				}
				return plugin.Fetch(req.Context(), plugin.FetchRequest{Provider: pp.ID, Account: key, URL: req.URL.String(), Method: req.Method, Headers: h, Body: body})
			}})
	}
	return out
}

// KeepTraeCheckedIn checks the Trae CN accounts in each day while settings
// say to, as KeepWorkBuddyCheckedIn does WorkBuddy's.
func KeepTraeCheckedIn(ctx context.Context) {
	keepCheckedIn(ctx, "trae", newTraeCheckiner(traeCheckinAccounts), func() bool { return settings.Load().TraeCheckin })
}

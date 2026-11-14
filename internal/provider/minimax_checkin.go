package provider

// MiniMax Code's daily check-in (签到, #811: a week of daily credits, more
// on days 4 and 7): MiniMax Code is served only by its community plugin,
// @magpie-community/opencode-minimax-auth (provider "minimax-code", the
// China site, and "minimax-code-global", the international one; no
// built-in, no mover). While the setting is on, whichever magpie runs the
// gateway presses it for each of the plugin's accounts once a Beijing day,
// as MiniMax Code's web page does: it asks the check-in's panel on the
// account's site (agent.minimax.cn, agent.minimax.io), and claims while
// today's is claimable. Both go
// through the plugin's fetch, which sends them as the account (its access
// token, renewed when near its end); the page's own signature
// (x-timestamp, x-signature) goes with them, though MiniMax doesn't check
// it now. The international site has the same check-in (ARNO on Discord):
// its page has the Daily check-in panel, and its /signin/status and /claim
// answer as the China site's do, refusing a bad signature and then asking
// for a sign-in.
//
// What came of it is kept in minimax-checkin.json by account id, as
// WorkBuddy's and Trae CN's are (checkin.go).

import (
	"cmp"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// MiniMaxCodeID is the MiniMax Code plugin's provider (the China site),
// MiniMaxCodeGlobalID its international site's.
const (
	MiniMaxCodeID       = "minimax-code"
	MiniMaxCodeGlobalID = "minimax-code-global"
)

// miniMaxCheckinURLs are MiniMax Code's check-in on each site, by the
// plugin's provider: /status, then /claim, each with the day's time zone.
var miniMaxCheckinURLs = map[string]string{
	MiniMaxCodeID:       "https://agent.minimax.cn/minimax-cloud/api/v1/signin",
	MiniMaxCodeGlobalID: "https://agent.minimax.io/minimax-cloud/api/v1/signin",
}

// miniMaxSignSecret is what MiniMax Code's page signs its requests with:
// x-signature is md5(seconds + it + the body).
const miniMaxSignSecret = "I*7Cf%WZ#S&%1RlZJ&C2"

// A day of MiniMax's check-in panel, by its status.
const (
	mmDayUpcoming  = 1
	mmDayClaimable = 2
	mmDayClaimed   = 3
	mmDayDisabled  = 4
)

// miniMaxDay is one day of the panel: the week's day_no, its credits.
type miniMaxDay struct {
	DayNo   int     `json:"day_no"`
	Points  float64 `json:"points"`
	Status  int     `json:"status"`
	IsToday bool    `json:"is_today"`
}

type miniMaxBase struct {
	Code int    `json:"status_code"`
	Msg  string `json:"status_msg"`
}

type miniMaxStatus struct {
	Data struct {
		Days []miniMaxDay `json:"days"`
	} `json:"data"`
	Base *miniMaxBase `json:"base_resp"`
}

type miniMaxClaim struct {
	Data struct {
		ClaimResult int     `json:"claim_result"` // 1 claimed now, 2 already today
		DayNo       int     `json:"day_no"`
		Points      float64 `json:"points"`
	} `json:"data"`
	Base *miniMaxBase `json:"base_resp"`
}

// miniMaxAccount is a MiniMax Code account checked in: site is the
// plugin's provider it is signed in to ("" the China site's), via sends a
// request as it.
type miniMaxAccount struct {
	User string
	On   bool
	site string
	uid  string
	card string
	via  func(*http.Request) (*http.Response, error)
}

func (a miniMaxAccount) siteID() string {
	if a.site == "" {
		return MiniMaxCodeID
	}
	return a.site
}

func miniMaxCheckinPath() string {
	return filepath.Join(filepath.Dir(Path()), "minimax-checkin.json")
}

func miniMaxCheckinKey(a miniMaxAccount) string {
	if a.uid == "" {
		return ""
	}
	return a.siteID() + "|" + a.uid
}

var miniMaxCheckinMu sync.Mutex

func newMiniMaxCheckiner(accounts func() []miniMaxAccount) checkiner {
	return checkiner{path: miniMaxCheckinPath(), now: time.Now, mu: &miniMaxCheckinMu, by: "minimax", label: "minimax", accounts: func() []checkinAcct {
		var out []checkinAcct
		for _, a := range accounts() {
			out = append(out, checkinAcct{User: a.User, On: a.On, key: miniMaxCheckinKey(a), provider: cmp.Or(a.card, a.siteID()), do: func(ctx context.Context) WorkBuddyCheckin { return miniMaxCheckin(ctx, a) }})
		}
		return out
	}}
}

// miniMaxCheckin checks a in for the day as MiniMax Code's page does: the
// panel first, and a claim only while today's is claimable. The streak is
// today's day of the week's panel.
func miniMaxCheckin(ctx context.Context, a miniMaxAccount) WorkBuddyCheckin {
	var st miniMaxStatus
	if err := miniMaxCall(ctx, a, http.MethodGet, "/status", &st); err != nil {
		return wbCheckinFailed(err)
	}
	if st.Base != nil && st.Base.Code != 0 {
		return WorkBuddyCheckin{Outcome: CheckinFailed, Msg: fmt.Sprintf("code %d: %s", st.Base.Code, st.Base.Msg)}
	}
	var today *miniMaxDay
	for i := range st.Data.Days {
		if st.Data.Days[i].IsToday {
			today = &st.Data.Days[i]
			break
		}
	}
	switch {
	case today == nil:
		return WorkBuddyCheckin{Outcome: CheckinInactive}
	case today.Status == mmDayClaimed:
		return WorkBuddyCheckin{Outcome: CheckinDone, Credit: today.Points, Streak: today.DayNo}
	case today.Status == mmDayDisabled:
		return WorkBuddyCheckin{Outcome: CheckinIneligible, Msg: "today's check-in is closed for this account"}
	}
	var got miniMaxClaim
	if err := miniMaxCall(ctx, a, http.MethodPost, "/claim", &got); err != nil {
		return wbCheckinFailed(err)
	}
	if got.Base != nil && got.Base.Code != 0 {
		return WorkBuddyCheckin{Outcome: CheckinFailed, Msg: fmt.Sprintf("code %d: %s", got.Base.Code, got.Base.Msg)}
	}
	day, credit := got.Data.DayNo, got.Data.Points
	if day == 0 {
		day = today.DayNo
	}
	if credit == 0 {
		credit = today.Points
	}
	switch got.Data.ClaimResult {
	case 1:
		return WorkBuddyCheckin{Outcome: CheckinClaimed, Credit: credit, Streak: day}
	case 2:
		return WorkBuddyCheckin{Outcome: CheckinDone, Credit: credit, Streak: day}
	}
	return WorkBuddyCheckin{Outcome: CheckinFailed, Msg: fmt.Sprintf("claim_result %d", got.Data.ClaimResult)}
}

// miniMaxCall asks the check-in's path as a, signed as MiniMax Code's page
// signs it, and reads its answer.
func miniMaxCall(ctx context.Context, a miniMaxAccount, method, path string, dst any) error {
	body := ""
	if method == http.MethodPost {
		body = "{}"
	}
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, miniMaxCheckinURLs[a.siteID()]+path+"?timezone_id=Asia%2FShanghai", rd)
	if err != nil {
		return err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sum := md5.Sum([]byte(ts + miniMaxSignSecret + body))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-timestamp", ts)
	req.Header.Set("x-signature", hex.EncodeToString(sum[:]))
	res, err := a.via(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if strings.EqualFold(res.Header.Get("X-Magpie-Sign-In"), "expired") || res.StatusCode == http.StatusUnauthorized {
		return errors.New("MiniMax Code's sign-in has expired — sign in again")
	}
	if err := json.Unmarshal(b, dst); err != nil || res.StatusCode < 200 || res.StatusCode >= 300 {
		if err == nil {
			// MiniMax's own answer, in an error status
			var e struct {
				Base *miniMaxBase `json:"base_resp"`
			}
			if json.Unmarshal(b, &e) == nil && e.Base != nil && e.Base.Code != 0 {
				return fmt.Errorf("code %d: %s", e.Base.Code, e.Base.Msg)
			}
		}
		return &accountStatusError{status: res.StatusCode}
	}
	return nil
}

// CheckInMiniMax checks each MiniMax Code account in use in for today now,
// those not in yet, and says how each stands.
func CheckInMiniMax(ctx context.Context) []WorkBuddyCheckin {
	return newMiniMaxCheckiner(miniMaxCheckinAccounts).checkinNow(ctx, true)
}

// MiniMaxCheckins is each MiniMax Code account's last check-in, as kept,
// by its name now; asks nothing.
func MiniMaxCheckins() []WorkBuddyCheckin {
	st := shownCheckins(miniMaxCheckinPath())
	var out []WorkBuddyCheckin
	listed := map[string]bool{}
	for _, a := range miniMaxCheckinAccounts() {
		key := miniMaxCheckinKey(a)
		if r, ok := st[key]; ok && a.On && key != "" && !listed[key] {
			listed[key] = true
			r.User, r.By = a.User, "minimax"
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].User < out[j].User })
	return out
}

// HasMiniMax says whether a MiniMax Code account is signed in, on either
// site.
func HasMiniMax() bool { return len(miniMaxCheckinAccounts()) > 0 }

// miniMaxCards are the usage cards of accts, for WithCheckins.
func miniMaxCards(accts []miniMaxAccount) []checkinCard {
	var out []checkinCard
	for _, a := range accts {
		if key := miniMaxCheckinKey(a); a.On && key != "" {
			out = append(out, checkinCard{User: a.User, card: a.card, key: key})
		}
	}
	return out
}

// miniMaxCheckinAccounts are the MiniMax Code plugin's accounts on both
// sites, each sent through the plugin's fetch, which signs it in as the
// account.
func miniMaxCheckinAccounts() []miniMaxAccount {
	var out []miniMaxAccount
	for _, site := range []string{MiniMaxCodeID, MiniMaxCodeGlobalID} {
		if pp, ok := PluginOf(site); ok && !pluginChecksIn(pp) {
			out = append(out, miniMaxSiteAccounts(site, pp)...)
		}
	}
	return out
}

func miniMaxSiteAccounts(site string, pp plugin.Provider) []miniMaxAccount {
	auths := plugin.Auths(pp.ID)
	card := PluginID(pp.ID)
	var out []miniMaxAccount
	for _, l := range pluginLogins(pp) {
		key := l.acct.Key
		uid := str(auths[key]["realUserID"])
		if uid == "" {
			uid = str(auths[key]["uid"])
		}
		out = append(out, miniMaxAccount{User: l.User, On: l.On, site: site, uid: uid, card: card,
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

// KeepMiniMaxCheckedIn checks the MiniMax Code accounts in each day while
// settings say to, as KeepTraeCheckedIn does Trae CN's.
func KeepMiniMaxCheckedIn(ctx context.Context) {
	keepCheckedIn(ctx, "minimax", newMiniMaxCheckiner(miniMaxCheckinAccounts), func() bool { return settings.Shared().MiniMaxCheckin })
}

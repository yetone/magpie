package provider

// Qoder's daily credits (ARNO on Discord: 100 Credits a day, claimed as
// wallechfox/qoder-checkin claims them): Qoder and Qoder CN list an
// account's campaigns on their openapi host, the day's credits among them
// (actionType CLAIM_BENEFIT), and claim one with a POST. While the setting
// is on, whichever magpie runs the gateway claims each claimable one for
// each account of @magpie-community/opencode-qoder-auth. Campaigns are
// checked every 30 minutes and on a Beijing day change: their daily
// rollover need not be midnight. The server decides which can be claimed.
// Both go through the plugin's fetch (0.2.7 and later), which sends them
// as the account, on its device token. The built-in's accounts, not moved
// onto the plugin, aren't checked in.
//
// What came of it is kept in qoder-checkin.json by site and account id, as
// WorkBuddy's and Trae CN's are (checkin.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/settings"
)

// qoderCampaignURLs are the campaigns of each site, by the plugin's
// provider; a claim is …/{id}/claim.
var qoderCampaignURLs = map[string]string{
	"qoder":   "https://openapi.qoder.sh/sash/api/v1/me/campaigns",
	QoderCNID: "https://openapi.qoder.com.cn/sash/api/v1/me/campaigns",
}

// qoderCampaign is one of an account's campaigns. startAt and endAt bound
// its claim window, which can cross Beijing midnight.
type qoderCampaign struct {
	ID          string  `json:"campaignId"`
	ActionType  string  `json:"actionType"`
	ClaimStatus string  `json:"claimStatus"`
	StartAt     float64 `json:"startAt"`
	EndAt       float64 `json:"endAt"`
	Benefit     struct {
		Amount float64 `json:"amount"`
	} `json:"benefit"`
}

type qoderClaim struct {
	Status  string `json:"status"`
	Benefit struct {
		Amount float64 `json:"amount"`
	} `json:"benefit"`
	Data *qoderClaim `json:"data"`
}

// qoderCheckinAcct is a Qoder account checked in: site is the plugin's
// provider it is signed in to, via sends a request as it.
type qoderCheckinAcct struct {
	User string
	On   bool
	site string
	uid  string
	card string
	via  func(*http.Request) (*http.Response, error)
}

func qoderCheckinPath() string { return filepath.Join(filepath.Dir(Path()), "qoder-checkin.json") }

func qoderCheckinKey(a qoderCheckinAcct) string {
	if a.uid == "" || a.site == "" {
		return ""
	}
	return a.site + "|" + a.uid
}

var qoderCheckinMu sync.Mutex

func newQoderCheckiner(accounts func() []qoderCheckinAcct, now func() time.Time) checkiner {
	return checkiner{path: qoderCheckinPath(), now: now, mu: &qoderCheckinMu, by: "qoder", label: "qoder", recheck: true, accounts: func() []checkinAcct {
		var out []checkinAcct
		for _, a := range accounts() {
			out = append(out, checkinAcct{User: a.User, On: a.On, key: qoderCheckinKey(a), do: func(ctx context.Context) WorkBuddyCheckin { return qoderCheckin(ctx, a, now()) }})
		}
		return out
	}}
}

// qoderCheckin claims a's claimable daily credits. None claimable: one
// claimed for the day now is a check-in done, no daily campaign at all is
// none running, and one neither is a failure, tried again later.
func qoderCheckin(ctx context.Context, a qoderCheckinAcct, now time.Time) WorkBuddyCheckin {
	var list struct {
		Campaigns []qoderCampaign `json:"campaigns"`
	}
	if err := qoderCall(ctx, a, http.MethodGet, qoderCampaignURLs[a.site], &list); err != nil {
		return wbCheckinFailed(err)
	}
	var daily []qoderCampaign
	for _, c := range list.Campaigns {
		if c.ActionType == "CLAIM_BENEFIT" {
			daily = append(daily, c)
		}
	}
	var credit float64
	var until time.Time
	claimed, failed := 0, 0
	var why string
	for _, c := range daily {
		if c.active(now) {
			if end := qoderWhen(c.EndAt); until.IsZero() || end.Before(until) {
				until = end
			}
		}
		if c.ClaimStatus != "CLAIMABLE" || c.ID == "" {
			continue
		}
		var got qoderClaim
		err := qoderCall(ctx, a, http.MethodPost, qoderCampaignURLs[a.site]+"/"+url.PathEscape(c.ID)+"/claim", &got)
		if got.Data != nil {
			got = *got.Data
		}
		if err == nil && got.Status != "CLAIMED" {
			err = fmt.Errorf("the claim answered %q", got.Status)
		}
		if err != nil {
			failed++
			why = err.Error()
			continue
		}
		claimed++
		if got.Benefit.Amount > 0 {
			credit += got.Benefit.Amount
		} else {
			credit += c.Benefit.Amount
		}
	}
	switch {
	case failed > 0:
		return WorkBuddyCheckin{Outcome: CheckinFailed, Msg: why}
	case claimed > 0:
		return WorkBuddyCheckin{Outcome: CheckinClaimed, Credit: credit, ValidUntil: until}
	case len(daily) == 0:
		return WorkBuddyCheckin{Outcome: CheckinInactive}
	}
	for _, c := range daily {
		if c.ClaimStatus == "CLAIMED" && c.active(now) {
			return WorkBuddyCheckin{Outcome: CheckinDone, Credit: c.Benefit.Amount, ValidUntil: until}
		}
	}
	return WorkBuddyCheckin{Outcome: CheckinFailed, Msg: "today's credits aren't offered yet"}
}

// active includes the start and excludes the end, regardless of the Beijing day.
func (c qoderCampaign) active(now time.Time) bool {
	return !now.Before(qoderWhen(c.StartAt)) && now.Before(qoderWhen(c.EndAt))
}

// qoderWhen is a time Qoder gives, in seconds or milliseconds.
func qoderWhen(v float64) time.Time {
	if v > 1e12 {
		return time.UnixMilli(int64(v))
	}
	return time.Unix(int64(v), 0)
}

// qoderCall asks u as a and reads its answer.
func qoderCall(ctx context.Context, a qoderCheckinAcct, method, u string, dst any) error {
	var rd io.Reader
	if method == http.MethodPost {
		rd = strings.NewReader("{}")
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	res, err := a.via(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if strings.EqualFold(res.Header.Get("X-Magpie-Sign-In"), "expired") || res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return errors.New("Qoder's sign-in has expired — sign in again")
	}
	if res.StatusCode == http.StatusNotFound && strings.Contains(string(b), "only chat completions") {
		// a plugin before 0.2.7 serves chat completions only
		return errors.New("the Qoder plugin can't check in: update it to 0.2.7 or later")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(b, &e) == nil {
			if m := orStr(e.Error.Message, e.Message); strings.TrimSpace(m) != "" {
				return fmt.Errorf("HTTP %d: %s", res.StatusCode, m)
			}
		}
		return &accountStatusError{status: res.StatusCode}
	}
	return json.Unmarshal(b, dst)
}

// CheckInQoder checks each Qoder account in use in for today now, those
// not in yet, and says how each stands.
func CheckInQoder(ctx context.Context) []WorkBuddyCheckin {
	return newQoderCheckiner(qoderCheckinAccounts, time.Now).checkinNow(ctx, true)
}

// QoderCheckins is each Qoder account's last check-in, as kept, by its
// name now; asks nothing.
func QoderCheckins() []WorkBuddyCheckin {
	st := readCheckins(qoderCheckinPath())
	var out []WorkBuddyCheckin
	listed := map[string]bool{}
	for _, a := range qoderCheckinAccounts() {
		key := qoderCheckinKey(a)
		if r, ok := st[key]; ok && a.On && key != "" && !listed[key] {
			listed[key] = true
			r.User, r.By = a.User, "qoder"
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].User < out[j].User })
	return out
}

// HasQoder says whether a Qoder account of the plugin is signed in, on
// either site.
func HasQoder() bool { return len(qoderCheckinAccounts()) > 0 }

// qoderCards are the usage cards of accts, for WithCheckins.
func qoderCards(accts []qoderCheckinAcct) []checkinCard {
	var out []checkinCard
	for _, a := range accts {
		if key := qoderCheckinKey(a); a.On && key != "" {
			out = append(out, checkinCard{User: a.User, card: a.card, key: key})
		}
	}
	return out
}

// qoderCheckinAccounts are the Qoder plugin's accounts on both sites,
// moved ones and ones signed in on the plugin beside the built-in, each
// sent through the plugin's fetch, which signs it as the account.
func qoderCheckinAccounts() []qoderCheckinAcct {
	var out []qoderCheckinAcct
	for _, pp := range heldPlugins() {
		if _, ok := qoderCampaignURLs[pp.ID]; !ok || movingNow(pp.ID) || pluginChecksIn(pp) {
			continue
		}
		auths := plugin.Auths(pp.ID)
		card := PluginID(pp.ID)
		for _, l := range pluginLogins(pp) {
			key := l.acct.Key
			out = append(out, qoderCheckinAcct{User: l.User, On: l.On, site: pp.ID, uid: str(auths[key]["uid"]), card: card,
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
	}
	return out
}

// KeepQoderCheckedIn checks the Qoder accounts in each day while settings
// say to, as KeepTraeCheckedIn does Trae CN's.
func KeepQoderCheckedIn(ctx context.Context) {
	keepCheckedIn(ctx, "qoder", newQoderCheckiner(qoderCheckinAccounts, time.Now), func() bool { return settings.Load().QoderCheckin })
}

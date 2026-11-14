package provider

// PLUGIN-SERVED (see AGENTS.md): WorkBuddy ("workbuddy") and WorkBuddy AI
// ("workbuddy-ai") are retired built-in subscriptions served by their
// plugin, @magpie-community/opencode-workbuddy-auth. The check-in here
// presses 签到 for the plugin's accounts, sent through the plugin's fetch,
// when the plugin doesn't check in itself. A fix to the plugin's own paths
// belongs in github.com/magpie-community/plugins (packages/workbuddy), with
// the movers' min raised in internal/provider/migrate_workbuddy.go.

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
// refused token) is tried again after wbCheckinRetry, or at the until a
// plugin gave it (heldUntil), or, when it never
// reached WorkBuddy (a machine just woken, its network not back yet), a
// minute later, and twice as long each time it still doesn't.
//
// Only the Chinese build has the event: WorkBuddy AI ships with it off.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/lazyre"
	"github.com/yetone/magpie/internal/plugin"
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
	CheckinFailed     = "failed"     // no answer; tried again later, at its until if given
	// CheckinCaptcha is a plugin's check-in the vendor asked a captcha
	// of: the user checks in in its own app, magpie never solves one
	CheckinCaptcha = "captcha"
	// CheckinOwnApp is a check-in the user is to do in the vendor's own
	// app: an answer for the day. WorkBuddy's check-in answered 401 while
	// the sign-in works; Trae CN refused the claim with 9074, which says
	// so in its own words (#1920), not that only its app is paid
	CheckinOwnApp = "own-app"
)

// wbCheckinSoon is how long a check-in that never reached WorkBuddy waits
// before it is tried again, doubling each time to wbCheckinRetry.
const wbCheckinSoon = time.Minute

// beijing is the day the event counts in, UTC+8 all year; fixed, so it
// needs no time zone database (Windows has none Go can read without one).
var beijing = time.FixedZone("CST", 8*60*60)

// CheckinDay is the day (Beijing, "2006-01-02") WorkBuddy's check-in
// counts t in: a WorkBuddyCheckin of that Day is today's.
func CheckinDay(t time.Time) string { return wbCheckinDay(t) }

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
	// ValidUntil is a campaign's end, or when a plugin said its vendor's
	// day turns (auth.checkin's until), when the answer must be checked
	// again, even if the Beijing day hasn't changed; on a failure, the
	// time before which it isn't asked again (heldUntil).
	ValidUntil time.Time `json:"until,omitzero"`
	// Offline is a failure that never reached WorkBuddy, and Tries how
	// many before it the same day did the same.
	Offline bool `json:"offline,omitempty"`
	Tries   int  `json:"tries,omitempty"`
	// By is the vendor whose check-in it is: "" WorkBuddy's, "trae"
	// Trae CN's; never kept.
	By string `json:"by,omitempty"`
	// Vendor is a plugin's provider by name, for a check-in By
	// "plugin:<id>"; never kept.
	Vendor string `json:"vendor,omitempty"`
	// Asked is true of one checked in on this run, not one read back;
	// sent so a press can tell a claim now from one earlier today.
	// Never kept: the file is written before it is set.
	Asked bool `json:"asked,omitempty"`
	// Claimed is when magpie last saw the account's credits claimed,
	// carried over while later answers that day say done: a done after
	// magpie's own claim is the day's, one before it may be yesterday's
	// (#3326).
	Claimed time.Time `json:"claimed,omitzero"`
	// Again says the answer is provisional (provisional): magpie asks
	// the vendor again later that day. Worked out when shown, never
	// kept.
	Again bool `json:"again,omitempty"`
}

// checkinSureHour is the Beijing hour from which a done, inactive or
// ineligible answer is taken as the day's. Before it, the vendor's own
// day may not have turned: at 00:00:28 Beijing AutoClaw CN, WorkBuddy,
// CodeArts, Trae CN, MiniMax and Raccoon answered "already in" for
// accounts whose day's credits were still to claim, and a claim at 10:54
// paid them (#3326, connoryang331, 14 of 29 accounts). Days that turn at
// UTC midnight (08:00 Beijing) and Qoder CN's 10:00 campaigns are past by
// noon; a vendor counting a US day isn't covered.
const checkinSureHour = 12

// claimedOn says whether magpie saw the account's credits claimed on day.
func (r WorkBuddyCheckin) claimedOn(day string) bool {
	return r.Outcome == CheckinClaimed && r.Day == day || !r.Claimed.IsZero() && wbCheckinDay(r.Claimed) == day
}

// provisional says whether r is an answer that may describe the vendor's
// day before (#3326): a done, inactive or ineligible one given before
// checkinSureHour, on a day magpie saw no claim. The loop asks such an
// account again every wbCheckinEvery until a claim, or one of these
// answers from checkinSureHour on, settles the day: at most 24 more
// asks a day for an account that was in early, none once magpie claims.
func (r WorkBuddyCheckin) provisional() bool {
	return r.unclaimedAnswer() && r.At.In(beijing).Hour() < checkinSureHour
}

// unclaimedAnswer says whether r is the vendor's word on how its day
// stands (done, inactive or ineligible) on a day magpie saw no claim:
// one a press asks the vendor again for, as it may have changed.
func (r WorkBuddyCheckin) unclaimedAnswer() bool {
	switch r.Outcome {
	case CheckinDone, CheckinInactive, CheckinIneligible:
		return !r.claimedOn(r.Day)
	}
	return false
}

// shown is r as a reader is given it: Again worked out.
func (r WorkBuddyCheckin) shown() WorkBuddyCheckin {
	r.Again = r.provisional()
	return r
}

// Checked says whether the day's credits are in, claimed now or before.
func (r WorkBuddyCheckin) Checked() bool {
	return r.Outcome == CheckinClaimed || r.Outcome == CheckinDone
}

// settled says whether r answers for day: it is that day's, not past
// ValidUntil (a plugin's vendor whose day turns at another hour said when:
// Qoder's daily credits open at 10:00 UTC+8, sdytman on Discord), and not
// a failure due another try (at now; soon, at once).
func (r WorkBuddyCheckin) settled(day string, now time.Time, soon bool) bool {
	// a failure held to the plugin's until waits it out, the day's turn
	// too; a press still asks
	if h := r.heldUntil(); !soon && now.Before(h) {
		return true
	}
	if r.Day != day || !r.ValidUntil.IsZero() && !now.Before(r.ValidUntil) {
		return false
	}
	return r.Outcome != CheckinFailed || !soon && now.Sub(r.At) < r.retryAfter()
}

// retryAfter is how long a failure waits to be tried again: wbCheckinRetry,
// or one that never reached WorkBuddy wbCheckinSoon, doubled for each try
// before it that didn't either (#265: a check-in the moment the machine
// woke, "no such host", then half an hour of 签到失败).
func (r WorkBuddyCheckin) retryAfter() time.Duration {
	if !r.Offline {
		return wbCheckinRetry
	}
	return min(wbCheckinRetry, wbCheckinSoon<<min(r.Tries, 5))
}

// checkinHoldMax bounds how long a failure's until holds it (heldUntil),
// so a far-off or wrong until can't stop an account's check-ins for good.
const checkinHoldMax = 24 * time.Hour

// heldUntil is when a failure a plugin gave an until later than its own
// retry (ZCode's claim read rate limited, its Retry-After or the plugin's
// own backoff: plugins#503, plugins#793) is asked again: at the until, at
// most checkinHoldMax after the failure, not every wbCheckinRetry, which
// kept the limit on. Zero for any other answer: a failure with no until,
// or one sooner than its retry, which settled asks again at the until.
func (r WorkBuddyCheckin) heldUntil() time.Time {
	if r.Outcome != CheckinFailed || !r.ValidUntil.After(r.At.Add(r.retryAfter())) {
		return time.Time{}
	}
	if most := r.At.Add(checkinHoldMax); most.Before(r.ValidUntil) {
		return most
	}
	return r.ValidUntil
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

// readCheckins is what a check-in file at path keeps, by entry. A file not
// there is empty. One there that can't be read or doesn't parse into
// entries (cut short, zeroed, `[]`) is unknown, never empty: the error
// says so, and what is given is what last read from it in this process,
// so a card still shows it. A check-in neither asks nor writes on an
// error, or it would check an account in twice in a day and write over
// the entries it couldn't read.
func readCheckins(path string) (map[string]WorkBuddyCheckin, error) {
	st := map[string]WorkBuddyCheckin{}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		err = nil
	case err == nil:
		if err = json.Unmarshal(b, &st); err != nil || st == nil {
			if err == nil {
				err = errors.New("it holds null")
			}
			err = fmt.Errorf("%s doesn't parse (%v): magpie leaves it as it is and checks in no account of it until it does", path, err)
		}
	default:
		err = fmt.Errorf("%s can't be read (%v): magpie leaves it as it is and checks in no account of it until it can", path, err)
	}
	checkinsReadMu.Lock()
	defer checkinsReadMu.Unlock()
	if err != nil {
		// logged once while it stays the same, not on every card read
		if checkinsBad[path] != err.Error() {
			checkinsBad[path] = err.Error()
			log.Printf("check-in: %v", err)
		}
		return maps.Clone(checkinsRead[path]), err
	}
	delete(checkinsBad, path)
	checkinsRead[path] = maps.Clone(st)
	return st, nil
}

// checkinsRead is each check-in file's entries as last read, for
// readCheckins to give when the file can't be read now, and checkinsBad
// the error last logged for one that can't.
var (
	checkinsReadMu sync.Mutex
	checkinsRead   = map[string]map[string]WorkBuddyCheckin{}
	checkinsBad    = map[string]string{}
)

// wroteCheckins notes st as written to path: what last read from it.
func wroteCheckins(path string, st map[string]WorkBuddyCheckin) {
	checkinsReadMu.Lock()
	defer checkinsReadMu.Unlock()
	delete(checkinsBad, path)
	checkinsRead[path] = maps.Clone(st)
}

// shownCheckins is readCheckins for a reader that only shows the entries:
// on an error, what last read (logged by readCheckins); each with Again
// worked out.
func shownCheckins(path string) map[string]WorkBuddyCheckin {
	st, _ := readCheckins(path)
	if st == nil {
		st = map[string]WorkBuddyCheckin{}
	}
	for k, r := range st {
		st[k] = r.shown()
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
	return wbCheckiner{path: wbCheckinPath(), now: time.Now, accounts: wbCheckinAccounts}
}

// checkinNow is checkiner's, over the accounts c lists.
func (c wbCheckiner) checkinNow(ctx context.Context, soon bool) []WorkBuddyCheckin {
	return c.checkiner().checkinNow(ctx, soon)
}

func (c wbCheckiner) clock() time.Time { return c.now() }

func (c wbCheckiner) checkiner() checkiner {
	return checkiner{path: c.path, now: c.now, mu: &wbCheckinMu, label: "workbuddy", accounts: func() []checkinAcct {
		var out []checkinAcct
		for _, a := range c.accounts() {
			key := ""
			if a.creds.UID != "" {
				key = wbCheckinKey(a)
			}
			out = append(out, checkinAcct{User: a.User, On: a.On, key: key, provider: cmp.Or(a.card, a.site.id), do: func(ctx context.Context) WorkBuddyCheckin { return wbCheckin(ctx, a) }})
		}
		return out
	}}
}

// wbCheckin checks a in for the day as WorkBuddy's 签到 does: the event's
// status first, and a claim only while it runs and today's isn't in.
func wbCheckin(ctx context.Context, a wbAccount) WorkBuddyCheckin {
	var h map[string]string // the plugin's fetch signs the request
	do := a.via
	var st wbCheckinStatus
	if err := wbCallVia(ctx, do, http.MethodPost, a.site.api()+"/v2/billing/meter/checkin-activity-status", h, map[string]any{}, &st); err != nil {
		return wbCheckinUnauthorized(ctx, do, a, h, err)
	}
	switch {
	case !st.Active:
		return WorkBuddyCheckin{Outcome: CheckinInactive}
	case st.TodayCheckedIn:
		return WorkBuddyCheckin{Outcome: CheckinDone, Credit: float64(st.TodayCredit), Streak: int(st.StreakDays)}
	}
	var got wbCheckinClaim
	if err := wbCallVia(ctx, do, http.MethodPost, a.site.api()+"/v2/billing/meter/daily-checkin", h, map[string]any{}, &got); err != nil {
		r := wbCheckinUnauthorized(ctx, do, a, h, err)
		if r.Outcome == CheckinDone {
			r.Streak = int(st.StreakDays)
		}
		return r
	}
	return WorkBuddyCheckin{Outcome: CheckinClaimed, Credit: float64(got.Credit), Streak: int(got.StreakDays), Msg: got.Message}
}

// wbCheckinUnauthorized is wbCheckinRefused, told apart on a 401: WorkBuddy's
// gateway answers a token it refuses with a bare 401 (an HTML page, so the
// card said only "Unauthorized" and "magpie tries again later", every 30
// minutes, all day; モモコ on Discord, a Free account, signing in again
// didn't help). The account's credits are read with the same headers at
// once: when they read, the sign-in is good and it is the check-in alone
// that refuses magpie — WorkBuddy's own app sends its check-in, and only
// its check-in, with a device token from its security SDK (X-Device-Token,
// buildHeadersWithTuringToken in WorkBuddy 5.5.6), which magpie can't make
// — so it is an answer for the day, to check in in the WorkBuddy app. When
// they don't read either, the sign-in itself is refused: sign in again.
func wbCheckinUnauthorized(ctx context.Context, do func(*http.Request) (*http.Response, error), a wbAccount, h map[string]string, err error) WorkBuddyCheckin {
	if !wbIsUnauthorized(err) {
		return wbCheckinRefused(err)
	}
	if wbCallVia(ctx, do, http.MethodPost, a.site.api()+"/billing/meter/get-user-resource-summary", h, map[string]any{}, nil) == nil {
		return WorkBuddyCheckin{Outcome: CheckinOwnApp, Msg: "WorkBuddy answered the check-in 401 Unauthorized while this account's credits read fine: its check-in takes WorkBuddy's own app"}
	}
	return WorkBuddyCheckin{Outcome: CheckinFailed, Msg: "WorkBuddy refused this account's sign-in (401 Unauthorized); sign in to it again"}
}

// wbIsUnauthorized says whether err is WorkBuddy's 401, bare or with a
// message.
func wbIsUnauthorized(err error) bool {
	var se *accountStatusError
	if errors.As(err, &se) {
		return se.status == http.StatusUnauthorized
	}
	var we *wbError
	return errors.As(err, &we) && we.code == http.StatusUnauthorized
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
	return wbCheckinFailed(err)
}

// wbCheckinFailed is a check-in err kept from an answer: offline when the
// request got no reply at all (no name lookup, no connection, no answer
// in time), as a *url.Error from the client says, or, for a check-in sent
// through a plugin's fetch, as Bun's words for it say: that comes back as
// text, and a moved WorkBuddy account's "ECONNRESET: The socket connection
// was closed unexpectedly" waited half an hour (plugins #149).
func wbCheckinFailed(err error) WorkBuddyCheckin {
	var ne net.Error
	return WorkBuddyCheckin{Outcome: CheckinFailed, Msg: err.Error(), Offline: errors.As(err, &ne) || unanswered.MatchString(err.Error())}
}

// unanswered is Bun's and Node's words for a fetch that got no reply: no
// name lookup, no connection, or one closed before the answer.
var unanswered = lazyre.New(`\b(ECONNRESET|ECONNREFUSED|ETIMEDOUT|ENOTFOUND|EAI_AGAIN|EHOSTUNREACH|ENETUNREACH|ConnectionRefused|ConnectionClosed|FailedToOpenSocket)\b|Unable to connect|socket connection was closed unexpectedly`)

// CheckInWorkBuddy checks each WorkBuddy (China) account in use in for
// today now, those not in yet, and says how each stands.
func CheckInWorkBuddy(ctx context.Context) []WorkBuddyCheckin {
	return newWBCheckiner().checkinNow(ctx, true)
}

// WorkBuddyCheckins is each WorkBuddy (China) account's last check-in, as
// kept, by its name now; asks nothing.
func WorkBuddyCheckins() []WorkBuddyCheckin {
	st := shownCheckins(wbCheckinPath())
	var out []WorkBuddyCheckin
	listed := map[string]bool{}
	for _, a := range wbCheckinAccounts() {
		key := wbCheckinKey(a)
		if r, ok := st[key]; ok && a.On && !listed[key] {
			listed[key] = true
			r.User = a.User
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].User < out[j].User })
	return out
}

// WithCheckins is qs with each WorkBuddy (China) account's card told it
// is checked in each day and how its last check-in went, for the Usage
// page to say on the card (#694: having to look in Settings to know whether
// it checked in is wrong). It holds for a moved account too: magpie checks
// those in through the plugin's fetch. qs, which may be the usage cache,
// is left as it is; nothing is asked.
func WithCheckins(qs []SubscriptionQuota) []SubscriptionQuota {
	qs = withCheckins(qs, wbCheckinAccounts(), shownCheckins(wbCheckinPath()))
	qs = markCheckins(qs, traeCards(traeCheckinAccounts()), shownCheckins(traeCheckinPath()), "trae")
	qs = markCheckins(qs, miniMaxCards(miniMaxCheckinAccounts()), shownCheckins(miniMaxCheckinPath()), "minimax")
	qs = markCheckins(qs, qoderCards(qoderCheckinAccounts()), shownCheckins(qoderCheckinPath()), "qoder")
	return pluginCheckinMarks(qs)
}

// LoginCheckins is m, agent's accounts' readings by user (LoginUsage),
// with each account's daily check-in as its Usage card has it
// (WithCheckins), for the provider editor's account rows (xwoffice_91592).
func LoginCheckins(agent string, m map[string]SubscriptionQuota) map[string]SubscriptionQuota {
	if len(m) == 0 {
		return m
	}
	card := loginProvider(Login{Agent: agent})
	if pp, ok := pluginOfAgent(agent); ok {
		card = PluginID(pp.ID)
	}
	users := make([]string, 0, len(m))
	qs := make([]SubscriptionQuota, 0, len(m))
	for u := range m {
		users = append(users, u)
		qs = append(qs, SubscriptionQuota{Provider: card, User: u})
	}
	qs = WithCheckins(qs)
	out := make(map[string]SubscriptionQuota, len(m))
	for i, u := range users {
		q, c := m[u], qs[i]
		q.Checkins, q.CheckinBy, q.Checkin, q.CheckinOff, q.CheckinKey = c.Checkins, c.CheckinBy, c.Checkin, c.CheckinOff, c.CheckinKey
		out[u] = q
	}
	return out
}

func withCheckins(qs []SubscriptionQuota, accts []wbAccount, st map[string]WorkBuddyCheckin) []SubscriptionQuota {
	var on []checkinCard
	for _, a := range accts {
		if a.On && a.creds.UID != "" {
			// the card: the site's, or the plugin's under its own id
			card := a.card
			if card == "" {
				card = a.site.id
			}
			on = append(on, checkinCard{User: a.User, card: card, key: wbCheckinKey(a)})
		}
	}
	return markCheckins(qs, on, st, "")
}

// checkinCard is an account checked in each day, by the usage card it is
// shown on and its entry in the vendor's file.
type checkinCard struct{ User, card, key string }

// markCheckins is qs with each card of an account in on told it is
// checked in each day, by the vendor by ("" WorkBuddy), and how its last
// check-in went, from st.
func markCheckins(qs []SubscriptionQuota, on []checkinCard, st map[string]WorkBuddyCheckin, by string) []SubscriptionQuota {
	if len(on) == 0 {
		return qs
	}
	out := slices.Clone(qs)
	off := checkinOff()
	for i, q := range out {
		var mine []checkinCard
		for _, a := range on {
			if a.card == q.Provider {
				mine = append(mine, a)
			}
		}
		var a *checkinCard
		for j := range mine {
			// a card without an account's name is the only account's
			if strings.EqualFold(mine[j].User, q.User) || q.User == "" && len(mine) == 1 {
				a = &mine[j]
				break
			}
		}
		if a == nil {
			continue
		}
		out[i].Checkins, out[i].CheckinBy, out[i].CheckinOff, out[i].CheckinKey = true, by, off[a.key], a.key
		if r, ok := st[a.key]; ok {
			r.User, r.By = a.User, by
			out[i].Checkin = &r
		}
	}
	return out
}

// HasWorkBuddy says whether a WorkBuddy (China) account is signed in.
func HasWorkBuddy() bool { return len(wbCheckinAccounts()) > 0 }

// wbCheckinAccounts are the WorkBuddy (China) accounts checked in: the
// plugin's, checked in through it, unless the plugin checks them in
// itself.
func wbCheckinAccounts() []wbAccount {
	pp, ok := PluginOf(wbCN.id)
	if !ok || pluginChecksIn(pp) {
		return nil
	}
	return wbPluginAccounts(pp)
}

// wbPluginAccounts are the WorkBuddy plugin's accounts of pp, each sent
// through the plugin's fetch, which signs it, and told by the card its
// usage is on.
func wbPluginAccounts(pp plugin.Provider) []wbAccount {
	auths := plugin.Auths(pp.ID)
	card := PluginID(pp.ID)
	var out []wbAccount
	for _, l := range pluginLogins(pp) {
		key := l.acct.Key
		out = append(out, wbAccount{Login: l.Login, site: wbCN, card: card, creds: wbCreds{UID: str(auths[key]["uid"])},
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

// KeepWorkBuddyCheckedIn checks the WorkBuddy accounts in each day while
// settings say to: two minutes after it starts, every wbCheckinEvery after
// that, and as the Beijing day turns, until ctx ends.
func KeepWorkBuddyCheckedIn(ctx context.Context) {
	keepCheckedIn(ctx, "workbuddy", newWBCheckiner(), func() bool { return settings.Shared().WorkBuddyCheckin })
}

// keepCheckedIn runs c while on says to. The clock is looked at every
// minute: a machine that slept through the day's turn notices on waking.
func keepCheckedIn(ctx context.Context, label string, c checkinRunner, on func() bool) {
	t := time.NewTimer(2 * time.Minute)
	defer t.Stop()
	var l wbCheckinLoop
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if on() {
			for _, r := range l.tick(ctx, c) {
				switch {
				case !r.Asked:
				case r.Outcome == CheckinClaimed && r.Streak == 0:
					log.Printf("%s check-in: %s +%g", label, r.User, r.Credit)
				case r.Outcome == CheckinClaimed:
					log.Printf("%s check-in: %s +%g, a %d-day streak", label, r.User, r.Credit, r.Streak)
				case r.Outcome == CheckinFailed:
					log.Printf("%s check-in: %s: %s", label, r.User, r.Msg)
				default:
					log.Printf("%s check-in: %s: %s", label, r.User, r.Outcome)
				}
			}
		}
		t.Reset(time.Minute)
	}
}

// wbCheckinLoop is when keepCheckedIn last looked at the accounts, and
// when a failure is due to be tried again, if before its next look.
type wbCheckinLoop struct{ last, again time.Time }

// tick looks at the accounts if it is time to: the first tick,
// wbCheckinEvery after the last look, as the Beijing day turns, or as a
// failure comes due again. It says how they stand, or nil.
func (l *wbCheckinLoop) tick(ctx context.Context, c checkinRunner) []WorkBuddyCheckin {
	// the wall clock, which goes on while the machine sleeps
	now := c.clock().Round(0)
	if !l.last.IsZero() && now.Sub(l.last) < wbCheckinEvery && wbCheckinDay(now) == wbCheckinDay(l.last) &&
		(l.again.IsZero() || now.Before(l.again)) {
		return nil
	}
	l.last, l.again = now, time.Time{}
	cx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	rs := c.checkinNow(cx, false)
	// and at the user's time of day, when the day's first ask waits for it
	if g := checkinGate(now); g.After(now) && (l.again.IsZero() || g.Before(l.again)) {
		l.again = g
	}
	for _, r := range rs {
		due := time.Time{}
		if r.Outcome == CheckinFailed {
			due = cmp.Or(r.heldUntil(), r.At.Add(r.retryAfter()))
		}
		// and as the vendor's own day turns, when it said (ValidUntil)
		if u := r.ValidUntil; u.After(now) && (due.IsZero() || u.Before(due)) {
			due = u
		}
		if !due.IsZero() && (l.again.IsZero() || due.Before(l.again)) {
			l.again = due
		}
	}
	return rs
}

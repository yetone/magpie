package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeWBCheckin is WorkBuddy's check-in API as one account sees it: the
// event's status, and a claim answered with claimCode (0 claims).
type fakeWBCheckin struct {
	mu        sync.Mutex
	active    bool
	checked   bool
	claimCode int
	down      bool // the network: every request fails
	broken    bool // every request is answered with a 500
	statuses  int
	claims    int
}

func (f *fakeWBCheckin) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.down {
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			c.Close()
			return
		}
		if f.broken {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Header.Get("Authorization") != "Bearer wb-access" || r.Header.Get("X-User-Id") != "u1" ||
			!strings.HasPrefix(r.Header.Get("User-Agent"), "WorkBuddy/") {
			json.NewEncoder(w).Encode(map[string]any{"code": 10085, "msg": "请求不合法"})
			return
		}
		ok := func(data any) { json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok", "data": data}) }
		switch r.URL.Path {
		case "/v2/billing/meter/checkin-activity-status":
			f.statuses++
			ok(map[string]any{"active": f.active, "today_checked_in": f.checked, "streak_days": 3,
				"daily_credit": 100, "today_credit": map[bool]int{true: 100}[f.checked], "total_credits": 300})
		case "/v2/billing/meter/daily-checkin":
			f.claims++
			if f.claimCode != 0 {
				json.NewEncoder(w).Encode(map[string]any{"code": f.claimCode, "msg": "今日已签到"})
				return
			}
			f.checked = true
			ok(map[string]any{"credit": 100, "streak_days": 4, "is_streak_day": true})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeWBCheckin) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statuses, f.claims
}

// wbCheckinFixture is a checkiner for one WorkBuddy account at srv, its
// clock at *now, its file in a temp folder.
func wbCheckinFixture(t *testing.T, srv *httptest.Server, now *time.Time) wbCheckiner {
	t.Helper()
	wbTokens.Lock()
	wbTokens.m = map[string]wbCreds{}
	wbTokens.Unlock()
	endpoint := srv.URL
	site := &wbSite{id: "workbuddy", name: "WorkBuddy", endpoint: &endpoint}
	future := time.Now().Add(24 * time.Hour).UnixMilli()
	a := wbAccount{Login: Login{User: "旅行者", On: true, Active: true}, site: site, own: true,
		creds: wbCreds{UID: "u1", Access: "wb-access", ExpiresAt: future, Domain: "www.codebuddy.cn"}}
	return wbCheckiner{
		path:     filepath.Join(t.TempDir(), "workbuddy-checkin.json"),
		now:      func() time.Time { return *now },
		accounts: func() []wbAccount { return []wbAccount{a} },
	}
}

// An account whose check-in is open is checked in once a Beijing day: the
// claim is kept, so a second look that day asks nothing, and the next day
// it is claimed again.
func TestWorkBuddyCheckinOncePerDay(t *testing.T) {
	f := &fakeWBCheckin{active: true}
	srv := f.serve(t)
	// 23:50 on 1 March in Beijing is 15:50 UTC
	now := time.Date(2026, 3, 1, 15, 50, 0, 0, time.UTC)
	c := wbCheckinFixture(t, srv, &now)
	ctx := context.Background()

	rs := c.checkinNow(ctx, false)
	if len(rs) != 1 || rs[0].Outcome != CheckinClaimed || !rs[0].Checked() || rs[0].Credit != 100 || rs[0].Streak != 4 ||
		rs[0].Day != "2026-03-01" || rs[0].User != "旅行者" || !rs[0].Asked {
		t.Fatalf("first: %+v", rs)
	}
	if s, cl := f.counts(); s != 1 || cl != 1 {
		t.Fatalf("asked status %d times, claimed %d", s, cl)
	}
	// kept by site and id, with no name and no token
	b, err := os.ReadFile(c.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"workbuddy|u1"`) || strings.Contains(string(b), "旅行者") || strings.Contains(string(b), "wb-access") {
		t.Fatalf("kept: %s", b)
	}

	// later that day: nothing asked, the claim read back
	now = now.Add(5 * time.Minute)
	rs = c.checkinNow(ctx, false)
	if len(rs) != 1 || rs[0].Outcome != CheckinClaimed || rs[0].Asked {
		t.Fatalf("again: %+v", rs)
	}
	if s, cl := f.counts(); s != 1 || cl != 1 {
		t.Fatalf("asked again the same day: status %d, claims %d", s, cl)
	}

	// past midnight in Beijing (16:05 UTC): a new day, claimed again
	now = now.Add(10 * time.Minute)
	f.mu.Lock()
	f.checked = false
	f.mu.Unlock()
	rs = c.checkinNow(ctx, false)
	if len(rs) != 1 || rs[0].Outcome != CheckinClaimed || rs[0].Day != "2026-03-02" || !rs[0].Asked {
		t.Fatalf("next day: %+v", rs)
	}
	if s, cl := f.counts(); s != 2 || cl != 2 {
		t.Fatalf("next day: status %d, claims %d", s, cl)
	}
}

// Checked in already (in WorkBuddy), or the event not running: nothing is
// claimed, and that holds for the day.
func TestWorkBuddyCheckinNothingToClaim(t *testing.T) {
	for _, tc := range []struct {
		name    string
		f       *fakeWBCheckin
		outcome string
	}{
		{"checked in", &fakeWBCheckin{active: true, checked: true}, CheckinDone},
		{"not active", &fakeWBCheckin{active: false}, CheckinInactive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := tc.f.serve(t)
			now := time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC)
			c := wbCheckinFixture(t, srv, &now)
			rs := c.checkinNow(context.Background(), false)
			if len(rs) != 1 || rs[0].Outcome != tc.outcome {
				t.Fatalf("%+v", rs)
			}
			if tc.outcome == CheckinDone && (!rs[0].Checked() || rs[0].Credit != 100 || rs[0].Streak != 3) {
				t.Fatalf("already in: %+v", rs[0])
			}
			now = now.Add(time.Hour)
			c.checkinNow(context.Background(), false)
			if s, cl := tc.f.counts(); s != 1 || cl != 0 {
				t.Fatalf("status %d, claims %d", s, cl)
			}
		})
	}
}

// A claim refused as already made (1001) or not eligible (1002) is that
// day's answer, not asked again.
func TestWorkBuddyCheckinRefused(t *testing.T) {
	for code, outcome := range map[int]string{wbCheckinAlready: CheckinDone, wbCheckinIneligible: CheckinIneligible, wbCheckinEnded: CheckinInactive} {
		f := &fakeWBCheckin{active: true, claimCode: code}
		srv := f.serve(t)
		now := time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC)
		c := wbCheckinFixture(t, srv, &now)
		rs := c.checkinNow(context.Background(), false)
		if len(rs) != 1 || rs[0].Outcome != outcome {
			t.Fatalf("%d: %+v", code, rs)
		}
		now = now.Add(2 * time.Hour)
		c.checkinNow(context.Background(), false)
		if s, cl := f.counts(); s != 1 || cl != 1 {
			t.Fatalf("%d asked again: status %d, claims %d", code, s, cl)
		}
	}
}

// An answer that isn't one of the event's (a 500, a refused token) is
// tried again later that day, not before wbCheckinRetry.
func TestWorkBuddyCheckinRetriesAfterFailure(t *testing.T) {
	f := &fakeWBCheckin{active: true, broken: true}
	srv := f.serve(t)
	now := time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC)
	c := wbCheckinFixture(t, srv, &now)
	if rs := c.checkinNow(context.Background(), false); len(rs) != 1 || rs[0].Outcome != CheckinFailed || rs[0].Checked() || rs[0].Offline {
		t.Fatalf("broken: %+v", rs)
	}
	f.mu.Lock()
	f.broken = false
	f.mu.Unlock()
	now = now.Add(10 * time.Minute)
	if rs := c.checkinNow(context.Background(), false); rs[0].Outcome != CheckinFailed || rs[0].Asked {
		t.Fatalf("tried again too soon: %+v", rs)
	}
	now = now.Add(wbCheckinRetry)
	if rs := c.checkinNow(context.Background(), false); rs[0].Outcome != CheckinClaimed || !rs[0].Asked {
		t.Fatalf("not tried again: %+v", rs)
	}
	if _, cl := f.counts(); cl != 1 {
		t.Fatalf("claims %d", cl)
	}
}

// A check-in that never reached WorkBuddy — the machine just woke and its
// network isn't back, "no such host" (#265) — is tried again a minute
// later, not half an hour, by the loop as well; while it still doesn't,
// the wait doubles up to wbCheckinRetry.
func TestWorkBuddyCheckinOfflineRetriesSoon(t *testing.T) {
	f := &fakeWBCheckin{active: true, down: true}
	srv := f.serve(t)
	now := time.Date(2026, 3, 1, 0, 37, 52, 0, time.UTC)
	c := wbCheckinFixture(t, srv, &now)
	ctx := context.Background()
	var l wbCheckinLoop
	// the loop's first look, the moment the machine wakes
	if rs := l.tick(ctx, c); len(rs) != 1 || rs[0].Outcome != CheckinFailed || !rs[0].Offline || !rs[0].Asked {
		t.Fatalf("woken: %+v", rs)
	}
	// a minute on, the loop's minute tick, the network back
	f.mu.Lock()
	f.down = false
	f.mu.Unlock()
	now = now.Add(time.Minute)
	if rs := l.tick(ctx, c); len(rs) != 1 || rs[0].Outcome != CheckinClaimed || !rs[0].Asked {
		t.Fatalf("a minute on: %+v", rs)
	}
	if _, cl := f.counts(); cl != 1 {
		t.Fatalf("claims %d", cl)
	}

	// down all along: tried at 0, 1, 3, 7, 15, 31, then every 30 minutes
	f = &fakeWBCheckin{active: true, down: true}
	srv = f.serve(t)
	now = time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC)
	c = wbCheckinFixture(t, srv, &now)
	l = wbCheckinLoop{}
	var tried []int
	for m := 0; m <= 91; m++ {
		for _, r := range l.tick(ctx, c) {
			if r.Asked {
				tried = append(tried, m)
			}
		}
		now = now.Add(time.Minute)
	}
	if want := []int{0, 1, 3, 7, 15, 31, 61, 91}; !slices.Equal(tried, want) {
		t.Fatalf("tried at %v, want %v", tried, want)
	}
}

// The accounts are WorkBuddy's own sign-in and those magpie added, read as
// the rest of magpie reads them; what is kept is shown by name.
func TestWorkBuddyCheckinSignedIn(t *testing.T) {
	home := signIn(t)
	wbTokens.Lock()
	wbTokens.m = map[string]wbCreds{}
	wbTokens.Unlock()
	future := time.Now().Add(24 * time.Hour).UnixMilli()
	writeFile(t, wbAuthFile(home), map[string]any{
		"account": map[string]any{"uid": "u1", "nickname": "旅行者"},
		"auth":    map[string]any{"accessToken": "wb-access", "refreshToken": "r", "expiresAt": future, "domain": "www.codebuddy.cn"},
	})
	f := &fakeWBCheckin{active: true}
	srv := f.serve(t)
	old := wbEndpoint
	wbEndpoint = srv.URL
	defer func() { wbEndpoint = old }()

	if !HasWorkBuddy() || len(WorkBuddyCheckins()) != 0 {
		t.Fatalf("before: %v %+v", HasWorkBuddy(), WorkBuddyCheckins())
	}
	rs := CheckInWorkBuddy(context.Background())
	if len(rs) != 1 || rs[0].Outcome != CheckinClaimed || rs[0].User != "旅行者" {
		t.Fatalf("checked in: %+v", rs)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "magpie", "workbuddy-checkin.json")); err != nil {
		t.Fatal(err)
	}
	if got := WorkBuddyCheckins(); len(got) != 1 || got[0].User != "旅行者" || got[0].Credit != 100 {
		t.Fatalf("kept: %+v", got)
	}
}

// An account on the plugin is checked in through the plugin's own fetch,
// which signs it: magpie holds no token of it to renew or send.
func TestWorkBuddyCheckinThroughPlugin(t *testing.T) {
	var paths []string
	via := func(req *http.Request) (*http.Response, error) {
		paths = append(paths, req.URL.Path)
		if req.Header.Get("Authorization") != "" {
			t.Errorf("magpie signed %s itself", req.URL.Path)
		}
		body := `{"code":0,"data":{"active":true,"today_checked_in":false,"streak_days":2}}`
		if strings.HasSuffix(req.URL.Path, "/daily-checkin") {
			body = `{"code":0,"data":{"credit":50,"streak_days":3}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	}
	a := wbAccount{Login: Login{User: "me", On: true}, site: wbCN, creds: wbCreds{UID: "u1"}, via: via}
	r := wbCheckin(context.Background(), a)
	if r.Outcome != CheckinClaimed || r.Credit != 50 || r.Streak != 3 {
		t.Fatalf("check-in through the plugin: %+v", r)
	}
	if len(paths) != 2 || !strings.HasSuffix(paths[0], "/checkin-activity-status") || !strings.HasSuffix(paths[1], "/daily-checkin") {
		t.Fatalf("asked %v", paths)
	}
}

// The Usage page's WorkBuddy card says how each account's check-in went
// (#694): matched by its name, or the only account's on a card without
// one; another vendor's card, and an account not checked in, untouched,
// and the cards given (the usage cache) left as they were.
func TestWorkBuddyCheckinOnTheCard(t *testing.T) {
	accts := []wbAccount{
		{Login: Login{User: "Ann", On: true}, site: wbCN, creds: wbCreds{UID: "u1"}},
		{Login: Login{User: "Bob", On: true}, site: wbCN, creds: wbCreds{UID: "u2"}},
		{Login: Login{User: "Off", On: false}, site: wbCN, creds: wbCreds{UID: "u3"}},
	}
	st := map[string]WorkBuddyCheckin{
		"workbuddy|u1": {Day: "2026-10-03", Outcome: CheckinClaimed, Credit: 50, Streak: 3},
		"workbuddy|u3": {Day: "2026-10-03", Outcome: CheckinDone},
	}
	qs := []SubscriptionQuota{{Provider: "workbuddy", User: "ann"}, {Provider: "workbuddy", User: "Bob"}, {Provider: "workbuddy", User: "Off"}, {Provider: "codex", User: "Ann"}}
	got := withCheckins(qs, accts, st)
	if !got[0].Checkins || got[0].Checkin == nil || got[0].Checkin.Credit != 50 || got[0].Checkin.User != "Ann" {
		t.Fatalf("Ann: %+v", got[0])
	}
	if !got[1].Checkins || got[1].Checkin != nil {
		t.Fatalf("Bob, not checked in yet: %+v", got[1])
	}
	if got[2].Checkins || got[2].Checkin != nil || got[3].Checkins || got[3].Checkin != nil {
		t.Fatalf("an account off, or another vendor's: %+v %+v", got[2], got[3])
	}
	if qs[0].Checkins || qs[0].Checkin != nil {
		t.Fatal("the cards given were changed")
	}
	one := withCheckins([]SubscriptionQuota{{Provider: "workbuddy"}}, accts[:1], st)
	if !one[0].Checkins || one[0].Checkin == nil {
		t.Fatalf("one account, its card unnamed: %+v", one[0])
	}
	if two := withCheckins([]SubscriptionQuota{{Provider: "workbuddy"}}, accts[:2], st); two[0].Checkins {
		t.Fatalf("an unnamed card of two accounts: %+v", two[0])
	}
}

// The WorkBuddy plugin signed in under its own id, not moved (signed in
// before the move was, or moved back), has its accounts checked in too and
// its cards ("workbuddy-plugin") say so, beside the built-in's; one account
// signed in to both is checked in once (#694, Dazzle-sys on 0.1.774: two
// accounts on the card, credits by day, no check-in).
func TestWorkBuddyCheckinPluginUnderItsOwnID(t *testing.T) {
	movedPlugin(t, "workbuddy", map[string]map[string]any{
		"workbuddy":     {"type": "oauth", "access": "a", "refresh": "r", "expires": 0, "accountId": "Ann", "uid": "u1"},
		"workbuddy#abc": {"type": "oauth", "access": "b", "refresh": "r", "expires": 0, "accountId": "Bob", "uid": "u2"},
	})
	_ = setMigration("workbuddy", func(m *Migration) { m.State = MovedBack })
	loginsMu.Lock()
	_ = writeLogins([]savedLogin{{Agent: "workbuddy", User: "Cat", On: true, First: true, Auth: []byte(`{"uid":"u3","accessToken":"c"}`)}})
	loginsMu.Unlock()
	if Moved("workbuddy") || PluginID("workbuddy") != "workbuddy-plugin" {
		t.Fatalf("set up as moved: %q", PluginID("workbuddy"))
	}
	var users []string
	for _, a := range wbCheckinAccounts() {
		users = append(users, a.User)
	}
	if strings.Join(users, ",") != "Cat,Ann,Bob" {
		t.Fatalf("accounts checked in: %v", users)
	}
	if !HasWorkBuddy() {
		t.Fatal("Settings has no check-in")
	}
	st := map[string]WorkBuddyCheckin{"workbuddy|u2": {Day: "2026-10-04", Outcome: CheckinClaimed, Credit: 100, Streak: 5}}
	qs := []SubscriptionQuota{
		{Provider: "workbuddy", User: "Cat"},
		{Provider: "workbuddy-plugin", User: "Ann"},
		{Provider: "workbuddy-plugin", User: "Bob"},
		{Provider: "workbuddy-ai-plugin", User: "Ann"},
		{Provider: "workbuddy-plugin", User: "Cat"},
	}
	got := withCheckins(qs, wbCheckinAccounts(), st)
	for i, want := range []bool{true, true, true, false, false} {
		if got[i].Checkins != want {
			t.Fatalf("card %d %s %s: checkins %v, want %v", i, got[i].Provider, got[i].User, got[i].Checkins, want)
		}
	}
	if got[2].Checkin == nil || got[2].Checkin.Credit != 100 || got[1].Checkin != nil {
		t.Fatalf("Bob's check-in: %+v, Ann's %+v", got[2].Checkin, got[1].Checkin)
	}

	// the same account on the built-in and the plugin: asked once
	var asked int
	via := func(req *http.Request) (*http.Response, error) {
		asked++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":0,"data":{"active":true,"today_checked_in":true,"today_credit":100,"streak_days":2}}`)), Header: http.Header{}}, nil
	}
	now := time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC)
	c := wbCheckiner{path: filepath.Join(t.TempDir(), "c.json"), now: func() time.Time { return now }, accounts: func() []wbAccount {
		return []wbAccount{
			{Login: Login{User: "Ann", On: true}, site: wbCN, creds: wbCreds{UID: "u1"}, via: via},
			{Login: Login{User: "Ann", On: true}, site: wbCN, card: "workbuddy-plugin", creds: wbCreds{UID: "u1"}, via: via},
		}
	}}
	if rs := c.checkinNow(context.Background(), true); len(rs) != 1 || asked != 1 || rs[0].Outcome != CheckinDone {
		t.Fatalf("one account twice: %d asked, %+v", asked, rs)
	}
}

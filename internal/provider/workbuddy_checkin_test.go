package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// No answer (the network) is tried again later that day, not before
// wbCheckinRetry.
func TestWorkBuddyCheckinRetriesAfterFailure(t *testing.T) {
	f := &fakeWBCheckin{active: true, down: true}
	srv := f.serve(t)
	now := time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC)
	c := wbCheckinFixture(t, srv, &now)
	if rs := c.checkinNow(context.Background(), false); len(rs) != 1 || rs[0].Outcome != CheckinFailed || rs[0].Checked() {
		t.Fatalf("down: %+v", rs)
	}
	f.mu.Lock()
	f.down = false
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

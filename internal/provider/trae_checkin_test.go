package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTraeCheckin is Trae CN's check-in as one account sees it, behind
// the plugin's fetch: status, and a claim answered with claimCode (0
// claims).
type fakeTraeCheckin struct {
	mu        sync.Mutex
	enable    bool
	checked   bool
	claimCode int
	expired   bool // the plugin marks the sign-in expired (code 1001)
	old       bool // a plugin before 0.1.4: chat completions only
	paths     []string
}

func (f *fakeTraeCheckin) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.old {
			http.Error(w, "only chat completions are served", http.StatusBadRequest)
			return
		}
		f.paths = append(f.paths, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/trae/api/v2/ug/checkin_credits"))
		if f.expired {
			w.Header().Set("X-Magpie-Sign-In", "expired")
			json.NewEncoder(w).Encode(map[string]any{"code": 1001, "message": "We're sorry, but we are not able to authenticate you"})
			return
		}
		w.Header().Set("X-Magpie-Sign-In", "kept")
		switch r.URL.Path {
		case "/trae/api/v2/ug/checkin_credits/status":
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "message": "success", "enable": f.enable, "checked_in": f.checked, "credits": 100})
		case "/trae/api/v2/ug/checkin_credits/claim":
			if f.claimCode != 0 {
				json.NewEncoder(w).Encode(map[string]any{"code": f.claimCode, "message": "already checked in on this device"})
				return
			}
			f.checked = true
			json.NewEncoder(w).Encode(map[string]any{"code": 0, "credits": 100, "extra_credits": 0})
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	old := traeCheckinURL
	traeCheckinURL = srv.URL + "/trae/api/v2/ug/checkin_credits"
	t.Cleanup(func() { traeCheckinURL = old })
	return srv
}

func (f *fakeTraeCheckin) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.paths
	f.paths = nil
	return out
}

// traeCheckinFixture is a checkiner for one Trae CN account, its clock at
// *now, its file in a temp folder.
func traeCheckinFixture(t *testing.T, now *time.Time) checkiner {
	c := newTraeCheckiner(func() []traeAccount {
		return []traeAccount{{User: "hu", On: true, uid: "7300", card: TraeCNID, via: http.DefaultClient.Do}}
	})
	c.path = filepath.Join(t.TempDir(), "trae-checkin.json")
	c.now = func() time.Time { return *now }
	return c
}

// One check-in a Beijing day (#694): the status first, a claim while it
// is on and today's isn't in; asked again the same day, nothing is sent,
// and the next day finds it in already when it was pressed in Trae.
func TestTraeCheckinOncePerDay(t *testing.T) {
	f := &fakeTraeCheckin{enable: true}
	f.serve(t)
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, beijing)
	c := traeCheckinFixture(t, &now)
	ctx := context.Background()

	rs := c.checkinNow(ctx, false)
	if len(rs) != 1 || rs[0].Outcome != CheckinClaimed || rs[0].Credit != 100 || rs[0].User != "hu" || rs[0].By != "trae" || !rs[0].Asked {
		t.Fatalf("first: %+v", rs)
	}
	if got := strings.Join(f.asked(), ", "); got != "POST /status, POST /claim" {
		t.Fatalf("asked %s", got)
	}

	now = now.Add(3 * time.Hour)
	rs = c.checkinNow(ctx, true)
	if len(rs) != 1 || rs[0].Outcome != CheckinClaimed || rs[0].Asked {
		t.Fatalf("same day: %+v", rs)
	}
	if got := f.asked(); len(got) != 0 {
		t.Fatalf("same day asked %v", got)
	}

	now = now.Add(24 * time.Hour)
	rs = c.checkinNow(ctx, false)
	if rs[0].Outcome != CheckinDone || rs[0].Credit != 100 {
		t.Fatalf("next day, pressed in Trae: %+v", rs)
	}
	if got := strings.Join(f.asked(), ", "); got != "POST /status" {
		t.Fatalf("next day asked %s", got)
	}
}

// Trae's answers for the day hold for it; a failure is tried again.
func TestTraeCheckinAnswers(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, beijing)

	off := &fakeTraeCheckin{}
	off.serve(t)
	if rs := traeCheckinFixture(t, &now).checkinNow(ctx, false); rs[0].Outcome != CheckinInactive {
		t.Fatalf("not on: %+v", rs)
	}
	if got := off.asked(); len(got) != 1 {
		t.Fatalf("claimed while off: %v", got)
	}

	// 9095: this device is in for today, an answer; no second device id
	dev := &fakeTraeCheckin{enable: true, claimCode: traeDeviceChecked}
	dev.serve(t)
	c := traeCheckinFixture(t, &now)
	if rs := c.checkinNow(ctx, false); rs[0].Outcome != CheckinIneligible || rs[0].Msg == "" {
		t.Fatalf("9095: %+v", rs)
	}
	dev.asked()
	if rs := c.checkinNow(ctx, true); rs[0].Outcome != CheckinIneligible || len(dev.asked()) != 0 {
		t.Fatalf("9095 asked again: %+v", rs)
	}

	// 9074: too often, tried again
	busy := &fakeTraeCheckin{enable: true, claimCode: traeRateLimited}
	busy.serve(t)
	c = traeCheckinFixture(t, &now)
	if rs := c.checkinNow(ctx, false); rs[0].Outcome != CheckinFailed {
		t.Fatalf("9074: %+v", rs)
	}
	busy.asked()
	busy.claimCode = 0
	if rs := c.checkinNow(ctx, true); rs[0].Outcome != CheckinClaimed {
		t.Fatalf("9074, again: %+v", rs)
	}

	// a refused token, marked by the plugin
	exp := &fakeTraeCheckin{expired: true}
	exp.serve(t)
	rs := traeCheckinFixture(t, &now).checkinNow(ctx, false)
	if rs[0].Outcome != CheckinFailed || !strings.Contains(rs[0].Msg, "sign in again") {
		t.Fatalf("expired: %+v", rs)
	}
	if got := exp.asked(); len(got) != 1 {
		t.Fatalf("claimed when signed out: %v", got)
	}

	// a plugin too old to send Trae's own pages
	old := &fakeTraeCheckin{old: true}
	old.serve(t)
	rs = traeCheckinFixture(t, &now).checkinNow(ctx, false)
	if rs[0].Outcome != CheckinFailed || !strings.Contains(rs[0].Msg, "0.1.4") {
		t.Fatalf("old plugin: %+v", rs)
	}
}

// A Trae CN account's usage card is told its check-in, and whose it is,
// so the card's switch is Trae's; another plugin's card isn't.
func TestTraeCheckinOnTheCard(t *testing.T) {
	st := map[string]WorkBuddyCheckin{TraeCNID + "|7300": {Day: "2026-10-04", Outcome: CheckinClaimed, Credit: 100}}
	accts := []traeAccount{{User: "hu", On: true, uid: "7300", card: TraeCNID}, {User: "nouid", On: true, card: TraeCNID}}
	qs := []SubscriptionQuota{{Provider: TraeCNID, User: "hu"}, {Provider: TraeCNID, User: "nouid"}, {Provider: "kiro", User: "hu"}}
	out := markCheckins(qs, traeCards(accts), st, "trae")
	if !out[0].Checkins || out[0].CheckinBy != "trae" || out[0].Checkin == nil || out[0].Checkin.Credit != 100 || out[0].Checkin.By != "trae" {
		t.Fatalf("trae card: %+v", out[0])
	}
	if out[1].Checkins || out[2].Checkins {
		t.Fatalf("marked: %+v, %+v", out[1], out[2])
	}
	if qs[0].Checkins {
		t.Fatal("qs changed")
	}
}

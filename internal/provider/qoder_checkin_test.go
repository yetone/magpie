package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeQoderCampaigns is Qoder's campaigns as one account sees them, behind
// the plugin's fetch, answering as wallechfox/qoder-checkin reads them: the
// day's credits (CLAIM_BENEFIT) among other campaigns, and a claim whose
// answer is under data.
type fakeQoderCampaigns struct {
	mu      sync.Mutex
	status  string // the day's campaign's claimStatus; "" none listed
	claimAs string // what a claim answers; "" CLAIMED
	expired bool   // the plugin marks the sign-in expired
	now     func() time.Time
	asked   []string
	bad     []string
}

func (f *fakeQoderCampaigns) serve(t *testing.T, site string) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		f.asked = append(f.asked, r.Method+" "+strings.TrimPrefix(r.URL.Path, "/sash/api/v1/me/campaigns"))
		if f.expired {
			w.Header().Set("X-Magpie-Sign-In", "expired")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		now := f.now().Unix()
		switch r.Method + " " + r.URL.Path {
		case "GET /sash/api/v1/me/campaigns":
			if len(b) != 0 {
				f.bad = append(f.bad, "a GET with a body")
			}
			list := `{"campaignId":"invite","actionType":"INVITE","claimStatus":"CLAIMABLE","benefit":{"amount":500}}`
			if f.status != "" {
				list += fmt.Sprintf(`,{"campaignId":"daily-1","actionType":"CLAIM_BENEFIT","claimStatus":%q,"startAt":%d,"endAt":%d,"benefit":{"amount":100}}`, f.status, (now-3600)*1000, (now+3600)*1000)
			}
			fmt.Fprintf(w, `{"campaigns":[%s]}`, list)
		case "POST /sash/api/v1/me/campaigns/daily-1/claim":
			if string(b) != "{}" {
				f.bad = append(f.bad, "claim body "+string(b))
			}
			as := f.claimAs
			if as == "" {
				as, f.status = "CLAIMED", "CLAIMED"
			}
			fmt.Fprintf(w, `{"data":{"status":%q,"benefit":{"amount":100}}}`, as)
		default:
			f.bad = append(f.bad, "asked "+r.Method+" "+r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	old := qoderCampaignURLs[site]
	qoderCampaignURLs[site] = srv.URL + "/sash/api/v1/me/campaigns"
	t.Cleanup(func() { qoderCampaignURLs[site] = old })
}

func (f *fakeQoderCampaigns) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.asked
	f.asked = nil
	return out
}

func qoderCheckinFixture(t *testing.T, now *time.Time, accts ...qoderCheckinAcct) checkiner {
	c := newQoderCheckiner(func() []qoderCheckinAcct { return accts }, func() time.Time { return *now })
	c.path = filepath.Join(t.TempDir(), "qoder-checkin.json")
	return c
}

// Qoder's daily credits (ARNO on Discord): the campaigns are read, the
// day's claimed while the server says claimable; one claimed already
// is a check-in done, none listed is no event, and a refusal or an expired
// sign-in is a failure with its reason. Other campaigns are never claimed.
func TestQoderCheckin(t *testing.T) {
	if !strings.HasPrefix(qoderCampaignURLs["qoder"], "https://openapi.qoder.sh/") || !strings.HasPrefix(qoderCampaignURLs[QoderCNID], "https://openapi.qoder.com.cn/") {
		t.Fatalf("campaigns at %v", qoderCampaignURLs)
	}
	now := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	f := &fakeQoderCampaigns{status: "CLAIMABLE", now: func() time.Time { return now }}
	f.serve(t, "qoder")
	c := qoderCheckinFixture(t, &now, qoderCheckinAcct{User: "arno", On: true, site: "qoder", uid: "u1", card: "qoder", via: http.DefaultClient.Do})

	rs := c.checkinNow(t.Context(), false)
	if len(rs) != 1 || rs[0].Outcome != CheckinClaimed || rs[0].Credit != 100 || rs[0].By != "qoder" || !rs[0].Asked {
		t.Fatalf("claimable: %+v", rs)
	}
	if got := strings.Join(f.take(), ", "); got != "GET , POST /daily-1/claim" {
		t.Fatalf("asked %s", got)
	}
	// a recent answer is reused: nothing asked again immediately
	if rs := c.checkinNow(t.Context(), false); rs[0].Outcome != CheckinClaimed || rs[0].Asked || len(f.take()) != 0 {
		t.Fatalf("asked again immediately: %+v", rs)
	}

	// the next day, claimed already (in Qoder): no claim
	now = now.Add(24 * time.Hour)
	if rs := c.checkinNow(t.Context(), false); rs[0].Outcome != CheckinDone || rs[0].Credit != 100 {
		t.Fatalf("in already: %+v", rs)
	}
	if got := strings.Join(f.take(), ", "); got != "GET " {
		t.Fatalf("in already, asked %s", got)
	}

	// no daily campaign listed
	now = now.Add(24 * time.Hour)
	f.status = ""
	if rs := c.checkinNow(t.Context(), false); rs[0].Outcome != CheckinInactive {
		t.Fatalf("none listed: %+v", rs)
	}

	// listed, but neither claimable nor claimed for today: tried again
	now = now.Add(24 * time.Hour)
	f.status = "UPCOMING"
	if rs := c.checkinNow(t.Context(), false); rs[0].Outcome != CheckinFailed || !strings.Contains(rs[0].Msg, "aren't offered yet") {
		t.Fatalf("not offered: %+v", rs)
	}

	// a claim that doesn't come back CLAIMED
	now = now.Add(24 * time.Hour)
	f.status, f.claimAs = "CLAIMABLE", "FAILED"
	if rs := c.checkinNow(t.Context(), false); rs[0].Outcome != CheckinFailed || !strings.Contains(rs[0].Msg, "FAILED") {
		t.Fatalf("refused: %+v", rs)
	}

	// an expired sign-in says so
	now = now.Add(24 * time.Hour)
	f.claimAs, f.expired = "", true
	if rs := c.checkinNow(t.Context(), false); rs[0].Outcome != CheckinFailed || !strings.Contains(rs[0].Msg, "sign in again") {
		t.Fatalf("expired: %+v", rs)
	}
	if len(f.bad) != 0 {
		t.Fatalf("sent wrong: %v", f.bad)
	}
}

// Qoder CN's account is checked in on its own site and kept apart from a
// Qoder account of the same uid; a plugin before 0.2.7, which serves chat
// completions only, says to update it.
func TestQoderCNCheckin(t *testing.T) {
	now := time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)
	intl := &fakeQoderCampaigns{status: "CLAIMED", now: func() time.Time { return now }}
	intl.serve(t, "qoder")
	cn := &fakeQoderCampaigns{status: "CLAIMABLE", now: func() time.Time { return now }}
	cn.serve(t, QoderCNID)
	c := qoderCheckinFixture(t, &now,
		qoderCheckinAcct{User: "arno", On: true, site: QoderCNID, uid: "u1", card: QoderCNID, via: http.DefaultClient.Do},
		qoderCheckinAcct{User: "hu", On: true, site: "qoder", uid: "u1", card: "qoder", via: http.DefaultClient.Do},
	)
	got := map[string]string{}
	for _, r := range c.checkinNow(t.Context(), false) {
		got[r.User] = r.Outcome
	}
	if got["arno"] != CheckinClaimed || got["hu"] != CheckinDone {
		t.Fatalf("checked in: %v", got)
	}
	if a := strings.Join(cn.take(), ", "); a != "GET , POST /daily-1/claim" {
		t.Fatalf("Qoder CN was asked %s", a)
	}
	if a := strings.Join(intl.take(), ", "); a != "GET " {
		t.Fatalf("Qoder was asked %s", a)
	}

	old := qoderCheckinAcct{User: "arno", On: true, site: "qoder", uid: "u2", via: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"only chat completions are served"}}`))}, nil
	}}
	if r := qoderCheckin(t.Context(), old, time.Now()); r.Outcome != CheckinFailed || !strings.Contains(r.Msg, "0.2.7") {
		t.Fatalf("old plugin: %+v", r)
	}
}

// A Qoder account's usage card is told it is checked in, and how.
func TestQoderCheckinOnTheCard(t *testing.T) {
	accts := []qoderCheckinAcct{{User: "arno", On: true, site: "qoder", uid: "u1", card: "qoder"}}
	st := map[string]WorkBuddyCheckin{"qoder|u1": {Day: CheckinDay(time.Now()), Outcome: CheckinClaimed, Credit: 100}}
	qs := []SubscriptionQuota{{Provider: "qoder", User: "arno"}}
	out := markCheckins(qs, qoderCards(accts), st, "qoder")
	if !out[0].Checkins || out[0].CheckinBy != "qoder" || out[0].Checkin == nil || out[0].Checkin.Credit != 100 {
		t.Fatalf("card: %+v", out[0])
	}
}

// The real Qoder CN campaign read on 2026-10-07 starts at 10:00 Beijing
// and ends at 09:59 the next day.
const qoderDailyCampaignJSON = `{"campaignId":"01a0f1db-9cc0-725f-a2f4-ca596883291d","actionType":"CLAIM_BENEFIT","startAt":1791338400,"endAt":1791424740,"claimStatus":"CLAIMED","benefit":{"kind":"CREDITS","amount":100,"modelScope":{"modelSeries":{"key":"ALL_MODELS"}},"validity":{"mode":"RELATIVE_DAYS","days":30}}}`

// A campaign can still be claimed after Beijing midnight. Its result must
// expire before the next campaign, including one saved without an expiry.
func TestQoderCheckinCampaignRollover(t *testing.T) {
	for _, site := range []string{"qoder", QoderCNID} {
		for _, previous := range []string{"CLAIMED", "CLAIMABLE", "empty", "legacy-done", "legacy-claimed"} {
			t.Run(site+"/"+previous, func(t *testing.T) {
				var campaign qoderCampaign
				if err := json.Unmarshal([]byte(qoderDailyCampaignJSON), &campaign); err != nil {
					t.Fatal(err)
				}
				now := time.Date(2026, 10, 8, 9, 0, 0, 0, beijing)
				gets, posts := 0, 0
				status := previous
				a := qoderCheckinAcct{User: "arno", On: true, site: site, uid: "u1", card: PluginID(site)}
				a.via = func(req *http.Request) (*http.Response, error) {
					var body string
					if req.Method == http.MethodGet {
						gets++
						campaign.ClaimStatus = status
						list := []qoderCampaign{campaign}
						if status == "empty" {
							list = nil
						}
						b, err := json.Marshal(map[string]any{"campaigns": list})
						if err != nil {
							t.Fatal(err)
						}
						body = string(b)
					} else {
						posts++
						status = "CLAIMED"
						body = `{"data":{"status":"CLAIMED","benefit":{"amount":100}}}`
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
				}
				c := qoderCheckinFixture(t, &now, a)
				if strings.HasPrefix(previous, "legacy-") {
					outcome := strings.TrimPrefix(previous, "legacy-")
					b := fmt.Sprintf(`{%q:{"day":"2026-10-08","at":"2026-10-08T09:00:00+08:00","outcome":%q,"credit":100}}`, qoderCheckinKey(a), outcome)
					if err := writePrivate(c.path, []byte(b)); err != nil {
						t.Fatal(err)
					}
					status = "CLAIMED"
					now = now.Add(wbCheckinEvery - time.Nanosecond)
					rs := c.checkinNow(t.Context(), false)
					if len(rs) != 1 || rs[0].Outcome != outcome || rs[0].Asked || gets != 0 {
						t.Fatalf("old saved result wasn't reused: %+v, reads %d", rs, gets)
					}
					now = now.Add(time.Nanosecond)
					rs = c.checkinNow(t.Context(), false)
					if len(rs) != 1 || rs[0].Outcome != CheckinDone || !rs[0].Asked || gets != 1 || !rs[0].ValidUntil.Equal(time.Unix(int64(campaign.EndAt), 0)) {
						t.Fatalf("old saved result wasn't refreshed: %+v, reads %d", rs, gets)
					}
				} else {
					rs := c.checkinNow(t.Context(), false)
					want, claims := CheckinDone, 0
					if previous == "CLAIMABLE" {
						want, claims = CheckinClaimed, 1
					} else if previous == "empty" {
						want = CheckinInactive
					}
					if len(rs) != 1 || rs[0].Outcome != want || posts != claims {
						t.Fatalf("active campaign after midnight: %+v, claims %d; want %s, %d", rs, posts, want, claims)
					}
				}
				before := posts
				now = time.Date(2026, 10, 8, 10, 0, 0, 0, beijing)
				campaign.StartAt += 86400
				campaign.EndAt += 86400
				status = "CLAIMABLE"
				rs := c.checkinNow(t.Context(), false)
				if len(rs) != 1 || rs[0].Outcome != CheckinClaimed || !rs[0].Asked || posts != before+1 {
					t.Fatalf("new campaign wasn't claimed: %+v, claims %d", rs, posts)
				}
				beforeReads, beforeClaims := gets, posts
				now = now.Add(wbCheckinEvery - time.Nanosecond)
				rs = c.checkinNow(t.Context(), false)
				if len(rs) != 1 || rs[0].Outcome != CheckinClaimed || rs[0].Asked || gets != beforeReads || posts != beforeClaims {
					t.Fatalf("asked again before campaign expiry: %+v, reads %d, claims %d", rs, gets-beforeReads, posts-beforeClaims)
				}
				rs = c.checkinNow(t.Context(), true)
				if len(rs) != 1 || rs[0].Outcome != CheckinDone || !rs[0].Asked || gets != beforeReads+1 || posts != beforeClaims {
					t.Fatalf("manual check-in didn't verify the campaign: %+v, reads %d, claims %d", rs, gets-beforeReads, posts)
				}
			})
		}
	}
}

// A successful campaign is reused for at most 30 minutes, and never past
// Beijing midnight or its end. Refreshing does not claim it again.
func TestQoderCheckinCampaignCache(t *testing.T) {
	for _, site := range []string{"qoder", QoderCNID} {
		for _, status := range []string{"CLAIMABLE", "CLAIMED"} {
			t.Run(site+"/"+status, func(t *testing.T) {
				var campaign qoderCampaign
				if err := json.Unmarshal([]byte(qoderDailyCampaignJSON), &campaign); err != nil {
					t.Fatal(err)
				}
				campaign.ClaimStatus = status
				start, end := time.Unix(int64(campaign.StartAt), 0), time.Unix(int64(campaign.EndAt), 0)
				now := start
				gets, posts := 0, 0
				a := qoderCheckinAcct{User: "arno", On: true, site: site, uid: "u1", via: func(req *http.Request) (*http.Response, error) {
					var body string
					if req.Method == http.MethodGet {
						gets++
						b, err := json.Marshal(map[string]any{"campaigns": []qoderCampaign{campaign}})
						if err != nil {
							t.Fatal(err)
						}
						body = string(b)
					} else {
						posts++
						campaign.ClaimStatus = "CLAIMED"
						body = `{"data":{"status":"CLAIMED","benefit":{"amount":100}}}`
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
				}}
				c := qoderCheckinFixture(t, &now, a)
				var l wbCheckinLoop
				rs := l.tick(t.Context(), c)
				want, claims := CheckinDone, 0
				if status == "CLAIMABLE" {
					want, claims = CheckinClaimed, 1
				}
				if len(rs) != 1 || rs[0].Outcome != want || !rs[0].ValidUntil.Equal(end) || posts != claims {
					t.Fatalf("first check-in: %+v, claims %d", rs, posts)
				}
				now = start.Add(wbCheckinEvery - time.Nanosecond)
				rs = c.checkinNow(t.Context(), false)
				if len(rs) != 1 || rs[0].Outcome != want || rs[0].Asked || gets != 1 || posts != claims {
					t.Fatalf("asked before the refresh interval: %+v, reads %d, claims %d", rs, gets, posts)
				}
				now = start.Add(wbCheckinEvery)
				rs = l.tick(t.Context(), c)
				if len(rs) != 1 || rs[0].Outcome != CheckinDone || !rs[0].Asked || gets != 2 || posts != claims {
					t.Fatalf("campaign wasn't refreshed: %+v, reads %d, claims %d", rs, gets, posts)
				}
				// A recent cached result must still be refreshed on a day change.
				now = time.Date(2026, 10, 7, 23, 55, 0, 0, beijing)
				l.tick(t.Context(), c)
				now = time.Date(2026, 10, 8, 0, 0, 0, 0, beijing)
				rs = l.tick(t.Context(), c)
				if len(rs) != 1 || rs[0].Outcome != CheckinDone || rs[0].Day != wbCheckinDay(now) || !rs[0].Asked || gets != 4 || posts != claims {
					t.Fatalf("midnight kept yesterday's result: %+v, reads %d, claims %d", rs, gets, posts)
				}
				if saved := readCheckins(c.path)[qoderCheckinKey(a)]; saved.Day != wbCheckinDay(now) {
					t.Fatalf("midnight didn't update the saved day: %+v", saved)
				}
				// A restart reads the same recent result from disk.
				restarted := newQoderCheckiner(func() []qoderCheckinAcct { return []qoderCheckinAcct{a} }, func() time.Time { return now })
				restarted.path = c.path
				rs = restarted.checkinNow(t.Context(), false)
				if len(rs) != 1 || rs[0].Day != wbCheckinDay(now) || rs[0].Asked || gets != 4 {
					t.Fatalf("restart didn't reuse the saved result: %+v, reads %d", rs, gets)
				}
				now = end.Add(-time.Minute)
				c.checkinNow(t.Context(), false)
				now = end.Add(-time.Nanosecond)
				rs = c.checkinNow(t.Context(), false)
				if len(rs) != 1 || rs[0].Outcome != CheckinDone || rs[0].Asked || gets != 5 || posts != claims {
					t.Fatalf("asked during the campaign: %+v, reads %d, claims %d", rs, gets, posts)
				}
				// Even a recent manual verification cannot extend the window.
				rs = c.checkinNow(t.Context(), true)
				if len(rs) != 1 || rs[0].Outcome != CheckinDone || !rs[0].Asked || gets != 6 || posts != claims {
					t.Fatalf("manual verification: %+v, reads %d, claims %d", rs, gets, posts)
				}
				now = end
				rs = c.checkinNow(t.Context(), false)
				if len(rs) != 1 || rs[0].Outcome != CheckinFailed || !rs[0].Asked || gets != 7 || posts != claims {
					t.Fatalf("expired result was reused: %+v, reads %d, claims %d", rs, gets, posts)
				}
				campaign.StartAt += 86400
				campaign.EndAt += 86400
				campaign.ClaimStatus = "CLAIMABLE"
				now = end.Add(wbCheckinRetry - time.Nanosecond)
				rs = c.checkinNow(t.Context(), false)
				if len(rs) != 1 || rs[0].Asked || gets != 7 {
					t.Fatalf("retried too soon: %+v, reads %d", rs, gets)
				}
				now = now.Add(time.Nanosecond)
				rs = c.checkinNow(t.Context(), false)
				if len(rs) != 1 || rs[0].Outcome != CheckinClaimed || !rs[0].Asked || gets != 8 || posts != claims+1 {
					t.Fatalf("next campaign wasn't claimed: %+v, reads %d, claims %d", rs, gets, posts)
				}
			})
		}
	}
}

// A claimed campaign, even a long one, must not hide a new or reset
// campaign. An empty list is also asked again after the same interval.
func TestQoderCheckinCampaignChanges(t *testing.T) {
	for _, site := range []string{"qoder", QoderCNID} {
		for _, change := range []string{"long-campaign", "reset", "inactive"} {
			t.Run(site+"/"+change, func(t *testing.T) {
				var campaign qoderCampaign
				if err := json.Unmarshal([]byte(qoderDailyCampaignJSON), &campaign); err != nil {
					t.Fatal(err)
				}
				if change == "long-campaign" {
					campaign.EndAt += 29 * 86400
				}
				now := time.Unix(int64(campaign.StartAt), 0)
				campaigns := []qoderCampaign{campaign}
				want := CheckinDone
				if change == "inactive" {
					campaigns, want = nil, CheckinInactive
				}
				gets, posts := 0, 0
				a := qoderCheckinAcct{User: "arno", On: true, site: site, uid: "u1", via: func(req *http.Request) (*http.Response, error) {
					body := `{"data":{"status":"CLAIMED","benefit":{"amount":100}}}`
					if req.Method == http.MethodGet {
						gets++
						b, err := json.Marshal(map[string]any{"campaigns": campaigns})
						if err != nil {
							t.Fatal(err)
						}
						body = string(b)
					} else {
						posts++
					}
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
				}}
				c := qoderCheckinFixture(t, &now, a)
				rs := c.checkinNow(t.Context(), false)
				if len(rs) != 1 || rs[0].Outcome != want || gets != 1 || posts != 0 {
					t.Fatalf("initial campaign: %+v, reads %d, claims %d", rs, gets, posts)
				}
				campaign.ClaimStatus = "CLAIMABLE"
				if change == "long-campaign" {
					campaign.ID = "next-daily"
					campaign.StartAt = float64(now.Add(wbCheckinEvery).Unix())
					campaign.EndAt = campaign.StartAt + 86400
					campaigns = append(campaigns, campaign)
				} else {
					campaigns = []qoderCampaign{campaign}
				}
				now = now.Add(wbCheckinEvery - time.Nanosecond)
				rs = c.checkinNow(t.Context(), false)
				if len(rs) != 1 || rs[0].Outcome != want || rs[0].Asked || gets != 1 || posts != 0 {
					t.Fatalf("asked too soon: %+v, reads %d, claims %d", rs, gets, posts)
				}
				now = now.Add(time.Nanosecond)
				rs = c.checkinNow(t.Context(), false)
				if len(rs) != 1 || rs[0].Outcome != CheckinClaimed || !rs[0].Asked || gets != 2 || posts != 1 {
					t.Fatalf("%s wasn't claimed after refresh: %+v, reads %d, claims %d", change, rs, gets, posts)
				}
			})
		}
	}
}

func TestQoderCheckinCampaignBounds(t *testing.T) {
	for _, scale := range []int64{1, 1000} {
		start := time.Date(2026, 10, 7, 10, 0, 0, 0, beijing)
		if scale == 1000 {
			start = start.Add(123 * time.Millisecond)
		}
		end := start.Add(time.Hour)
		startAt, endAt := start.Unix(), end.Unix()
		if scale == 1000 {
			startAt, endAt = start.UnixMilli(), end.UnixMilli()
		}
		for _, status := range []string{"CLAIMABLE", "CLAIMED"} {
			for _, tc := range []struct {
				name string
				now  time.Time
				want string
			}{
				{"before", start.Add(-time.Nanosecond), CheckinFailed},
				{"start", start, CheckinClaimed},
				{"last", end.Add(-time.Nanosecond), CheckinClaimed},
				{"end", end, CheckinFailed},
			} {
				t.Run(fmt.Sprintf("%s/%s/%d", status, tc.name, scale), func(t *testing.T) {
					want := tc.want
					if status == "CLAIMABLE" {
						want = CheckinClaimed
					} else if want == CheckinClaimed {
						want = CheckinDone
					}
					claims := 0
					a := qoderCheckinAcct{site: "qoder", via: func(req *http.Request) (*http.Response, error) {
						body := fmt.Sprintf(`{"campaigns":[{"campaignId":"daily","actionType":"CLAIM_BENEFIT","claimStatus":%q,"startAt":%d,"endAt":%d,"benefit":{"amount":100}}]}`, status, startAt, endAt)
						if req.Method == http.MethodPost {
							claims++
							body = `{"status":"CLAIMED","benefit":{"amount":100}}`
						}
						return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
					}}
					r := qoderCheckin(t.Context(), a, tc.now)
					if r.Outcome != want || (claims == 1) != (want == CheckinClaimed) {
						t.Fatalf("at %s: %+v, claims %d; want %s", tc.now, r, claims, want)
					}
					if tc.want == CheckinClaimed && !r.ValidUntil.Equal(end) {
						t.Fatalf("campaign expiry: %s; want %s", r.ValidUntil, end)
					}
				})
			}
		}
	}
}

func TestQoderCheckinClaimableWithoutWindow(t *testing.T) {
	for _, site := range []string{"qoder", QoderCNID} {
		t.Run(site, func(t *testing.T) {
			claims := 0
			a := qoderCheckinAcct{site: site, via: func(req *http.Request) (*http.Response, error) {
				body := `{"campaigns":[{"campaignId":"daily","actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMABLE","benefit":{"amount":100}}]}`
				if req.Method == http.MethodPost {
					claims++
					body = `{"status":"CLAIMED","benefit":{"amount":100}}`
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			}}
			r := qoderCheckin(t.Context(), a, time.Now())
			if r.Outcome != CheckinClaimed || r.Credit != 100 || claims != 1 {
				t.Fatalf("server-claimable campaign without times: %+v, claims %d", r, claims)
			}
		})
	}
}

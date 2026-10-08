package provider

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// copilotUsageOf is the card magpie makes of one /copilot_internal/user body.
func copilotUsageOf(t *testing.T, body string) SubscriptionQuota {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := CopilotUserURL
	CopilotUserURL = srv.URL
	t.Cleanup(func() { CopilotUserURL = old })
	q := copilotSubscriptionUsage(t.Context(), "tok", "")
	if q.Error != "" {
		t.Fatalf("error: %s", q.Error)
	}
	return q
}

func copilotWindow(q SubscriptionQuota, name string) *QuotaWindow {
	for i := range q.Windows {
		if q.Windows[i].Name == name {
			return &q.Windows[i]
		}
	}
	return nil
}

// The snapshots of a token-based-billing account's quota_snapshots, the way
// GitHub sends them: a Max (individual_max) account's, read from the real
// endpoint on 2026-10-07 with its ids and lists left out, and the
// Business/Enterprise shapes VS Code's own parser is tested with
// (chatEntitlementService.test.ts): entitlement as a string, has_quota
// false on every snapshot, an organization's pool as unlimited.
const copilotUnlimitedSnaps = `"chat":{"overage_count":0,"overage_permitted":false,"percent_remaining":100.0,"quota_id":"chat","quota_remaining":0.0,"unlimited":true,"timestamp_utc":"2026-10-06T14:40:30.429-07:00","has_quota":true,"quota_reset_at":0,"token_based_billing":true,"credits_used":0,"overage_entitlement":0,"remaining":0,"entitlement":0},
"completions":{"overage_count":0,"overage_permitted":false,"percent_remaining":100.0,"quota_id":"completions","quota_remaining":0.0,"unlimited":true,"timestamp_utc":"2026-10-06T14:40:30.429-07:00","has_quota":true,"quota_reset_at":0,"token_based_billing":true,"credits_used":0,"overage_entitlement":0,"remaining":0,"entitlement":0}`

const copilotBusinessUnlimited = `"chat":{"overage_count":0,"overage_entitlement":0,"overage_permitted":false,"percent_remaining":100,"unlimited":true,"entitlement":"0","has_quota":false},
"completions":{"overage_count":0,"overage_entitlement":0,"overage_permitted":false,"percent_remaining":100,"unlimited":true,"entitlement":"0","has_quota":false}`

func TestCopilotUsageReadsEverySeat(t *testing.T) {
	type want struct {
		used    float64
		display string
		aside   bool
	}
	for _, c := range []struct {
		name, body string
		premium    *want // nil: the card must say Premium requests are unlimited
	}{
		{"Max with allowance left, the real response", `{"access_type_sku":"free_github_star_quota","copilot_plan":"individual_max","quota_reset_date":"2026-11-01","quota_snapshots":{` + copilotUnlimitedSnaps + `,
"premium_interactions":{"overage_count":0,"overage_permitted":false,"percent_remaining":99.9,"quota_id":"premium_interactions","quota_remaining":19999.9,"unlimited":false,"timestamp_utc":"2026-10-06T14:40:30.429-07:00","has_quota":true,"quota_reset_at":0,"token_based_billing":true,"credits_used":0,"overage_entitlement":0,"remaining":19999,"entitlement":20000}},
"quota_reset_date_utc":"2026-11-01T00:00:00.000Z","token_based_billing":true}`,
			&want{0.0005, "0.1 / 20000", false}},
		{"Pro used up, overage permitted: spending goes on", `{"access_type_sku":"monthly_subscriber_quota","copilot_plan":"individual","quota_reset_date_utc":"2026-11-01T00:00:00.000Z","quota_snapshots":{` + copilotUnlimitedSnaps + `,
"premium_interactions":{"overage_count":12,"overage_permitted":true,"percent_remaining":0.0,"quota_id":"premium_interactions","quota_remaining":0.0,"unlimited":false,"has_quota":false,"quota_reset_at":0,"overage_entitlement":0,"remaining":0,"entitlement":300}},"token_based_billing":true}`,
			&want{100, "300 / 300", true}},
		{"Business with allowance left (entitlement as a string, has_quota false)", `{"access_type_sku":"copilot_for_business_seat","copilot_plan":"business","quota_reset_date_utc":"2026-11-01T00:00:00.000Z","token_based_billing":true,"quota_snapshots":{` + copilotBusinessUnlimited + `,
"premium_interactions":{"overage_count":0,"overage_entitlement":0,"overage_permitted":false,"percent_remaining":5.5,"unlimited":false,"entitlement":"1000","has_quota":false}}}`,
			&want{94.5, "", false}},
		{"Business used up (#1063)", `{"access_type_sku":"copilot_for_business_seat","copilot_plan":"business","quota_reset_date_utc":"2026-11-01T00:00:00.000Z","token_based_billing":true,"quota_snapshots":{` + copilotBusinessUnlimited + `,
"premium_interactions":{"overage_count":0,"overage_entitlement":0,"overage_permitted":false,"percent_remaining":0,"unlimited":false,"entitlement":"300","quota_remaining":"0","has_quota":false}}}`,
			&want{100, "300 / 300", false}},
		{"Business used up, numbers as github.com sent the Max one (#1063's card: Chat and Completions unlimited, nothing else)", `{"access_type_sku":"copilot_for_business_seat","copilot_plan":"business","quota_reset_date":"2026-11-01","quota_snapshots":{` + copilotUnlimitedSnaps + `,
"premium_interactions":{"overage_count":0,"overage_permitted":false,"percent_remaining":0.0,"quota_id":"premium_interactions","quota_remaining":0.0,"unlimited":false,"has_quota":false,"quota_reset_at":0,"token_based_billing":true,"credits_used":0,"overage_entitlement":0,"remaining":0,"entitlement":300}},
"quota_reset_date_utc":"2026-11-01T00:00:00.000Z","token_based_billing":true}`,
			&want{100, "300 / 300", false}},
		{"Business used up, overage permitted: the organization still pauses it", `{"access_type_sku":"copilot_for_business_seat","copilot_plan":"business","quota_reset_date_utc":"2026-11-01T00:00:00.000Z","token_based_billing":true,"quota_snapshots":{` + copilotBusinessUnlimited + `,
"premium_interactions":{"overage_count":0,"overage_entitlement":0,"overage_permitted":true,"percent_remaining":0,"unlimited":false,"entitlement":"300","has_quota":false}}}`,
			&want{100, "", false}},
		{"Enterprise's pooled allowance spent", `{"access_type_sku":"copilot_enterprise_seat_multi_quota","copilot_plan":"enterprise","token_based_billing":true,"quota_reset_date_utc":"2026-11-01T00:00:00.000Z","quota_snapshots":{` + copilotBusinessUnlimited + `,
"premium_interactions":{"overage_count":0,"overage_entitlement":0,"overage_permitted":false,"percent_remaining":0,"unlimited":true,"entitlement":"0","has_quota":false}}}`,
			&want{100, "", false}},
		{"Enterprise's pooled allowance with some left", `{"access_type_sku":"copilot_enterprise_seat_multi_quota","copilot_plan":"enterprise","token_based_billing":true,"quota_snapshots":{"premium_interactions":{"overage_count":0,"overage_entitlement":0,"overage_permitted":false,"percent_remaining":50,"unlimited":true,"entitlement":"0","has_quota":true}}}`,
			nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := copilotUsageOf(t, c.body)
			w := copilotWindow(q, "Premium requests")
			if w == nil {
				t.Fatalf("no Premium requests window: %+v", q.Windows)
			}
			if c.premium == nil {
				if !w.Unlimited {
					t.Fatalf("pool with some left read as %+v", *w)
				}
				return
			}
			if w.Unlimited || w.Display != c.premium.display || w.Aside != c.premium.aside ||
				w.Used < c.premium.used-1e-9 || w.Used > c.premium.used+1e-9 {
				t.Fatalf("Premium requests = %+v, want %+v", *w, *c.premium)
			}
			if w.ResetsAt == nil && c.premium.used > 0 {
				t.Fatalf("no reset: %+v", *w)
			}
			for _, n := range []string{"Chat requests", "Completions"} {
				if o := copilotWindow(q, n); o != nil && !o.Unlimited {
					t.Fatalf("%s = %+v", n, *o)
				}
			}
			// a used-up seat that can't go on rests until the month renews
			now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
			used, _ := allowanceOf(q.Windows, now).For("claude-sonnet-4.5", now)
			if stops := c.premium.used >= 100 && !c.premium.aside; stops != (used >= 100) {
				t.Fatalf("routing sees %v used", used)
			}
		})
	}
}

// A Free account's premium_interactions with no allowance at all is left
// out, as VS Code leaves it; its limited chat and completions are counted
// though has_quota is false on them.
func TestCopilotUsageFreeUnderTokenBilling(t *testing.T) {
	q := copilotUsageOf(t, `{"access_type_sku":"free_limited_copilot","copilot_plan":"free","token_based_billing":true,"quota_snapshots":{
"chat":{"overage_count":0,"overage_entitlement":0,"overage_permitted":false,"percent_remaining":98.7,"unlimited":false,"entitlement":"200","has_quota":false},
"completions":{"overage_count":0,"overage_entitlement":0,"overage_permitted":false,"percent_remaining":100,"unlimited":false,"entitlement":"4000","has_quota":false},
"premium_interactions":{"overage_count":0,"overage_entitlement":0,"overage_permitted":false,"percent_remaining":0,"unlimited":false,"entitlement":"0","has_quota":false}}}`)
	if len(q.Windows) != 2 || q.Windows[0].Name != "Chat requests" || q.Windows[1].Name != "Completions" {
		t.Fatalf("windows = %+v", q.Windows)
	}
	if u := q.Windows[0].Used; u < 1.29 || u > 1.31 {
		t.Fatalf("chat used = %v", u)
	}
}

// A snapshot that doesn't say how much is used is unknown: it is left out,
// never shown as unlimited.
func TestCopilotUsageUnknownIsNotUnlimited(t *testing.T) {
	q := copilotUsageOf(t, `{"copilot_plan":"business","quota_snapshots":{"premium_interactions":{"unlimited":false,"entitlement":"many"}}}`)
	for _, w := range q.Windows {
		if w.Unlimited {
			t.Fatalf("unknown read as unlimited: %+v", q.Windows)
		}
	}
}

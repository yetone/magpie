package dimagent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A reply as the upstream sends it, with the plan record, a bucket holding a
// window, an add-on, a meter and the two reset counters.
const usageBody = `{"success":true,"data":{
  "account_id":"42",
  "subscription":{"product":{"name":"DimAgent Pro"},
    "subscription":{"status":"active","cancel_at_period_end":false},
    "current_term":{"start_at":"2026-09-01T00:00:00Z","end_at":"2026-10-01T00:00:00Z"}},
  "credits":{
    "subscription_bucket":{"id":1,"bucket_kind":"credits","total_units":700,"used_units":210,
      "remaining_units":490,"expires_at":"2026-10-01T00:00:00Z",
      "window_states":[{"id":9,"bucket_id":1,"window_duration_hours":5,"window_token_cap":250000,
        "window_token_used":100000,"window_started_at":"2026-09-29T09:00:00Z",
        "window_expires_at":"2026-09-29T14:00:00Z"}]},
    "addon_buckets":[{"id":2,"total_units":100,"remaining_units":40,"expires_at":"2026-12-31T00:00:00Z"}],
    "total_units":700,"used_units":210,"remaining_units":490},
  "resets":{"window":{"available_count":2,"nearest_expires_at":"2026-09-30T00:00:00Z"},
            "monthly_full":{"available_count":0}},
  "feature_meters":[{"feature_key":"web_search","unit":"call","total_allowance":1000,"total_remaining":940}]}}`

func TestParseUsage(t *testing.T) {
	u, err := ParseUsage([]byte(usageBody))
	if err != nil {
		t.Fatal(err)
	}
	if u.AccountID != "42" {
		t.Fatalf("account id read from a string: %q", u.AccountID)
	}
	if u.Plan.Name != "DimAgent Pro" || !u.Plan.RenewKnown || !u.Plan.Renew {
		t.Fatalf("plan: %+v", u.Plan)
	}
	if got := u.Plan.EndsAt.Format(time.RFC3339); got != "2026-10-01T00:00:00Z" {
		t.Fatalf("term end: %s", got)
	}
	b := u.Subscription
	if b.Total != 700 || b.Used != 210 || b.Remaining != 490 {
		t.Fatalf("bucket: %+v", b)
	}
	if len(b.Windows) != 1 {
		t.Fatalf("windows: %+v", b.Windows)
	}
	w := b.Windows[0]
	if w.Hours != 5 || w.Cap != 250000 || w.Used != 100000 || w.Expires.IsZero() {
		t.Fatalf("window: %+v", w)
	}
	if len(u.AddOns) != 1 || u.AddOns[0].Used != 60 {
		t.Fatalf("add-on: %+v", u.AddOns)
	}
	if len(u.Meters) != 1 || u.Meters[0].Feature != "web_search" || u.Meters[0].Used != 60 {
		t.Fatalf("meters: %+v", u.Meters)
	}
	if u.Resets.Window.Available != 2 || u.Resets.Monthly.Available != 0 {
		t.Fatalf("resets: %+v", u.Resets)
	}
}

// A plan record with no product is named by the subscription's status; one
// that says cancel_at_period_end off shows as not renewing.
func TestParseUsagePlanFallback(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal([]byte(usageBody), &v); err != nil {
		t.Fatal(err)
	}
	data := v["data"].(map[string]any)
	sub := data["subscription"].(map[string]any)
	delete(sub, "product")
	sub["subscription"].(map[string]any)["cancel_at_period_end"] = true
	body, _ := json.Marshal(v)
	u, err := ParseUsage(body)
	if err != nil {
		t.Fatal(err)
	}
	if u.Plan.Name != "active" {
		t.Fatalf("status fallback: %q", u.Plan.Name)
	}
	if u.Plan.RenewKnown && u.Plan.Renew {
		t.Fatalf("renew: %+v", u.Plan)
	}
}

// A bucket whose only window is the flat placeholder an older reply leaves
// says nothing, so the tile falls back on the credits themselves.
func TestParseUsageEmptyWindow(t *testing.T) {
	body := `{"data":{"credits":{"subscription_bucket":{"total_units":10,"used_units":3,
	  "window_duration_hours":0,"window_token_cap":0,"window_token_used":0,
	  "window_started_at":null,"window_expires_at":null}}}}`
	u, err := ParseUsage([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Subscription.Windows) != 0 {
		t.Fatalf("placeholder window kept: %+v", u.Subscription.Windows)
	}
}

func TestParseUsageNoAllowance(t *testing.T) {
	_, err := ParseUsage([]byte(`{"data":{"account_id":"1"}}`))
	if err == nil || !strings.Contains(err.Error(), "no allowance") {
		t.Fatalf("err: %v", err)
	}
}

func TestParseUsageRefused(t *testing.T) {
	_, err := ParseUsage([]byte(`{"success":false,"message":"plan expired"}`))
	if err == nil || err.Error() != "dimagent usage: plan expired" {
		t.Fatalf("err: %v", err)
	}
}

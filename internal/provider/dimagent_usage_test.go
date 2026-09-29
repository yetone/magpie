package provider

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The tiles the Usage page draws are one per allowance the account holds, so
// an account on no plan of its own — one bucket, its add-on, and a total_units
// that is only that add-on added up — gets the add-on once, not twice over.
func TestDimAgentQuotaShowsABucketOnce(t *testing.T) {
	dimagentKeep(t, "me")
	dimagentUsage(t, `{"success":true,"data":{"account_id":7,"subscription":{"product":{"name":"Example Lite"},
	  "subscription":{"status":"canceled","cancel_at_period_end":true},"last_term":{"end_at":"2026-09-21T00:00:00Z"}},
	  "credits":{"subscription_bucket":null,"addon_buckets":[{"bucket_kind":"addon",
	    "total_units":300,"used_units":60,"remaining_units":240,"expires_at":"2026-10-21T00:00:00Z",
	    "status":"active","window_token_cap":0,"window_duration_hours":0}],
	    "total_units":300,"used_units":60,"remaining_units":240},
	  "credits_display":{"enabled":true,"credit_name":"Credits"}}}`)
	q := dimagentLoginQuota(context.Background(), Login{Agent: "dimagent", User: "me"})
	if q.Error != "" {
		t.Fatal(q.Error)
	}
	if len(q.Windows) != 1 {
		t.Fatalf("the same credits shown again as a second ring: %+v", q.Windows)
	}
	w := q.Windows[0]
	if w.Name != "Add-on credits" || w.Used != 20 || !strings.Contains(w.Display, "60 / 300 Credits") {
		t.Fatalf("add-on tile: %+v", w)
	}
	if q.Plan != "Example Lite" || q.Renew != "off" || q.Until == nil {
		t.Fatalf("plan: %+v", q)
	}
}

// An account that is granted an allowance of its own gets it as its own tile,
// and a rate window inside it is named as long as it runs, which is how the
// vendor's console spells them.
func TestDimAgentQuotaTilesAndWindows(t *testing.T) {
	dimagentKeep(t, "me")
	dimagentUsage(t, `{"data":{"account_id":7,"subscription":{"product":{"name":"Pro"},
	  "subscription":{"status":"active","cancel_at_period_end":false},
	  "current_term":{"end_at":"2026-10-01T00:00:00Z"}},
	  "credits":{"subscription_bucket":{"total_units":700,"used_units":210,"remaining_units":490,
	    "window_states":[{"window_duration_hours":5,"window_token_cap":250000,"window_token_used":50000,
	      "window_expires_at":"2026-09-29T14:00:00Z"}]},
	    "addon_buckets":[{"total_units":100,"used_units":20,"remaining_units":80}],
	    "total_units":700,"used_units":210,"remaining_units":490},
	  "feature_meters":[{"feature_key":"web_search","unit":"calls","total_allowance":1000,"total_used":60}],
	  "credits_display":{"enabled":true,"credit_name":"Credits"}}}`)
	q := dimagentLoginQuota(context.Background(), Login{Agent: "dimagent", User: "me"})
	if q.Error != "" {
		t.Fatal(q.Error)
	}
	var names []string
	for _, w := range q.Windows {
		names = append(names, w.Name)
	}
	want := []string{"Credits", "5 hours", "Add-on credits", "Searches"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tiles: %v", names)
	}
	if q.Windows[0].Used != 30 || q.Windows[1].Used != 20 || q.Windows[2].Used != 20 || q.Windows[3].Used != 6 {
		t.Fatalf("percentages: %+v", q.Windows)
	}
	if q.Windows[1].Span != 5*time.Hour || q.Windows[1].ResetsAt == nil {
		t.Fatalf("rate window: %+v", q.Windows[1])
	}
	if q.Plan != "Pro" || q.Renew != "auto" {
		t.Fatalf("plan: %+v", q)
	}
}

// A reply that names no currency still says credits, and one whose bucket is
// unlimited is shown as unlimited rather than as nothing spent.
func TestDimAgentQuotaUnitsAndUnlimited(t *testing.T) {
	dimagentKeep(t, "me")
	dimagentUsage(t, `{"data":{"credits":{"subscription_bucket":{"total_units":0,"used_units":0,
	  "unlimited":true,"remaining_units":0}}}}`)
	q := dimagentLoginQuota(context.Background(), Login{Agent: "dimagent", User: "me"})
	if q.Error != "" || len(q.Windows) != 1 || q.Windows[0].Display != "unlimited" {
		t.Fatalf("unlimited bucket: %+v", q)
	}
}

// A reply carrying no allowance of any kind is a failed reading, not a tile
// of nothing spent.
func TestDimAgentQuotaNoAllowance(t *testing.T) {
	dimagentKeep(t, "me")
	dimagentUsage(t, `{"data":{"account_id":7}}`)
	if q := dimagentLoginQuota(context.Background(), Login{Agent: "dimagent", User: "me"}); q.Error == "" {
		t.Fatalf("an account with nothing to show: %+v", q)
	}
}

// The window's own names, spelled as the console spells them.
func TestDimAgentWindowNames(t *testing.T) {
	for hours, want := range map[float64]string{5: "5 hours", 168: "7 days",
		48: "2 days", 0.5: "30 minutes", 1.5: "1.5 hours", 0: "Window"} {
		if got := dimagentWindowName(hours); got != want {
			t.Errorf("%v hours: %q, want %q", hours, got, want)
		}
	}
	for feature, want := range map[string]string{"image": "Images", "web_search": "Searches",
		"video": "Video", "": "Usage", "something_else": "something_else"} {
		if got := meterName(feature); got != want {
			t.Errorf("%q: %q, want %q", feature, got, want)
		}
	}
}

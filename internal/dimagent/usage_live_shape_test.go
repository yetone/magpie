package dimagent

import (
	"strings"
	"testing"
)

// A reply as the live endpoint answers one today, its shapes kept and its
// figures and names invented. Its account_id is a number, not a string — read as a
// string the whole allowance was lost for every account. A plan that has
// ended names no current term, so its paid time ended with its last term; and
// it holds no bucket of the plan's own besides its add-ons, whose total is
// those added up and must not stand as a second ring.
const usageLive = `{"success":true,"data":{
  "account_id":7,
  "subscription":{"product":{"name":"Example Lite"},
    "subscription":{"status":"canceled","cancel_at_period_end":true},
    "current_term_id":null,
    "last_term":{"end_at":"2026-09-21T00:00:00Z","status":"ended"}},
  "credits":{
    "subscription_bucket":null,
    "addon_buckets":[{"bucket_kind":"addon","total_units":300,"used_units":10,
      "remaining_units":290,"unlimited":false,"expires_at":"2026-10-21T00:00:00Z",
      "status":"active","window_token_cap":0,"window_duration_hours":0}],
    "total_units":300,"used_units":10,"remaining_units":290},
  "feature_meters":[],
  "resets":{"window":{"available_count":0},"monthly_full":{"available_count":0}},
  "credits_display":{"enabled":true,"credit_name":"Credits","decimals":0}}}`

func TestParseUsageLiveShape(t *testing.T) {
	u, err := ParseUsage([]byte(usageLive))
	if err != nil {
		t.Fatal(err)
	}
	if u.AccountID != "7" {
		t.Fatalf("account id read from a number: %q", u.AccountID)
	}
	if u.Plan.Name != "Example Lite" || u.Plan.Status != "canceled" {
		t.Fatalf("plan: %+v", u.Plan)
	}
	if got := u.Plan.EndsAt.Format("2006-01-02"); got != "2026-09-21" {
		t.Fatalf("an ended plan's paid term: %s", got)
	}
	if !u.Plan.RenewKnown || u.Plan.Renew {
		t.Fatalf("renew: %+v", u.Plan)
	}
	// its stated total is its one add-on's, so it is left out rather than
	// shown as the same credits twice
	if u.Total.holds() {
		t.Fatalf("the buckets' own sum shown again: %+v", u.Total)
	}
	if len(u.AddOns) != 1 {
		t.Fatalf("add-ons: %+v", u.AddOns)
	}
	a := u.AddOns[0]
	if a.Total != 300 || a.Used != 10 || a.Remaining != 290 {
		t.Fatalf("add-on: %+v", a)
	}
	if a.Units != "Credits" {
		t.Fatalf("units read from credits_display: %+v", a)
	}
	if len(a.Windows) != 0 {
		t.Fatalf("a window of no cap kept as a rate window: %+v", a.Windows)
	}
}

// A reply that names no buckets at all, leaving the account's credits as the
// one figure it states, is read from that figure — otherwise an account with
// credits would seem to have none.
func TestParseUsageFlatTotalAlone(t *testing.T) {
	const body = `{"data":{"account_id":7,"credits":{
	  "subscription_bucket":null,"addon_buckets":[],
	  "total_units":900,"used_units":20,"remaining_units":880},
	  "credits_display":{"enabled":true,"credit_name":"Credits"}}}`
	u, err := ParseUsage([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if u.Total.Total != 900 || u.Total.Used != 20 || u.Total.Remaining != 880 {
		t.Fatalf("tally: %+v", u.Total)
	}
	if u.Total.Units != "Credits" {
		t.Fatalf("tally units: %+v", u.Total)
	}
	if len(u.AddOns) != 0 || u.Subscription.holds() {
		t.Fatalf("buckets invented out of the tally: %+v", u)
	}
}

// An add-on bucket whose units the reply doesn't name still reads: a vendor
// that shows no currency of its own leaves the reader to say credits.
func TestParseUsageNoCreditName(t *testing.T) {
	body := strings.Replace(usageLive, `"credits_display":{"enabled":true,"credit_name":"Credits","decimals":0}`,
		`"credits_display":{"enabled":false,"credit_name":"Credits"}`, 1)
	u, err := ParseUsage([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if u.AddOns[0].Units != "" {
		t.Fatalf("a currency the vendor doesn't show: %+v", u.AddOns[0])
	}
}

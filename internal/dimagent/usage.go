package dimagent

// The account's allowance. /api/me/usage answers what DimAgent's own console
// draws: the subscription's credit buckets and, on a plan that runs its calls
// through a window, that window's cap and how much of it is spent.
//
//	{"success":true,"data":{
//	  "account_id":7,
//	  "subscription":{"product":{"name":…},"subscription":{"status":…,
//	               "cancel_at_period_end":…},"current_term":{"start_at":…,"end_at":…},
//	               "last_term":{"end_at":…}},
//	  "credits":{"subscription_bucket":{"total_units":700,"used_units":210,
//	               "remaining_units":490,"unlimited":…,"status":…,"bucket_kind":…,
//	               "expires_at":…,"hard_deadline_at":…,
//	               "window_states":[{"window_duration_hours":5,"window_token_cap":…,
//	                 "window_token_used":…,"window_started_at":…,"window_expires_at":…}]},
//	             "addon_buckets":[…],
//	             "total_units":…, "used_units":…, "remaining_units":…},
//	  "resets":{"window":{"available_count":…,"nearest_expires_at":…},
//	            "monthly_full":{…}},
//	  "feature_meters":[{"feature_key":…,"unit":…,"total_remaining":…,
//	                     "total_allowance":…,"total_used":…,"unlimited":…,"period_end":…}],
//	  "credits_display":{"enabled":…,"credit_name":"Credits"}}}
//
// Which fields a reply leaves out varies by the account, so the reader takes
// each as it arrives rather than failing over one shape:
//
//   - account_id is a number, though a console prints the same digits;
//   - a plan that has run out names no current_term, and its paid time ended
//     with the last_term the record still carries;
//   - an account on no plan has no subscription_bucket and no credits of the
//     plan's own, so its total_units is its add-ons added up — the same
//     credits shown twice;
//   - what the units count is stated by credits_display.credit_name, not by a
//     bucket's bucket_kind, which only says which pile a bucket is.
//
// The plan's name is product.name, falling back to the subscription's status
// when there is no product. A bucket that names no total says nothing about an
// allowance; one that is unlimited is shown as unlimited rather than as
// nothing spent.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Bucket is one allowance: units spent of units granted, and the windows the
// upstream counts them in. Which pile it is — the plan's own or an add-on —
// the reader already knows from where it found it; a bucket_kind says no more
// than that, so it isn't kept.
type Bucket struct {
	Units        string // what its units are counted in, as the vendor names them
	Total        float64
	Used         float64
	Remaining    float64
	Unlimited    bool
	ExpiresAt    time.Time
	HardDeadline time.Time
	Status       string
	Windows      []Window
}

// Window is a rate-limit window inside a bucket: how long it runs, what it
// allows, and when it starts again. The upstream counts hours as a number, so
// a window may be half one.
type Window struct {
	Hours   float64
	Cap     float64
	Used    float64
	Started time.Time
	Expires time.Time
}

// empty is a window that says nothing: all zero, as an older reply leaves its
// flat fields when a bucket has no window at all.
func (w Window) empty() bool {
	return w.Hours == 0 && w.Cap == 0 && w.Used == 0 && w.Expires.IsZero()
}

// Plan is the subscription as the record describes it: what the account is
// on, whether it renews itself, and when the paid term ends.
type Plan struct {
	Name       string
	Status     string
	Renew      bool      // cancel_at_period_end is false, so the plan renews
	RenewKnown bool      // the record said one way or the other
	EndsAt     time.Time // current_term end, its last_term's once the plan is over: renewal when Renew, the end when not
}

// Usage is what one account was told about itself.
type Usage struct {
	AccountID    string
	Plan         Plan
	Subscription Bucket
	AddOns       []Bucket
	Total        Bucket // the account's flat tally, kept only in place of buckets
	Resets       Resets
	Meters       []FeatureMeter
}

// Resets is when the allowance comes back: the app reads it out of two
// counters, one for the window and one for the month.
type Resets struct {
	Window  Reset
	Monthly Reset
}

// Reset is one of those counters: how many resets are still owed, and when
// the next one lands.
type Reset struct {
	Available int
	NextAt    time.Time
}

// FeatureMeter is an allowance counted by something other than credits —
// images made, seconds of audio — which the console lists beside them.
type FeatureMeter struct {
	Feature   string
	Unit      string
	Remaining float64
	Allowance float64
	Used      float64
	Unlimited bool
	PeriodEnd time.Time
}

// window is the shape a rate window is sent in, both inside window_states and
// laid flat on the bucket, which is how the older replies spell it.
type window struct {
	WindowDurationHrs float64 `json:"window_duration_hours"`
	WindowTokenCap    float64 `json:"window_token_cap"`
	WindowTokenUsed   float64 `json:"window_token_used"`
	WindowStartedAt   any     `json:"window_started_at"`
	WindowExpiresAt   any     `json:"window_expires_at"`
}

func (w window) asWindow() Window {
	return Window{Hours: w.WindowDurationHrs, Cap: w.WindowTokenCap, Used: w.WindowTokenUsed,
		Started: parseTime(w.WindowStartedAt), Expires: parseTime(w.WindowExpiresAt)}
}

// ParseUsage reads the answer. A reply that carries no allowance at all is an
// error, so a caller says it couldn't read the quota rather than showing
// nothing spent.
func ParseUsage(body []byte) (Usage, error) {
	var env struct {
		Success *bool           `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return Usage{}, fmt.Errorf("dimagent usage: unreadable reply: %w", err)
	}
	if env.Success != nil && !*env.Success {
		return Usage{}, fmt.Errorf("dimagent usage: %s", nonEmpty(env.Message, "the upstream refused"))
	}
	payload := env.Data
	if len(payload) == 0 || string(payload) == "null" {
		payload = body // a reply that is the object bare
	}
	var raw struct {
		// The upstream answers an account's own id as a number; a console
		// prints the same figure, so its shape isn't one to fail a whole
		// reading over.
		AccountID    any `json:"account_id"`
		Subscription struct {
			Product struct {
				Name string `json:"name"`
			} `json:"product"`
			Subscription struct {
				Status            string `json:"status"`
				CancelAtPeriodEnd *bool  `json:"cancel_at_period_end"`
			} `json:"subscription"`
			CurrentTerm struct {
				EndAt any `json:"end_at"`
			} `json:"current_term"`
			// a plan that has run out names no current term at all: its
			// paid time ended with the last term it had
			LastTerm struct {
				EndAt any `json:"end_at"`
			} `json:"last_term"`
		} `json:"subscription"`
		Credits struct {
			SubscriptionBucket json.RawMessage   `json:"subscription_bucket"`
			AddonBuckets       []json.RawMessage `json:"addon_buckets"`
			TotalUnits         *float64          `json:"total_units"`
			UsedUnits          *float64          `json:"used_units"`
			RemainingUnits     *float64          `json:"remaining_units"`
		} `json:"credits"`
		// what the vendor calls its currency, and whether it shows it at all
		CreditsDisplay struct {
			Enabled    *bool  `json:"enabled"`
			CreditName string `json:"credit_name"`
		} `json:"credits_display"`
		Resets struct {
			Window      resetRaw `json:"window"`
			MonthlyFull resetRaw `json:"monthly_full"`
		} `json:"resets"`
		FeatureMeters []struct {
			FeatureKey     string   `json:"feature_key"`
			Unit           string   `json:"unit"`
			TotalRemaining *float64 `json:"total_remaining"`
			TotalAllowance *float64 `json:"total_allowance"`
			TotalUsed      *float64 `json:"total_used"`
			Unlimited      bool     `json:"unlimited"`
			PeriodEnd      any      `json:"period_end"`
		} `json:"feature_meters"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Usage{}, fmt.Errorf("dimagent usage: unreadable reply: %w", err)
	}
	u := Usage{AccountID: accountIDOf(raw.AccountID)}
	// the plan's name as the vendor's console reads it: product name, or the
	// subscription's status when there is no product
	u.Plan = Plan{Name: strings.TrimSpace(raw.Subscription.Product.Name),
		Status: strings.TrimSpace(raw.Subscription.Subscription.Status),
		EndsAt: parseTime(raw.Subscription.CurrentTerm.EndAt)}
	// a plan that ended names no current term; its paid time ended with the
	// last term it had, which the record still carries
	if u.Plan.EndsAt.IsZero() {
		u.Plan.EndsAt = parseTime(raw.Subscription.LastTerm.EndAt)
	}
	if u.Plan.Name == "" {
		u.Plan.Name = u.Plan.Status
	}
	if c := raw.Subscription.Subscription.CancelAtPeriodEnd; c != nil {
		u.Plan.Renew, u.Plan.RenewKnown = !*c, true
	}
	units := strings.TrimSpace(raw.CreditsDisplay.CreditName)
	if raw.CreditsDisplay.Enabled != nil && !*raw.CreditsDisplay.Enabled {
		units = "" // the vendor shows no currency of its own
	}
	if b, ok := parseBucket(raw.Credits.SubscriptionBucket, units); ok {
		u.Subscription = b
	}
	for _, a := range raw.Credits.AddonBuckets {
		if b, ok := parseBucket(a, units); ok {
			u.AddOns = append(u.AddOns, b)
		}
	}
	// An account's own tally is an aggregate over its buckets, so it says
	// something of its own only when the reply names none: kept beside them
	// it would be the same credits counted twice over.
	if raw.Credits.TotalUnits != nil && *raw.Credits.TotalUnits > 0 &&
		!u.Subscription.holds() && len(u.AddOns) == 0 {
		t := Bucket{Units: units, Total: *raw.Credits.TotalUnits}
		if raw.Credits.UsedUnits != nil {
			t.Used = *raw.Credits.UsedUnits
		}
		if raw.Credits.RemainingUnits != nil {
			t.Remaining = *raw.Credits.RemainingUnits
		}
		t.Total, t.Used, t.Remaining = threeOf(t.Total, t.Used, t.Remaining)
		u.Total = t
	}
	u.Resets.Window = raw.Resets.Window.asReset()
	u.Resets.Monthly = raw.Resets.MonthlyFull.asReset()
	for _, f := range raw.FeatureMeters {
		m := FeatureMeter{Feature: f.FeatureKey, Unit: f.Unit, Unlimited: f.Unlimited, PeriodEnd: parseTime(f.PeriodEnd)}
		if f.TotalAllowance != nil {
			m.Allowance = *f.TotalAllowance
		}
		if f.TotalUsed != nil {
			m.Used = *f.TotalUsed
		}
		if f.TotalRemaining != nil {
			m.Remaining = *f.TotalRemaining
		}
		m.Allowance, m.Used, m.Remaining = threeOf(m.Allowance, m.Used, m.Remaining)
		u.Meters = append(u.Meters, m)
	}
	if !u.hasAllowance() {
		return Usage{}, fmt.Errorf("dimagent usage: the reply has no allowance")
	}
	return u, nil
}

// threeOf fills in the one of total / used / remaining a reply leaves out from
// the other two, which is how it spells a bucket it hasn't worked out itself.
// Units may be counted in fractions, so they stay floats throughout.
func threeOf(total, used, remaining float64) (float64, float64, float64) {
	switch {
	case total > 0 && used == 0 && remaining > 0:
		used = total - remaining
	case total > 0 && remaining == 0 && used > 0:
		remaining = total - used
	case total == 0 && used > 0 && remaining > 0:
		total = used + remaining
	}
	return total, used, remaining
}

// holds is whether a bucket says an allowance of its own. An account with none
// of them carries only the flat total_units, which is an aggregate over the
// buckets rather than a bucket itself.
func (b Bucket) holds() bool {
	return b.Total > 0 || b.Unlimited || len(b.Windows) > 0
}

// hasAllowance says the account has anything to show: a bucket of its own, a
// tally in place of them, or a meter counting something else.
func (u Usage) hasAllowance() bool {
	return u.Subscription.holds() || u.Total.holds() ||
		len(u.AddOns) > 0 || len(u.Meters) > 0
}

// accountIDOf is the account's id as written: the upstream sends a number
// where its console prints digits, and either arrives.
func accountIDOf(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case string:
		return strings.TrimSpace(t)
	}
	return ""
}

// resetRaw is one of the two reset counters, whose count may be said as a
// number or as a numeric string.
type resetRaw struct {
	AvailableCount any `json:"available_count"`
	NearestExpires any `json:"nearest_expires_at"`
}

func (r resetRaw) asReset() Reset {
	out := Reset{NextAt: parseTime(r.NearestExpires)}
	switch n := r.AvailableCount.(type) {
	case float64:
		out.Available = int(n)
	case string:
		var i int
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &i); err == nil {
			out.Available = i
		}
	}
	return out
}

// parseBucket reads one allowance. Its units are named total_units /
// used_units / remaining_units, and its expiry either as a number or an
// RFC3339 string; a bucket that says no total and no window says nothing and
// is reported as such.
func parseBucket(raw json.RawMessage, units string) (Bucket, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return Bucket{}, false
	}
	var b struct {
		TotalUnits     float64  `json:"total_units"`
		UsedUnits      float64  `json:"used_units"`
		RemainingUnits float64  `json:"remaining_units"`
		Unlimited      bool     `json:"unlimited"`
		Status         string   `json:"status"`
		ExpiresAt      any      `json:"expires_at"`
		HardDeadlineAt any      `json:"hard_deadline_at"`
		WindowStates   []window `json:"window_states"`
		// an older reply lays one window out flat on the bucket instead
		window
	}
	if json.Unmarshal(raw, &b) != nil {
		return Bucket{}, false
	}
	out := Bucket{Units: units, Unlimited: b.Unlimited, Status: b.Status,
		Total: b.TotalUnits, Used: b.UsedUnits, Remaining: b.RemainingUnits,
		ExpiresAt: parseTime(b.ExpiresAt), HardDeadline: parseTime(b.HardDeadlineAt)}
	out.Total, out.Used, out.Remaining = threeOf(out.Total, out.Used, out.Remaining)
	for _, w := range b.WindowStates {
		win := w.asWindow()
		if win.empty() {
			continue // a placeholder window, which says nothing
		}
		out.Windows = append(out.Windows, win)
	}
	// the older shape: one window laid flat on the bucket itself
	if len(b.WindowStates) == 0 {
		if win := b.window.asWindow(); !win.empty() {
			out.Windows = append(out.Windows, win)
		}
	}
	if out.Total <= 0 && !out.Unlimited && len(out.Windows) == 0 {
		return Bucket{}, false
	}
	return out, true
}

// parseTime reads a stamp the upstream may send as unix seconds, unix
// milliseconds, an RFC3339 string, or nothing at all.
func parseTime(v any) time.Time {
	switch t := v.(type) {
	case nil:
		return time.Time{}
	case float64:
		if t <= 0 {
			return time.Time{}
		}
		if t > 1e12 { // milliseconds
			return time.UnixMilli(int64(t))
		}
		return time.Unix(int64(t), 0)
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return time.Time{}
		}
		for _, f := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
			if when, err := time.Parse(f, s); err == nil {
				return when
			}
		}
	}
	return time.Time{}
}

func nonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

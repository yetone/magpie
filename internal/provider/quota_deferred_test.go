package provider

import (
	"encoding/json"
	"testing"
	"time"
)

// An account deliberately not read has an older reading, not a failed
// request. Keep that distinction in both the GUI card and CLI report.
func TestQuotaDeferredReadIsNotFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clear := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	}
	clear()
	t.Cleanup(clear)
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	good := SubscriptionQuota{Provider: "claude", User: "saved@example.com", ReadAt: &at,
		Windows: []QuotaWindow{{Name: "7 days", Used: 32}}}
	keepLast(good, "")
	clear() // as after an app restart
	deferred := func(v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var obj map[string]any
		if err := json.Unmarshal(b, &obj); err != nil {
			t.Fatal(err)
		}
		return obj["readDeferred"] == true
	}
	for _, tc := range []struct {
		name, err string
		want      bool
	}{
		{"saved", errClaudeSaved.Error(), true},
		{"not asked", errClaudeNotAsked.Error(), true},
		{"manual only", errNotAsked.Error(), true},
		// holdUnasked stops the vendor's request itself, so the error
		// arrives wrapped by the HTTP client.
		{"held request", `Get "https://api.example.com/usage": ` + errNotAsked.Error(), true},
		{"rate limited", "HTTP 429 Too Many Requests", false},
		{"unavailable", errClaudeUsageUnavailable.Error(), false},
		{"network", "connection refused", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := keepLast(SubscriptionQuota{Provider: good.Provider, User: good.User, Error: tc.err}, "")
			if q.Error != "" || q.AsOf == nil || q.ReadAt == nil || !q.ReadAt.Equal(at) || len(q.Windows) != 1 || q.Windows[0].Used != 32 {
				t.Fatalf("cached reading lost its values or time: %+v", q)
			}
			if got := deferred(q); got != tc.want {
				t.Errorf("readDeferred = %v, want %v for %q", got, tc.want, tc.err)
			}
			report := quotaReport([]SubscriptionQuota{q}, nil, nil, time.Now())
			if len(report) != 1 || deferred(report[0]) != tc.want {
				t.Errorf("CLI/gateway report lost readDeferred: %+v", report)
			}
			// a failed read says why beside the reading it stands in
			// for (tmr11235 on X: agy-cli's quota stood days old with no
			// reason); a deferred one failed at nothing
			want := tc.err
			if tc.want {
				want = ""
			}
			if q.ReadError != want || len(report) != 1 || report[0].ReadError != want {
				t.Errorf("readError = %q (report %+v), want %q", q.ReadError, report, want)
			}
		})
	}
	if q := keepLast(SubscriptionQuota{Provider: good.Provider, User: good.User, Error: "401 unauthorized"}, ""); q.Error == "" || deferred(q) {
		t.Fatalf("an account refusal was hidden as a deferred read: %+v", q)
	}
	if q := keepLast(good, ""); q.AsOf != nil || deferred(q) || q.ReadError != "" {
		t.Fatalf("a successful reading is still deferred: %+v", q)
	}
}

package provider

import (
	"testing"
	"time"
)

// Routing sees the same model family the picker offers, including when
// quota is read before the live model catalog has been saved.
func TestAntigravityQuotaRoutesBaseModel(t *testing.T) {
	f := &fakeGoogle{
		load: `{"cloudaicompanionProject":"test-project"}`,
		models: `{"models":{
			"gemini-3.7-flash-high":{"quotaInfo":{"remainingFraction":0,"resetTime":"2099-01-01T00:00:00Z"}},
			"gemini-3.7-flash-low":{"quotaInfo":{"remainingFraction":1}},
			"gemini-3.7-flash-lite":{"quotaInfo":{"remainingFraction":1}},
			"gpt-oss-120b-medium":{"quotaInfo":{"remainingFraction":1}}}}`,
	}
	googleSandbox(t, f)
	auth := googleAuth{AccessToken: "tok", RefreshToken: "test-refresh", Expiry: time.Now().Add(time.Hour).UnixMilli()}
	if err := addGoogleLogin("antigravity", "test@example.com", "", auth); err != nil {
		t.Fatal(err)
	}
	q := googleLogins("antigravity")[0].acct.quota(t.Context(), "test-plan")
	if q.Error != "" || len(q.Windows) != 4 {
		t.Fatalf("quota: %+v", q)
	}
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	reset := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	a := allowanceOf(q.Windows, now)
	for _, model := range []string{"gemini-3.7-flash", "gemini-3.7-flash-high", "gemini-3.7-flash-low"} {
		if used, _ := a.For(model, now); used != 100 {
			t.Errorf("%s: used %v, want 100", model, used)
		}
		if got := a.Full(model, 98, now); !got.Equal(reset) {
			t.Errorf("%s: full until %v, want %v", model, got, reset)
		}
		if pace, _ := a.Pace(model, now); pace != 0 {
			t.Errorf("%s: pace %v, want 0", model, pace)
		}
	}
	for _, w := range q.Windows {
		if w.Model != w.Name {
			t.Errorf("vendor id %s changed to %s", w.Name, w.Model)
		}
	}
	for _, model := range []string{"claude-sonnet-4-6", "gemini-3.7-flash-lite", "gemini-3.7-flash-tiered"} {
		if used, _ := a.For(model, now); used != 0 {
			t.Errorf("unrelated model %s counted: %v", model, used)
		}
	}
	if used, _ := a.For("gemini-3.7-flash", reset); used != 0 {
		t.Errorf("reset window still full: %v", used)
	}

	// A restart reconstructs the matcher from the saved vendor ids.
	t.Cleanup(func() {
		lastQuotas.Lock()
		lastQuotas.loaded, lastQuotas.m = false, nil
		lastQuotas.Unlock()
	})
	keepLast(q, q.User)
	lastQuotas.Lock()
	lastQuotas.loaded, lastQuotas.m = false, nil
	lastQuotas.Unlock()
	saved := lastAllowances("antigravity")[q.User]
	if used, _ := saved.For("gemini-3.7-flash", now); used != 100 {
		t.Errorf("saved quota: used %v, want 100", used)
	}
	if got := saved.Full("gemini-3.7-flash", 98, now); !got.Equal(reset) {
		t.Errorf("saved quota: full until %v, want %v", got, reset)
	}
}

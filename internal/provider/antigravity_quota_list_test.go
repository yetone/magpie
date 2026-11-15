package provider

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/catalog"
)

// A model Google gives one Antigravity account after its list was fetched
// (01huadalang on Discord: Gemini 4 on one account of several) joins that
// account's list and the subscription's at the account's next quota
// reading, which asks fetchAvailableModels anyway: no probe, sync or
// account made preferred is needed for the picker, routing or its bar.
// An account switched off changes nothing.
func TestAntigravityQuotaReadingListsNewModels(t *testing.T) {
	t.Cleanup(AntigravityBuiltinForTest()) // the retired built-in route
	replies := map[string]string{
		"project-a":   `{"models":{"gemini-3.8-flash":{"quotaInfo":{"remainingFraction":1}},"gemini-4-argon-disabled":{"displayName":"Gemini 4 Argon","disabled":true}}}`,
		"project-b":   `{"models":{"gemini-3.8-flash":{"quotaInfo":{"remainingFraction":1}}}}`,
		"project-off": `{"models":{"gemini-3.8-flash":{}}}`,
	}
	f := &fakeGoogle{}
	// the fake holds f.mu while it asks modelsFor
	f.modelsFor = func(body map[string]any) (int, string) {
		project, _ := body["project"].(string)
		return 200, replies[project]
	}
	googleSandbox(t, f)
	for _, user := range []string{"a", "b", "off"} {
		if err := addGoogleLogin("antigravity", user+"@example.com", "", googleAuth{
			AccessToken: user, RefreshToken: "rt-" + user, Project: "project-" + user,
			Expiry: time.Now().Add(time.Hour).UnixMilli(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := switchGoogleLogin("antigravity", "a@example.com"); err != nil {
		t.Fatal(err)
	}
	for user, on := range map[string]bool{"b@example.com": true, "off@example.com": false} {
		if err := setGoogleLoginOn("antigravity", user, on); err != nil {
			t.Fatal(err)
		}
	}
	p, _ := googleAccountOf("antigravity")
	if _, err := p.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	pooled := func() []string {
		ms, _, _ := catalog.Live("antigravity")
		var ids []string
		for _, m := range ms {
			ids = append(ids, m.ID)
		}
		return ids
	}
	if got := pooled(); !slices.Equal(got, []string{"gemini-3.8-flash"}) {
		t.Fatalf("pooled before %v", got)
	}
	read := func(user string) {
		t.Helper()
		for _, l := range googleLogins("antigravity") {
			if l.User == user {
				if q := l.acct.quota(context.Background(), ""); q.Error != "" {
					t.Fatalf("%s's reading: %s", user, q.Error)
				}
				return
			}
		}
		t.Fatalf("no login %s", user)
	}
	// Google gives b Gemini 4, and gives the switched-off account a model
	f.mu.Lock()
	replies["project-b"] = `{"models":{"gemini-3.8-flash":{"quotaInfo":{"remainingFraction":1}},"gemini-4-argon":{"displayName":"Gemini 4 Argon","quotaInfo":{"remainingFraction":0.5}}}}`
	replies["project-off"] = `{"models":{"gemini-3.8-flash":{},"off-only":{}}}`
	f.mu.Unlock()
	read("a@example.com")
	read("off@example.com")
	if got := pooled(); !slices.Equal(got, []string{"gemini-3.8-flash"}) {
		t.Fatalf("pooled after a's and the off account's readings %v", got)
	}
	read("b@example.com")
	if got := pooled(); !slices.Equal(got, []string{"gemini-3.8-flash", "gemini-4-argon"}) {
		t.Fatalf("pooled after b's reading %v, want Gemini 4 in it", got)
	}
	p, _ = googleAccountOf("antigravity")
	if p.Account.Lists("gemini-4-argon") {
		t.Error("a, whose Gemini 4 is disabled, lists it")
	}
	also := p.AlsoOn()
	if len(also) != 1 || !also[0].Account.Lists("gemini-4-argon") {
		t.Error("b's own list lacks Gemini 4 after its reading")
	}
	if ms, _, _ := catalog.Live(accountModels("antigravity", "off@example.com")); slices.ContainsFunc(ms, func(m catalog.Model) bool { return m.ID == "off-only" }) {
		t.Error("a switched-off account's reading kept its list")
	}
	q, _ := Find("antigravity")
	if !slices.ContainsFunc(q.Available(), func(m catalog.Model) bool { return strings.HasPrefix(m.ID, "gemini-4-argon") }) {
		t.Error("Gemini 4 is not offered after b's reading")
	}
}

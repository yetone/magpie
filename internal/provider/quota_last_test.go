package provider

import (
	"testing"
	"time"
)

// What an account said last is kept in its home's quotas.json, and a test
// signed in (isolate) starts from its own: not from an earlier test's
// readings still in memory, which Allowances would route by until its
// first reading is back.
func TestIsolateForgetsTheLastReadings(t *testing.T) {
	signIn(t)
	rememberLogins(true)
	later := time.Now().Add(time.Hour)
	keepLast(SubscriptionQuota{Provider: "codex", User: "me@example.com", Windows: []QuotaWindow{
		{Name: "5 hours", Used: 97, ResetsAt: &later, Span: 5 * time.Hour}}}, "me@example.com")
	if _, ok := lastAllowances("codex")["me@example.com"]; !ok {
		t.Fatal("not kept")
	}
	signIn(t) // the next test, in a home of its own
	rememberLogins(true)
	if a, ok := lastAllowances("codex")["me@example.com"]; ok {
		t.Fatalf("an earlier test's reading stands in a fresh home: %+v", a)
	}
}

func TestKeepLast(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	restart := func() {
		forgetLastReadings()
	}
	restart()
	defer restart()
	later := time.Now().Add(time.Hour)
	good := SubscriptionQuota{Provider: "claude", Name: "Claude Code", User: "A@x.com", Windows: []QuotaWindow{
		{Name: "5 hours", Used: 40, ResetsAt: &later, Span: 5 * time.Hour},
		{Name: "7 days · Opus", Used: 10, Model: "opus", Span: 7 * 24 * time.Hour},
	}}
	if q := keepLast(good, ""); q.AsOf != nil || q.Windows[0].Used != 40 {
		t.Fatalf("%+v", q)
	}
	restart() // what was read is on disk

	limited := SubscriptionQuota{Provider: "claude", Name: "Claude Code", User: "a@x.com", Windows: []QuotaWindow{},
		Error: errClaudeNotAsked.Error()}
	q := keepLast(limited, "")
	if q.Error != "" || q.AsOf == nil || len(q.Windows) != 2 || q.Windows[0].Used != 40 || q.Windows[1].Model != "opus" || q.Windows[0].Span != 5*time.Hour {
		t.Fatalf("not asked: %+v", q)
	}

	// the same through LoginUsage, which knows the user but not the name
	q = keepLast(SubscriptionQuota{Provider: "claude", Windows: []QuotaWindow{}, Error: "Too Many Requests"}, "a@x.com")
	if q.Error != "" || q.Name != "Claude Code" || q.Windows[0].Used != 40 {
		t.Fatalf("login: %+v", q)
	}

	// a sign-in gone bad is said, not covered up
	if q := keepLast(SubscriptionQuota{Provider: "claude", User: "a@x.com", Error: "Claude Code's sign-in has expired"}, ""); q.Error == "" {
		t.Fatalf("expired: %+v", q)
	}

	// a new reading is used at once
	good.Windows[0].Used = 70
	keepLast(good, "")
	if q := keepLast(limited, ""); q.Windows[0].Used != 70 {
		t.Fatalf("newer: %+v", q)
	}

	// a window that has reset since starts again from nothing
	past := time.Now().Add(-time.Minute)
	good.Windows[0].ResetsAt = &past
	keepLast(good, "")
	if q := keepLast(limited, ""); q.Windows[0].Used != 0 {
		t.Fatalf("reset: %+v", q)
	}
}

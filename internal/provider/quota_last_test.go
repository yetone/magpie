package provider

import (
	"testing"
	"time"
)

func TestKeepLast(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	restart := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
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

func TestKeepLastClinePluginErrorPreservesWindows(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	lastQuotas.Lock()
	lastQuotas.m, lastQuotas.loaded = nil, false
	lastQuotas.Unlock()
	defer func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	}()
	reset := time.Now().Add(time.Hour)
	var q SubscriptionQuota
	// The plugin says the limits read failed while the rest of the account
	// read fine; a 404 is passing only through that sentence.
	for _, why := range []string{"ClinePass limits couldn't be read: HTTP 502", "ClinePass limits couldn't be read (HTTP 404: not found)"} {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
		good := SubscriptionQuota{Provider: "cline-plugin", User: "u", Windows: []QuotaWindow{{Name: "Weekly", Used: 80, ResetsAt: &reset}}}
		good = scopePluginWindows("cline", good)
		keepLast(good, "")
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
		q = keepLast(SubscriptionQuota{Provider: "cline-plugin", User: "u", Error: why}, "")
		if q.Error != "" || len(q.Windows) != 1 || q.Windows[0].Used != 80 || q.Windows[0].Model != clinePassPrefix {
			t.Fatalf("%q dropped the last good windows: %+v", why, q)
		}
	}
	if used, _ := allowanceOf(q.Windows, time.Now()).For("cline-pass/mythomax-l2-13b", time.Now()); used != 80 {
		t.Fatalf("kept ClinePass window didn't count ClinePass model: %v", used)
	}
	if used, _ := allowanceOf(q.Windows, time.Now()).For("cline-free/deepseek-v4.1-flash", time.Now()); used != 0 {
		t.Fatalf("kept ClinePass window counts free model: %v", used)
	}
	if used, _ := allowanceOf(q.Windows, reset.Add(time.Minute)).For("cline-pass/mythomax-l2-13b", reset.Add(time.Minute)); used != 0 {
		t.Fatalf("kept ClinePass window didn't reset after ResetsAt: %v", used)
	}
}

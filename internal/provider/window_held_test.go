package provider

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
)

// #4791 (hayden4608 on Discord, handong66): four accounts of one plan,
// three with 100% of 5 hours left and 0% of 7 days, one with 0% of 5 hours
// and 22% of 7 days. The three can't spend their 5 hours until their week
// starts again, so their 5 hours are held by it; the fourth's week isn't
// held by its five hours, which start again first.
func TestHeldWindowsReportersCase(t *testing.T) {
	now := time.Unix(1_793_620_800, 0) // 2026-11-02 12:00 UTC
	// /wham/usage's rate_limit, as Codex reads it
	codex := func(user string, fiveUsed, weekUsed float64, fiveIn, weekIn int64) SubscriptionQuota {
		var u codexUsage
		b := []byte(`{"plan_type":"plus","rate_limit":{"allowed":true,` +
			`"primary_window":{"used_percent":` + jsonNum(fiveUsed) + `,"limit_window_seconds":18000,"reset_at":` + jsonNum(float64(now.Unix()+fiveIn)) + `},` +
			`"secondary_window":{"used_percent":` + jsonNum(weekUsed) + `,"limit_window_seconds":604800,"reset_at":` + jsonNum(float64(now.Unix()+weekIn)) + `}}}`)
		if err := json.Unmarshal(b, &u); err != nil {
			t.Fatal(err)
		}
		return SubscriptionQuota{Provider: "codex", User: user, Plan: "plus", Windows: []QuotaWindow{u.RateLimit.Primary.window(), u.RateLimit.Secondary.window()}}
	}
	qs := []SubscriptionQuota{
		codex("a@example.com", 0, 100, 5*3600, 3*86400),
		codex("b@example.com", 0, 100, 5*3600, 2*86400),
		codex("c@example.com", 0, 100, 5*3600, 86400),
		codex("d@example.com", 100, 78, 2*3600, 4*86400),
	}
	out := WithHeldWindows(qs, now)
	for i := range 3 {
		if got := out[i].Windows[0].HeldBy; got != "7 days" {
			t.Errorf("%s: 5 hours held by %q, want 7 days", out[i].User, got)
		}
		if got := out[i].Windows[1].HeldBy; got != "" {
			t.Errorf("%s: 7 days held by %q, want none", out[i].User, got)
		}
	}
	for _, w := range out[3].Windows {
		if w.HeldBy != "" {
			t.Errorf("d: %s held by %q: a week behind five hours used up keeps its 22%% for after them", w.Name, w.HeldBy)
		}
	}
	for _, q := range qs {
		for _, w := range q.Windows {
			if w.HeldBy != "" {
				t.Fatalf("the cached cards were written: %s %s", q.User, w.Name)
			}
		}
	}
}

func TestHeldWindowsOnlyByWhatStopsTheAccount(t *testing.T) {
	now := time.Unix(1_793_620_800, 0)
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	five := QuotaWindow{Name: "5 hours", Used: 0, ResetsAt: at(time.Hour), Span: 5 * time.Hour}
	for _, c := range []struct {
		why  string
		week QuotaWindow
		user string
		held bool
	}{
		{"a week used up", QuotaWindow{Name: "7 days", Used: 100, ResetsAt: at(48 * time.Hour), Span: 168 * time.Hour}, "", true},
		{"a week used up, its reset not told", QuotaWindow{Name: "7 days", Used: 100}, "", true},
		{"one model's week (Claude's Opus)", QuotaWindow{Name: "7 days Opus", Used: 100, ResetsAt: at(48 * time.Hour), Model: "opus"}, "", false},
		{"money aside", QuotaWindow{Name: "Extra usage", Used: 100, Aside: true}, "", false},
		{"a week whose reset has passed", QuotaWindow{Name: "7 days", Used: 100, ResetsAt: at(-time.Minute)}, "", false},
		{"a week that starts again first", QuotaWindow{Name: "7 days", Used: 100, ResetsAt: at(30 * time.Minute)}, "", false},
		{"a week with room", QuotaWindow{Name: "7 days", Used: 99, ResetsAt: at(48 * time.Hour)}, "", false},
		{"a ChatGPT account going on on its credits", QuotaWindow{Name: "7 days", Used: 100, ResetsAt: at(48 * time.Hour), Credits: true}, "e@example.com", false},
	} {
		q := SubscriptionQuota{Provider: "codex", User: c.user, Windows: []QuotaWindow{five, c.week}}
		got := WithHeldWindows([]SubscriptionQuota{q}, now)[0].Windows[0].HeldBy
		if (got != "") != c.held {
			t.Errorf("%s: 5 hours held by %q, want held %v", c.why, got, c.held)
		}
	}
	// another magpie's card is as it sent it, a failed reading as it was
	q := SubscriptionQuota{Provider: "codex", From: "studio", Windows: []QuotaWindow{five, {Name: "7 days", Used: 100}}}
	if got := WithHeldWindows([]SubscriptionQuota{q}, now)[0].Windows[0].HeldBy; got != "" {
		t.Errorf("another magpie's card held by %q", got)
	}
}

// kawa_tsura on X: an Antigravity account's Claude & GPT pool read 100% of
// its 5 hours left beside its week spent, the Gemini pool 4.3% of 5 hours
// and 75.8% of the week. Once the week is used up (Google then leaves its
// remainingFraction out, read as all used), the pool's 5 hours can't be
// spent before it starts again: held by it, as a Codex account's 5 hours
// are by its week (#4791). Nothing of the Gemini pool is held by Claude's
// week, a week with 0.1% left holds nothing, and Gemini's week with room
// isn't held by its 5 hours nearly spent.
func TestHeldWindowsInAPool(t *testing.T) {
	now := time.Unix(1_795_500_000, 0)
	at := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }
	// the plugin's usage (packages/antigravity pools()), as it sends it
	card := func(claudeWeek string) SubscriptionQuota {
		var u plugin.Usage
		b := `{"plan":"Google AI Pro","windows":[
			{"name":"Claude Opus 5.5 (Thinking)","used":100,"model":"claude-opus-5-5-thinking","family":"Claude","pool":"Claude & GPT","span":604800,"resetsAt":"` + at(80*time.Hour) + `"},
			{"name":"Gemini 3.8 Flash","used":95.7,"model":"gemini-3.8-flash","family":"Gemini","pool":"Gemini","resetsAt":"` + at(2*time.Hour) + `"},
			{"name":"5 hours","pool":"Gemini","used":95.7,"aside":true,"span":18000,"resetsAt":"` + at(2*time.Hour) + `"},
			{"name":"7 days","pool":"Gemini","used":24.2,"aside":true,"span":604800,"resetsAt":"` + at(140*time.Hour) + `"},
			{"name":"5 hours","pool":"Claude & GPT","used":0,"aside":true,"span":18000,"resetsAt":"` + at(2*time.Hour) + `"},
			{"name":"7 days","pool":"Claude & GPT",` + claudeWeek + `"aside":true,"span":604800,"resetsAt":"` + at(80*time.Hour) + `"}]}`
		if err := json.Unmarshal([]byte(b), &u); err != nil {
			t.Fatal(err)
		}
		return quotaOfPlugin(SubscriptionQuota{Provider: "antigravity", User: "a@example.com"}, u)
	}
	held := func(q SubscriptionQuota) map[string]string {
		out := map[string]string{}
		for _, w := range WithHeldWindows([]SubscriptionQuota{q}, now)[0].Windows {
			if w.HeldBy != "" {
				out[w.Pool+" · "+w.Name] = w.HeldBy
			}
		}
		return out
	}
	got := held(card(`"used":100,`))
	if len(got) != 1 || got["Claude & GPT · 5 hours"] != "7 days" {
		t.Errorf("week used up: held %v, want only Claude & GPT · 5 hours, by its 7 days", got)
	}
	if got := held(card(`"used":99.9,`)); len(got) != 0 {
		t.Errorf("week with 0.1%% left: held %v, want none", got)
	}
}

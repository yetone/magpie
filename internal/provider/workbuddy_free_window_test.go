package provider

import (
	"testing"
	"time"
)

// youjianqiasongshan on Discord: a WorkBuddy Free plan card of 积分 500 /
// 500, Extra credits 3,406.7 / 3,750 and Free model tokens refused (0 left
// of 200,000,000), asking why the other credits can't be used. The usage
// answer is shaped as workbuddy 0.2.16 gives it for that card: the plan's
// credits used up and set aside (usageOf), both credit windows not counting
// the free models (spareFree), and the free model's own window refused,
// listing its two ids. Routing reads that as the free model used up and
// every other model on Extra credits, never as the account out.
func TestWorkBuddyFreeTokensSpentLeaveCredits(t *testing.T) {
	now := time.Now()
	reset := now.Add(20 * time.Hour).UTC().Format(time.RFC3339)
	usage := []byte(`{"plan":"Free plan","windows":[
		{"name":"Credits","used":100,"display":"500 / 500","amount":500,"limit":500,"unit":"credits","aside":true,"notModels":["deepseek-v4.1-flash","deepseek-v4.1-flash-sg"]},
		{"name":"Extra credits","used":90.85,"display":"3,406.7 / 3,750","amount":3406.7,"limit":3750,"unit":"credits","notModels":["deepseek-v4.1-flash","deepseek-v4.1-flash-sg"]},
		{"name":"Free model tokens","used":100,"amount":200000000,"limit":200000000,"unit":"tokens","display":"200.0M / 200M","resetsAt":"` + reset + `","span":86400,"models":["deepseek-v4.1-flash","deepseek-v4.1-flash-sg"],"refused":true}]}`)
	a := PluginAllowanceForTest(usage, now)
	if a == nil {
		t.Fatal("no allowance from the usage answer")
	}
	for _, m := range []string{"glm-5.1", "kimi-k2.6", "deepseek-v4.1"} {
		if used, _ := a.For(m, now); used >= 100 {
			t.Errorf("%s: %v%% used, want Extra credits' share", m, used)
		}
		if at := a.Full(m, 100, now); !at.IsZero() {
			t.Errorf("%s: used up until %v, want usable", m, at)
		}
	}
	for _, m := range []string{"deepseek-v4.1-flash", "deepseek-v4.1-flash-sg"} {
		if at := a.Full(m, 100, now); at.IsZero() {
			t.Errorf("%s: not used up, want held to the free window's reset", m)
		}
		if !a.Pooled(m, 100, now) {
			t.Errorf("%s: its refusal stops the account, want only the model", m)
		}
	}
	if stopped, _ := a.heldUp(); stopped {
		t.Error("the account is held up as a whole")
	}
	if a.WeekUsedUp(now) {
		t.Error("the free window reads as the account's week used up")
	}
}

package provider

import "testing"

// FreePlan reads a plan as its vendor or plugin names it: WorkBuddy's
// "Free plan", MiniMax's and Trae's "Free", a trial, a Chinese name.
func TestFreePlan(t *testing.T) {
	for plan, want := range map[string]bool{
		"Free plan": true, "Free": true, "free": true, "Pro (free trial)": true, "Trial": true, "免费版": true, "试用": true,
		"Pro": false, "Credits": false, "M Plan Plus": false, "TokenPlanMax-年度会员": false, "": false,
	} {
		if got := FreePlan(plan); got != want {
			t.Errorf("FreePlan(%q) = %v, want %v", plan, got, want)
		}
	}
}

package provider

import (
	"testing"
	"time"
)

// WithImageLane adds the image row to a read ChatGPT account's card only,
// once, and says unknown, never a share, when nothing was heard.
func TestWithImageLaneOnlyReadCodexCards(t *testing.T) {
	was := ImageRest
	t.Cleanup(func() { ImageRest = was })
	ImageRest = nil
	now := time.Now()
	win := []QuotaWindow{{Name: "5 hours", Used: 100}}
	in := []SubscriptionQuota{
		{Provider: "codex", User: "a@x.com", Windows: win},
		{Provider: "codex", User: "b@x.com", Error: "not signed in"},
		{Provider: "codex", User: "c@x.com", From: "elsewhere", Windows: win},
		{Provider: "claude", User: "d@x.com", Windows: win},
	}
	out := WithImageLane(WithImageLane(in, now), now)
	if len(out[0].Windows) != 2 || out[0].Windows[1].Lane != ImageLane || out[0].Windows[1].Used != 0 || out[0].Windows[1].Display != "Unknown" || out[0].Windows[1].ResetsAt != nil {
		t.Fatalf("read card = %+v", out[0].Windows)
	}
	for _, i := range []int{1, 2, 3} {
		if len(out[i].Windows) != len(in[i].Windows) {
			t.Errorf("card %d got a row: %+v", i, out[i].Windows)
		}
	}
	until := now.Add(time.Hour)
	ImageRest = func(agent, user string, _ time.Time) (time.Time, bool) {
		return until, agent == "codex" && user == "a@x.com"
	}
	if w := WithImageLane(in, now)[0].Windows[1]; w.ResetsAt == nil || !w.ResetsAt.Equal(until) || w.ResetSecs != 3600 {
		t.Fatalf("row = %+v", w)
	}
}

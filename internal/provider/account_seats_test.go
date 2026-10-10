package provider

import (
	"testing"
	"time"
)

// #1515 (aindijrncom): a GLM team seat added as a key (「某某·GLM高级版」)
// and signed in through ZCode is shown once, on ZCode's card. ZCode's read
// failing ("BigModel API keys: not JSON") says nothing about which seat it
// is, so the key's card stays off rather than coming back for a minute;
// only the account signed out, or windows that now disagree, bring it back.
func TestSeatKeptThroughAFailedRead(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reset := time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC)
	five := 5 * time.Hour
	plan := SubscriptionQuota{Provider: "zhipu-team", Name: "某某·GLM高级版", glmPlan: true, Plan: "Pro",
		Windows: []QuotaWindow{{Name: "5 hours", Span: five, Used: 12, ResetsAt: &reset}}}
	acct := SubscriptionQuota{Provider: "zcode-plugin", Name: "ZCode", User: "31****36@qq.com",
		Windows: []QuotaWindow{{Name: "5 hours", Span: five, Used: 12, ResetsAt: &reset}}}
	if got := notShown([]SubscriptionQuota{plan}, []SubscriptionQuota{acct}); len(got) != 0 {
		t.Fatalf("one seat on two cards: %+v", got)
	}
	failed := SubscriptionQuota{Provider: "zcode-plugin", Name: "ZCode", User: "31****36@qq.com",
		Error: "BigModel API keys: not JSON"}
	if got := notShown([]SubscriptionQuota{plan}, []SubscriptionQuota{failed}); len(got) != 0 {
		t.Fatalf("a failed read brought the key's card back: %+v", got)
	}
	// nor does a window with no reset yet (the plan's not used since)
	fresh := plan
	fresh.Windows = []QuotaWindow{{Name: "5 hours", Span: five}}
	if got := notShown([]SubscriptionQuota{fresh}, []SubscriptionQuota{failed}); len(got) != 0 {
		t.Fatalf("an unused window brought the key's card back: %+v", got)
	}
	// signed out of ZCode: the key's card is the seat's only one
	if got := notShown([]SubscriptionQuota{plan}, nil); len(got) != 1 {
		t.Fatalf("signed out, the key's card is gone: %+v", got)
	}
	// windows that disagree: another seat after all, and forgotten
	other := reset.Add(time.Hour)
	moved := SubscriptionQuota{Provider: "zcode-plugin", User: "31****36@qq.com",
		Windows: []QuotaWindow{{Name: "5 hours", Span: five, ResetsAt: &other}}}
	if got := notShown([]SubscriptionQuota{plan}, []SubscriptionQuota{moved}); len(got) != 1 {
		t.Fatalf("another seat's card hid the key's: %+v", got)
	}
	if got := notShown([]SubscriptionQuota{plan}, []SubscriptionQuota{failed}); len(got) != 1 {
		t.Fatalf("a seat told apart is still remembered: %+v", got)
	}
}

// The account's card standing in for a key's is named for it (Seat), and
// by the user's own name for the account first (Alias); the cards handed
// in are a cache's and stay as they were.
func TestSeatNamesTheAccountsCard(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reset := time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC)
	w := []QuotaWindow{{Name: "5 hours", Span: 5 * time.Hour, ResetsAt: &reset}}
	plan := SubscriptionQuota{Provider: "zhipu-team", Name: "某某·GLM高级版", glmPlan: true, Windows: w}
	subs := []SubscriptionQuota{{Provider: "zcode", Name: "ZCode", User: "a@qq.com", Windows: w}}
	shown, named := seatCards([]SubscriptionQuota{plan}, subs)
	if len(shown) != 0 || named[0].Seat != "某某·GLM高级版" || subs[0].Seat != "" {
		t.Fatalf("shown %+v named %+v subs %+v", shown, named, subs)
	}
	if got := withAccountNames(named); got[0].Alias != "某某·GLM高级版" {
		t.Fatalf("alias %q", got[0].Alias)
	}
	// a provider of several keys names the key too
	plan.User = "张三"
	if _, named := seatCards([]SubscriptionQuota{plan}, subs); named[0].Seat != "某某·GLM高级版 · 张三" {
		t.Fatalf("seat %q", named[0].Seat)
	}
	// the remembered seat survives a restart (seats.json)
	seatMemo.Lock()
	seatMemo.m = nil
	seatMemo.Unlock()
	failed := []SubscriptionQuota{{Provider: "zcode", User: "A@qq.com", Error: "not JSON"}}
	if shown, named := seatCards([]SubscriptionQuota{plan}, failed); len(shown) != 0 || named[0].Seat == "" {
		t.Fatalf("after a restart: shown %+v named %+v", shown, named)
	}
}

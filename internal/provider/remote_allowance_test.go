package provider

import (
	"context"
	"testing"
	"time"
)

// #6708 (zzh11223311 on X): routing weighs a Remote magpie by the cards
// it tells, as last read here. Each window is read as the remote's
// routing reads it (its span, the model it alone counts), the account
// with the most left for the model when the remote has several, and
// nothing is asked of the remote again within remoteCardsFor. A remote
// too old to tell what routing reads isn't weighed.
func TestRemoteAllowance(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	claude := func(user string, week float64, renews time.Duration) SubscriptionQuota {
		return SubscriptionQuota{Provider: "claude", Name: "Claude Code", User: user, Kind: "subscription", Windows: []QuotaWindow{
			{Name: "5 hours", Used: 10, ResetsAt: at(3 * time.Hour), Span: 5 * time.Hour},
			{Name: "7 days", Used: week, ResetsAt: at(renews), Span: 7 * 24 * time.Hour},
			{Name: "7 days · Opus", Used: 95, ResetsAt: at(renews), Span: 7 * 24 * time.Hour, Model: "opus"},
			{Name: "Extra usage", Used: 0, Aside: true},
		}}
	}
	cards := []SubscriptionQuota{
		claude("a@x.com", 51, 117*time.Hour),
		claude("b@x.com", 80, 20*time.Hour),
		// a card it has from a magpie behind it, by its whole path
		{Provider: "home/codex", Name: "Codex · Home", User: "c@x.com", Kind: "subscription", Via: []string{"home-self"},
			Windows: []QuotaWindow{{Name: "Week", Used: 30, ResetsAt: at(48 * time.Hour)}}},
	}

	t.Run("old remote", func(t *testing.T) {
		peer := &fakePeer{cards: cards} // no Routing told
		remoteQuotaHome(t, peer)
		RemoteCards(context.Background())
		p, err := Find("office")
		if err != nil {
			t.Fatal(err)
		}
		if a, _, ok := RemoteAllowance(*p, "claude/claude-sonnet-5-5", true); ok {
			t.Errorf("a card with no routing facts is weighed: %+v", a)
		}
	})

	peer := &fakePeer{cards: ToldRouting(cards)}
	remoteQuotaHome(t, peer)
	t.Cleanup(func() { endReadsOut(t, "after the test") })
	p, err := Find("office")
	if err != nil {
		t.Fatal(err)
	}
	// not asked to read: nothing is sent
	if _, _, ok := RemoteAllowance(*p, "claude/claude-sonnet-5-5", false); ok {
		t.Fatal("known before any read")
	}
	endReadsOut(t, "after a weighing that doesn't ask")
	peer.Lock()
	asked := peer.gets
	peer.Unlock()
	if asked != 0 {
		t.Fatalf("the remote was asked %d times by a weighing that doesn't ask", asked)
	}
	// nothing read yet: not known, never waited for; the read it starts
	// in the background makes it known
	if _, _, ok := RemoteAllowance(*p, "claude/claude-sonnet-5-5", true); ok {
		t.Fatal("known before any read")
	}
	var a Allowance
	var inner string
	for deadline := time.Now().Add(20 * time.Second); ; {
		var ok bool
		if a, inner, ok = RemoteAllowance(*p, "claude/claude-sonnet-5-5", true); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the remote's cards were never read for routing")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if inner != "claude-sonnet-5-5" {
		t.Errorf("inner model %q", inner)
	}
	// a@x.com, the one with most left; the Opus week and the extra usage
	// don't count Sonnet
	if u, renews := a.For(inner, now); u != 51 || len(renews) != 2 || !renews[0].Equal(*at(117 * time.Hour)) {
		t.Errorf("Sonnet: used %v, renews %v", u, renews)
	}
	// the five hours is a rate cap, not the week: the pace goes by the week
	if _, due := a.Pace(inner, now); !due.Equal(*at(117 * time.Hour)) {
		t.Errorf("pace due %v, want the week's renewal", due)
	}
	if a, _, ok := RemoteAllowance(*p, "claude/claude-opus-5", true); !ok {
		t.Error("Opus not known")
	} else if u, _ := a.For("claude-opus-5", now); u != 95 {
		t.Errorf("Opus used %v, want 95", u)
	}
	// a third magpie's card, its span named by the window's name
	if a, inner, ok := RemoteAllowance(*p, "home/codex/gpt-5.6-sol", true); !ok || inner != "gpt-5.6-sol" {
		t.Errorf("home/codex: %v %q", ok, inner)
	} else if _, due := a.Pace(inner, now); !due.Equal(*at(48 * time.Hour)) {
		t.Errorf("home/codex pace due %v", due)
	}
	// a provider the remote tells no card of, and a group there
	for _, m := range []string{"deepseek/deepseek-chat", "auto-claude"} {
		if _, _, ok := RemoteAllowance(*p, m, true); ok {
			t.Errorf("%s known", m)
		}
	}
	peer.Lock()
	gets := peer.gets
	peer.Unlock()
	if gets != 1 {
		t.Errorf("the remote was asked %d times, want 1 within remoteCardsFor", gets)
	}
}

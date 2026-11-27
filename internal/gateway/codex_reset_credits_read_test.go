package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// shangyuan0818 on X: the week used up over an hour, the turns going on
// on the account's credits, and no reset spent by itself. A turn magpie
// relays spends it first (resetBeforeCredits) with the account's usage
// read from the backend as magpie reads it, not handed to the routing:
// relayed on Codex's own sign-in, and routed to it past one held.
func TestCodexResetBeforeCreditsAsRead(t *testing.T) {
	old := allowances
	allowances = provider.Allowances // the accounts' windows as read, here
	t.Cleanup(func() { allowances = old })
	read := func(t *testing.T, users ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if !provider.ReadUsageNow(ctx, "codex", users...) {
			t.Fatal("never read")
		}
		t.Cleanup(func() {
			for _, u := range users {
				provider.StaleAllowance("codex", u)
			}
		})
		for deadline := time.Now().Add(5 * time.Second); !allowances("codex")[users[0]].WeekUsedUp(time.Now()); time.Sleep(10 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the week never read used up: %+v", allowances("codex"))
			}
		}
	}
	t.Run("relayed", func(t *testing.T) {
		codexSignedIn(t)
		b := newResetBackend(t, "acct-1")
		b.credits["acct-1"] = true
		if err := provider.SetCodexAutoResetCase("me@example.com", provider.ResetCaseWeek, true); err != nil {
			t.Fatal(err)
		}
		read(t, "me@example.com")
		srv := New()
		code, body := resetPost(t, srv)
		if tried, spent := b.seen(); code != 200 || !strings.Contains(body, "pong") || spent != "acct-1" {
			t.Fatalf("%d %s tried %q spent %q", code, body, tried, spent)
		}
	})
	t.Run("routed", func(t *testing.T) {
		codexSignedIn(t, "spare@example.com")
		b := newResetBackend(t, "acct-1", "acct-2")
		b.held["acct-2"] = 0
		b.credits["acct-1"] = true
		if err := provider.SetCodexAutoResetCase("me@example.com", provider.ResetCaseWeek, true); err != nil {
			t.Fatal(err)
		}
		read(t, "me@example.com", "spare@example.com")
		srv := New()
		code, body := resetPost(t, srv)
		if tried, spent := b.seen(); code != 200 || !strings.Contains(body, "pong") || spent != "acct-1" {
			t.Fatalf("%d %s tried %q spent %q", code, body, tried, spent)
		}
	})
}

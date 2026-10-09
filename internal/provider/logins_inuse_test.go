package provider

import (
	"testing"
	"time"
)

// noRoom is inUseOf's room for a caller with no allowances to weigh: the
// account in use stays whichever it has left, as it did before the menu
// bar's card was made to follow the gateway.
func noRoom(string) (bool, bool) { return false, false }

// The account in use, as the menu bar's "account in use" card follows it:
// the one the agent is signed in to, unless paused, else the first on.
func TestInUseOf(t *testing.T) {
	for _, c := range []struct {
		ls   []Login
		want string
	}{
		{[]Login{{User: "a", On: true}, {User: "b", Active: true, On: true}}, "b"},
		{[]Login{{User: "b", Active: true, On: true, Paused: true}, {User: "c"}, {User: "d", On: true}}, "d"},
		{[]Login{{User: "c"}, {User: "e", On: true}}, "e"},
		{[]Login{{User: "c"}}, ""},
		{nil, ""},
	} {
		if got := inUseOf(c.ls, noRoom); got != c.want {
			t.Errorf("%+v: %q, want %q", c.ls, got, c.want)
		}
	}
}

// A provider's account A is spent and the gateway has moved its requests to
// B, but magpie has not switched the sign-in yet (it does that only every
// few minutes). The card must show B's remaining allowance, not A's already
// gone (a user report: the menu bar kept showing the exhausted account). So
// the account in use follows the gateway to the next one on with room —
// but only where the allowance is actually known to be spent: a single
// account, an unknown allowance, or a failed read must still show the one
// the agent is signed in to, never another.
func TestInUseOfFollowsGatewayWhenSpent(t *testing.T) {
	// room reports which accounts have used up their allowance.
	spend := func(spent ...string) func(string) (bool, bool) {
		return func(user string) (bool, bool) {
			for _, s := range spent {
				if s == user {
					return true, true // known and spent
				}
			}
			return true, false // known, has room
		}
	}
	unknown := func(string) (bool, bool) { return false, false }

	t.Run("signed-in account spent, another on has room", func(t *testing.T) {
		ls := []Login{
			{User: "a", Active: true, On: true},
			{User: "b", On: true},
		}
		if got := inUseOf(ls, spend("a")); got != "b" {
			t.Errorf("got %q, want b: the gateway goes to b, the card should follow", got)
		}
	})

	t.Run("signed-in account spent, none has room", func(t *testing.T) {
		ls := []Login{
			{User: "a", Active: true, On: true},
			{User: "b", On: true},
		}
		if got := inUseOf(ls, spend("a", "b")); got != "a" {
			t.Errorf("got %q, want a: with no account left to show, keep the one signed in", got)
		}
	})

	t.Run("allowance unknown, keep the signed-in account", func(t *testing.T) {
		// This is the "read failed / not read yet" path: a card that fell
		// back to another account would show an allowance the user is not
		// actually on.
		ls := []Login{
			{User: "a", Active: true, On: true},
			{User: "b", On: true},
		}
		if got := inUseOf(ls, unknown); got != "a" {
			t.Errorf("got %q, want a: an unknown allowance is never taken for spent", got)
		}
	})

	t.Run("single account spent, still shows it", func(t *testing.T) {
		// The only account, however drained, is the one the agent is on:
		// there is no other to follow, and the card would be wrong to jump.
		ls := []Login{{User: "a", Active: true, On: true}}
		if got := inUseOf(ls, spend("a")); got != "a" {
			t.Errorf("got %q, want a: a single account is never swapped out", got)
		}
	})

	t.Run("paused account not rescued by room", func(t *testing.T) {
		// The first loop only considers Active/first and not paused; a
		// paused signed-in account already falls through to the On pass,
		// which room does not reorder.
		ls := []Login{
			{User: "a", Active: true, On: true, Paused: true},
			{User: "b", On: true},
		}
		if got := inUseOf(ls, spend("b")); got != "b" {
			t.Errorf("got %q, want b", got)
		}
	})

	t.Run("a lapsed spare is not taken for room", func(t *testing.T) {
		// A signed-out account can take no request (NextLogin skips one
		// lapsed), so the card must not follow it either.
		ls := []Login{
			{User: "a", Active: true, On: true},
			{User: "b", On: true, Lapsed: "signed out"},
		}
		if got := inUseOf(ls, spend("a")); got != "a" {
			t.Errorf("got %q, want a: the lapsed spare has no room to follow", got)
		}
	})

	t.Run("a spare that is itself the signed-in one is not chosen twice", func(t *testing.T) {
		// Only other accounts can take over: skip the spent user by name, so
		// a same account marked twice doesn't loop back to itself.
		ls := []Login{
			{User: "a", Active: true, On: true},
			{User: "b", On: true},
		}
		if got := inUseOf(ls, spend("a")); got != "b" {
			t.Errorf("got %q, want b", got)
		}
	})
}

// loginRoom weighs an account against the share the gateway's routing counts
// it spent at, reading the allowances the gateway routes by. inUseOf relies
// on it to decide whether the account in use still has room, so it must
// agree with the gateway on which accounts are out: a full one is spent, one
// with room is not, and one never read is unknown — never taken for spent, so
// the card never jumps off an account whose allowance it hasn't seen.
func TestLoginRoom(t *testing.T) {
	signIn(t)
	// Inject the allowances as the gateway would see them: Codex's signed-in
	// account at the vendor's edge, a spare with room, and an account magpie
	// has no reading for. Marked fresh so Allowances answers from them rather
	// than refetching over a test's fake home.
	usedCache.Lock()
	if usedCache.m == nil {
		usedCache.m, usedCache.at, usedCache.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
	}
	usedCache.m["codex"] = map[string]Allowance{
		"me@example.com":    {{Used: 100, Span: 5 * time.Hour}},
		"spare@example.com": {{Used: 10, Span: 5 * time.Hour}},
	}
	usedCache.at["codex"] = time.Now()
	usedCache.Unlock()
	t.Cleanup(func() {
		usedCache.Lock()
		usedCache.m, usedCache.at, usedCache.loading = map[string]map[string]Allowance{}, map[string]time.Time{}, map[string]chan struct{}{}
		usedCache.stale = nil
		usedCache.Unlock()
	})

	room := loginRoom("codex")
	if known, spent := room("me@example.com"); !known || !spent {
		t.Errorf("me: known=%v spent=%v, want both: a full account is spent", known, spent)
	}
	if known, spent := room("spare@example.com"); !known || spent {
		t.Errorf("spare: known=%v spent=%v, want known and not spent", known, spent)
	}
	if known, spent := room("nobody@example.com"); known || spent {
		t.Errorf("nobody: known=%v spent=%v, want unknown: an unread allowance is never spent", known, spent)
	}
}

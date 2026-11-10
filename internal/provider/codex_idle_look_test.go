package provider

import (
	"testing"
	"time"
)

// TestCodexIdleLooksLess: while Codex has written no session in
// codexIdleAfter, an account not near out is read every
// codexIdleLookEvery, not loginSwitchEvery; near out it is still read
// every codexNearOutEvery; and a session written again makes the next
// full look read it at once.
func TestCodexIdleLooksLess(t *testing.T) {
	old := codexIdle
	t.Cleanup(func() { codexIdle = old })
	idle := true
	codexIdle = func(time.Time) bool { return idle }
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)

	if got, was := codexEveryAt(loginSwitchEvery, now); got != codexIdleLookEvery || !was {
		t.Fatalf("idle, not near out: %v %v, want %v true", got, was, codexIdleLookEvery)
	}
	if got, was := codexEveryAt(codexNearOutEvery, now); got != codexNearOutEvery || was {
		t.Fatalf("idle, near out: %v %v, want %v false", got, was, codexNearOutEvery)
	}
	next := now.Add(codexIdleLookEvery)
	if codexDue(now.Add(loginSwitchEvery), next, true) {
		t.Fatal("idle still: read again before codexIdleLookEvery")
	}
	if !codexDue(next, next, true) {
		t.Fatal("not read at codexIdleLookEvery")
	}

	idle = false
	if got, was := codexEveryAt(loginSwitchEvery, now); got != loginSwitchEvery || was {
		t.Fatalf("in use: %v %v, want %v false", got, was, loginSwitchEvery)
	}
	if !codexDue(now.Add(loginSwitchEvery), next, true) {
		t.Fatal("a session written again doesn't have the next look read the account")
	}
	if codexDue(now.Add(time.Minute), now.Add(loginSwitchEvery), false) {
		t.Fatal("in use: read before its loginSwitchEvery")
	}
}

// TestCodexIdleUnreadableIsInUse: a sessions folder that can't be read
// counts as written, so the account is read every loginSwitchEvery.
func TestCodexIdleUnreadableIsInUse(t *testing.T) {
	old := codexSessionsQuiet
	t.Cleanup(func() { codexSessionsQuiet = old })
	codexSessionsQuiet = func(string, time.Time, time.Duration) bool { return false }
	if got, was := codexEveryAt(loginSwitchEvery, time.Now()); got != loginSwitchEvery || was {
		t.Fatalf("%v %v, want %v false", got, was, loginSwitchEvery)
	}
}

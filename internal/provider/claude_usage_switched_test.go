package provider

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// Claude Code's /usage tells of the account Claude Code is signed in to as
// it runs. Moved to another account before or while it ran (a switch,
// "Make first"), its reading is that one's, never kept as the account it
// was asked for (nil_1024: A, never used, shown as spent as B, made first).
func TestClaudeUsageOfAnotherAccountNotKept(t *testing.T) {
	home := claudeHome(t)
	cred := claudeSignIn(t, home, time.Now().Add(time.Hour))
	profile := filepath.Join(home, ".claude.json")
	signedIn := func(email string) {
		writeFile(t, profile, map[string]any{"oauthAccount": map[string]any{"emailAddress": email, "accountUuid": "u-" + email}})
		writeFile(t, cred, map[string]any{"claudeAiOauth": map[string]any{"accessToken": "tok-" + email, "refreshToken": "r-" + email,
			"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "max"}})
		forgetAccountCaches()
	}
	signedIn("a@example.com")

	var out atomic.Value
	out.Store("")
	runs := fakeClaudeUsage(t, &out, nil)
	spent := "Current session: 100% used · resets " + soon(1) + " at 3:30pm (UTC)\nCurrent week (all models): 60% used · resets " + soon(3) + " at 2pm (UTC)\n"
	fine := "Current session: 5% used · resets " + soon(1) + " at 3:30pm (UTC)\nCurrent week (all models): 20% used · resets " + soon(3) + " at 2pm (UTC)\n"
	switchMidway := false
	UsageClaudeVia(func(context.Context) (string, error) {
		runs.Add(1)
		if switchMidway {
			// Claude Code moved to B as its /usage ran: it read B's
			signedIn("b@example.com")
			return spent, nil
		}
		return fine, nil
	})
	ctx := context.Background()
	// as if the last reading were long enough ago to read again
	later := func() {
		claudeUsage.Lock()
		for k, e := range claudeUsage.m {
			e.tried = e.tried.Add(-claudeAskFloor)
			claudeUsage.m[k] = e
		}
		claudeUsage.Unlock()
		AskClaudeUsage()
	}

	AskClaudeUsage()
	if ws, err := claudeWindows(ctx, "a@example.com", true); err != nil || len(ws) == 0 || ws[0].Used != 5 {
		t.Fatalf("A read on A: %v %+v", err, ws)
	}

	switchMidway = true
	later()
	ws, _ := claudeWindows(ctx, "a@example.com", true)
	if len(ws) == 0 || ws[0].Used != 5 {
		t.Fatalf("A after a reading taken as Claude Code moved to B: %+v (B's 100%% kept as A's?)", ws)
	}
	if runs.Load() != 2 {
		t.Fatalf("%d runs, want 2", runs.Load())
	}

	// Claude Code on B already: A, asked as the account it is on by a
	// caller that looked before the switch, isn't read on B's sign-in
	switchMidway = false
	later()
	if ws, _ := claudeWindows(ctx, "a@example.com", true); len(ws) == 0 || ws[0].Used != 5 || runs.Load() != 2 {
		t.Fatalf("A read with Claude Code on B: %+v, %d runs", ws, runs.Load())
	}
	// B itself is read
	if ws, err := claudeWindows(ctx, "b@example.com", true); err != nil || len(ws) == 0 || ws[0].Used != 5 || runs.Load() != 3 {
		t.Fatalf("B: %v %+v, %d runs", err, ws, runs.Load())
	}
}

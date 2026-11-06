package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A saved account's card promises to run Claude Code's /usage, even when
// Claude Code itself is signed in to another account (#1630, hhh2210).
func TestRefreshSavedClaudeUsage(t *testing.T) {
	claudeThree(t)
	var text atomic.Value
	text.Store("Current session: 17% used\nCurrent week (all models): 38% used")
	runs := fakeClaudeUsage(t, &text, nil)
	var expectSaved atomic.Bool
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		runs.Add(1)
		if expectSaved.Load() {
			if dir != claudeAccountDir("max@x.com") {
				return "", errors.New("/usage ran in another account's directory")
			}
			c, ok := readClaudeDir(dir)
			if !ok || c.OAuth.AccessToken != "tok-max@x.com" {
				return "", errors.New("/usage received another account's sign-in")
			}
		}
		return text.Load().(string), nil
	})
	ctx := context.Background()
	before := LoginUsage(ctx, "claude")
	if before["max@x.com"].Error != errClaudeSaved.Error() {
		t.Fatalf("unasked saved account: %+v", before["max@x.com"])
	}
	// the Usage page opened, `magpie quota`: the user asking, but not for
	// this account, and each saved account would start a Claude Code
	beforeRuns := runs.Load()
	AskClaudeUsage()
	if q := LoginUsage(Asked(ctx), "claude")["max@x.com"]; q.Error != errClaudeSaved.Error() || runs.Load() != beforeRuns {
		t.Fatalf("an ask for every card ran a saved account: %+v, %d runs", q, runs.Load()-beforeRuns)
	}
	beforeRuns = runs.Load()
	expectSaved.Store(true)
	RefreshUsage(ctx, "claude", "max@x.com")
	after := LoginUsage(ctx, "claude")
	q := after["max@x.com"]
	if q.Error != "" || len(q.Windows) != 2 || q.Windows[0].Used != 17 || q.Windows[1].Used != 38 || q.ReadAt == nil {
		t.Fatalf("saved account's refresh read no allowance: %+v", q)
	}
	if runs.Load() != beforeRuns+1 {
		t.Fatalf("refresh ran Claude Code %d times, want once", runs.Load()-beforeRuns)
	}
	if after["team@x.com · Acme"].Error != before["team@x.com · Acme"].Error {
		t.Fatal("refresh changed another saved account's card")
	}
	active := ""
	for _, l := range Logins("claude") {
		if l.Active {
			active = l.User
		}
	}
	if active != "pro@x.com" {
		t.Fatalf("refresh changed Claude Code's own account to %q", active)
	}

	// Rapid clicks still respect the per-account floor. A later explicit
	// refresh reads anew, while a background read keeps what was read.
	RefreshUsage(ctx, "claude", "max@x.com")
	if runs.Load() != beforeRuns+1 {
		t.Fatal("rapid refresh bypassed the per-account floor")
	}
	claudeUsage.Lock()
	e := claudeUsage.m["max@x.com"]
	e.tried = e.tried.Add(-time.Hour)
	claudeUsage.m["max@x.com"] = e
	claudeUsage.Unlock()
	if ws, err := claudeWindows(ctx, "max@x.com", false); err != nil || len(ws) != 2 || runs.Load() != beforeRuns+1 {
		t.Fatalf("background read ran a saved account: %v %+v", err, ws)
	}
	text.Store("Current session: 21% used\nCurrent week (all models): 40% used")
	RefreshUsage(ctx, "claude", "max@x.com")
	q = LoginUsage(ctx, "claude")["max@x.com"]
	if q.Error != "" || len(q.Windows) != 2 || q.Windows[0].Used != 21 || runs.Load() != beforeRuns+2 {
		t.Fatalf("later refresh kept an old reading: %+v", q)
	}
}

// The tray panel's top refresh of the allowances, and the Usage page's
// Refresh (AskEveryClaudeUsage, &all=1), read every saved Claude account
// anew, as each card's refresh does (#5380: the panel said "Allowances
// refreshed" while each saved account kept a reading half an hour old).
// Opening the page and an unasked read still start no saved account's
// Claude Code, and the 30-second floor holds.
func TestRefreshEveryReadsSavedClaude(t *testing.T) {
	claudeThree(t)
	var text atomic.Value
	text.Store("Current session: 9% used\nCurrent week (all models): 97% used")
	runs := fakeClaudeUsage(t, &text, nil)
	var mu sync.Mutex
	ran := map[string]int{}
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		runs.Add(1)
		mu.Lock()
		ran[dir]++
		mu.Unlock()
		return text.Load().(string), nil
	})
	runsIn := func(user string) int {
		mu.Lock()
		defer mu.Unlock()
		return ran[claudeAccountDir(user)]
	}
	var saved []string
	for _, l := range Logins("claude") {
		if !l.Active {
			saved = append(saved, l.User)
		}
	}
	if len(saved) != 2 {
		t.Fatalf("saved accounts: %v", saved)
	}
	age := func(user string) {
		claudeUsage.Lock()
		e := claudeUsage.m[strings.ToLower(user)]
		e.tried = e.tried.Add(-time.Hour)
		claudeUsage.m[strings.ToLower(user)] = e
		claudeUsage.Unlock()
	}
	ctx := context.Background()

	// the page opened: asked, but no saved account's Claude Code
	AskClaudeUsage()
	LoginUsage(Asked(ctx), "claude")
	for _, user := range saved {
		if n := runsIn(user); n != 0 {
			t.Fatalf("opening ran saved %s %d times", user, n)
		}
	}

	// the top refresh: every saved account read, each once, in its own
	// directory
	AskEveryClaudeUsage()
	got := LoginUsage(Asked(ctx), "claude")
	for _, user := range saved {
		q := got[user]
		if q.Error != "" || len(q.Windows) != 2 || q.Windows[0].Used != 9 || q.Windows[1].Used != 97 || q.ReadAt == nil {
			t.Fatalf("top refresh read no allowance of saved %s: %+v", user, q)
		}
		if n := runsIn(user); n != 1 {
			t.Fatalf("top refresh ran saved %s %d times, want once", user, n)
		}
	}

	// Claude Code used meanwhile; past the floor, nobody asking, and the
	// page opened again: the readings stand
	text.Store("Current session: 36% used\nCurrent week (all models): 100% used")
	for _, user := range saved {
		age(user)
	}
	for _, user := range saved {
		if ws, err := claudeWindows(ctx, user, false); err != nil || len(ws) != 2 || ws[0].Used != 9 || runsIn(user) != 1 {
			t.Fatalf("unasked read ran saved %s: %v %+v", user, err, ws)
		}
	}
	AskClaudeUsage()
	for _, user := range saved {
		if q := LoginUsage(Asked(ctx), "claude")[user]; q.Windows[0].Used != 9 || runsIn(user) != 1 {
			t.Fatalf("opening again ran saved %s: %+v", user, q)
		}
	}

	// the top refresh again reads the new figures
	AskEveryClaudeUsage()
	got = LoginUsage(Asked(ctx), "claude")
	for _, user := range saved {
		if q := got[user]; q.Error != "" || q.Windows[0].Used != 36 || q.Windows[1].Used != 100 || runsIn(user) != 2 {
			t.Fatalf("second top refresh kept an old reading of %s: %+v, %d runs", user, q, runsIn(user))
		}
	}

	// rapid clicks keep to the per-account floor: one account past it is
	// read, the other not
	age(saved[0])
	AskEveryClaudeUsage()
	LoginUsage(Asked(ctx), "claude")
	if runsIn(saved[0]) != 3 || runsIn(saved[1]) != 2 {
		t.Fatalf("rapid top refresh: %s ran %d, %s ran %d; want 3 and 2", saved[0], runsIn(saved[0]), saved[1], runsIn(saved[1]))
	}

	// an ask the floor put off isn't run by a read long after it
	AskEveryClaudeUsage() // saved[1] is inside its floor: not read
	LoginUsage(Asked(ctx), "claude")
	claudeEveryAt.Store(time.Now().Add(-claudeEveryFor - time.Second).UnixNano())
	age(saved[1])
	if _, err := claudeWindows(ctx, saved[1], false); err != nil || runsIn(saved[1]) != 2 {
		t.Fatalf("a timed read ran saved %s for a refresh long gone: %v, %d runs", saved[1], err, runsIn(saved[1]))
	}
}

// Claude Code run in a saved account's directory refreshes the sign-in it
// keeps there when it is due, spending the old refresh token. magpie keeps
// the new one at once, and doesn't hand the account to Claude Code itself
// while that run may still be refreshing it: Claude Code's own copy would
// be the spent one.
func TestSavedClaudeUsageKeepsRotatedSignIn(t *testing.T) {
	claudeThree(t)
	home := os.Getenv("HOME")
	var text atomic.Value
	text.Store("Current session: 17% used")
	fakeClaudeUsage(t, &text, nil)
	running, release := make(chan string, 1), make(chan struct{})
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		if dir == "" {
			return text.Load().(string), nil
		}
		running <- dir
		<-release
		// as Claude Code refreshes: the new sign-in written where it
		// keeps it for this directory
		writeFile(t, filepath.Join(dir, ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{
			"accessToken": "tok-rotated", "refreshToken": "r-rotated",
			"expiresAt": time.Now().Add(8 * time.Hour).UnixMilli(), "subscriptionType": "max"}})
		return text.Load().(string), nil
	})
	var freed sync.Once
	free := func() { freed.Do(func() { close(release) }) }
	t.Cleanup(free) // a failing test doesn't leave the run waiting
	refreshed := make(chan struct{})
	go func() {
		defer close(refreshed)
		RefreshUsage(context.Background(), "claude", "max@x.com")
	}()
	var dir string
	select {
	case dir = <-running:
	case <-refreshed:
		t.Fatal("the card's refresh ran no /usage in the account's directory")
	}
	switched := make(chan error, 1)
	go func() { switched <- SwitchLogin("claude", "max@x.com") }()
	select {
	case err := <-switched:
		t.Fatalf("switched to the account while its /usage ran (%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	free()
	<-refreshed
	if err := <-switched; err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil || !strings.Contains(string(b), "r-rotated") {
		t.Fatalf("Claude Code was handed the spent sign-in: %s %v", b, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the account's directory still holds its sign-in after the switch: %v", err)
	}
}

// Without a switch, the rotated sign-in is the saved account's in
// logins.json as soon as the run ends, not when the account is next used.
func TestSavedClaudeUsageSavesRotatedSignIn(t *testing.T) {
	claudeThree(t)
	var text atomic.Value
	text.Store("Current session: 17% used")
	fakeClaudeUsage(t, &text, nil)
	UsageClaudeVia(func(_ context.Context, dir string) (string, error) {
		if dir != "" {
			writeFile(t, filepath.Join(dir, ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{
				"accessToken": "tok-rotated", "refreshToken": "r-rotated",
				"expiresAt": time.Now().Add(8 * time.Hour).UnixMilli(), "subscriptionType": "max"}})
		}
		return text.Load().(string), nil
	})
	RefreshUsage(context.Background(), "claude", "max@x.com")
	loginsMu.Lock()
	ls := readLogins()
	loginsMu.Unlock()
	for _, l := range ls {
		if l.Agent == "claude" && l.User == "max@x.com" {
			if c, ok := parseClaudeCredentials(l.Auth); !ok || c.OAuth.RefreshToken != "r-rotated" {
				t.Fatalf("logins.json kept the spent sign-in: %s", l.Auth)
			}
			return
		}
	}
	t.Fatal("max@x.com isn't saved")
}

// #5443 (prgrmrwy): the gateway's POST /v1/magpie/quotas/refresh with no
// card named (another magpie's refresh of this one's card, or an app with
// a gateway key) reads every card again (ReadAllCards), and a saved,
// inactive Claude account kept the reading it had: ReadAllCards asked
// AskUsage alone, which runs no saved account's Claude Code. It is the
// user's refresh of every allowance, as the Usage page's Refresh is, so
// each saved account's /usage runs too, side by side, and the cards the
// gateway answers (CachedCards) carry the new figures. The floor holds.
func TestReadAllCardsReadsSavedClaude(t *testing.T) {
	claudeThree(t)
	var text atomic.Value
	text.Store("Current session: 0% used\nCurrent week (all models): 10% used")
	fakeClaudeUsage(t, &text, nil)
	var saved []string
	for _, l := range Logins("claude") {
		if !l.Active {
			saved = append(saved, l.User)
		}
	}
	if len(saved) != 2 {
		t.Fatalf("saved accounts: %v", saved)
	}
	var mu sync.Mutex
	ran := map[string]int{}
	// while held, a saved account's run waits for the other's to start:
	// read one after another, the first gives up waiting
	var held atomic.Bool
	both := make(chan struct{})
	var in atomic.Int32
	var alone atomic.Bool
	UsageClaudeVia(func(ctx context.Context, dir string) (string, error) {
		if dir != "" {
			mu.Lock()
			ran[dir]++
			mu.Unlock()
			if held.Load() {
				if in.Add(1) == 2 {
					close(both)
				}
				select {
				case <-both:
				case <-time.After(5 * time.Second):
					alone.Store(true)
				case <-ctx.Done():
					alone.Store(true)
				}
			}
		}
		return text.Load().(string), nil
	})
	runsIn := func(user string) int {
		mu.Lock()
		defer mu.Unlock()
		return ran[claudeAccountDir(user)]
	}
	card := func(user string) SubscriptionQuota {
		for _, q := range CachedCards(time.Now()) {
			if q.Provider == "claude" && strings.EqualFold(q.User, user) {
				return q
			}
		}
		return SubscriptionQuota{}
	}
	age := func(user string) {
		claudeUsage.Lock()
		e := claudeUsage.m[strings.ToLower(user)]
		e.tried = e.tried.Add(-time.Hour)
		claudeUsage.m[strings.ToLower(user)] = e
		claudeUsage.Unlock()
	}
	ctx := context.Background()
	// each saved account read once, by its own card, as the reporter's
	// had been hours before
	for _, user := range saved {
		RefreshUsage(ctx, "claude", user)
		if q := card(user); q.Error != "" || len(q.Windows) != 2 || q.Windows[1].Used != 10 || runsIn(user) != 1 {
			t.Fatalf("card refresh of saved %s: %+v, %d runs", user, q, runsIn(user))
		}
		age(user)
	}

	text.Store("Current session: 45% used\nCurrent week (all models): 60% used")
	held.Store(true)
	ReadAllCards(ctx)
	held.Store(false)
	for _, user := range saved {
		q := card(user)
		if runsIn(user) != 2 {
			t.Fatalf("the gateway's refresh of every card ran saved %s's /usage %d times, want once more", user, runsIn(user)-1)
		}
		if q.Error != "" || len(q.Windows) != 2 || q.Windows[0].Used != 45 || q.Windows[1].Used != 60 {
			t.Fatalf("the gateway's refresh of every card kept saved %s's old reading: %+v", user, q)
		}
	}
	if alone.Load() {
		t.Fatal("the saved accounts' /usage ran one after another, not side by side")
	}

	// pressed again at once: the per-account floor holds
	ReadAllCards(ctx)
	for _, user := range saved {
		if n := runsIn(user); n != 2 {
			t.Fatalf("a second refresh inside the floor ran saved %s again (%d runs)", user, n)
		}
	}
}

// A gateway key told only some cards has each of them read again on a
// refresh of them all (RefreshEach), side by side: one after another, a
// few saved Claude accounts' /usage outlasted the gateway's 30 seconds
// (#5443).
func TestRefreshEachReadsSideBySide(t *testing.T) {
	claudeThree(t)
	var text atomic.Value
	text.Store("Current session: 12% used\nCurrent week (all models): 34% used")
	fakeClaudeUsage(t, &text, nil)
	var saved []SubscriptionQuota
	for _, l := range Logins("claude") {
		if !l.Active {
			saved = append(saved, SubscriptionQuota{Provider: "claude", User: l.User})
		}
	}
	if len(saved) != 2 {
		t.Fatalf("saved accounts: %v", saved)
	}
	both := make(chan struct{})
	var in atomic.Int32
	var alone atomic.Bool
	UsageClaudeVia(func(ctx context.Context, dir string) (string, error) {
		if dir != "" {
			if in.Add(1) == 2 {
				close(both)
			}
			select {
			case <-both:
			case <-time.After(5 * time.Second):
				alone.Store(true)
			case <-ctx.Done():
				alone.Store(true)
			}
		}
		return text.Load().(string), nil
	})
	RefreshEach(context.Background(), saved)
	if alone.Load() || in.Load() != 2 {
		t.Fatalf("saved accounts read one after another: %d runs, alone %v", in.Load(), alone.Load())
	}
	for _, s := range saved {
		q := LoginUsage(context.Background(), "claude")[s.User]
		if q.Error != "" || len(q.Windows) != 2 || q.Windows[0].Used != 12 {
			t.Fatalf("saved %s not read: %+v", s.User, q)
		}
	}
}

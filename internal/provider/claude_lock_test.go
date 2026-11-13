package provider

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/claudelock"
	"github.com/yetone/magpie/internal/filelock"
)

// claudeLockAccounts signs Claude Code in to b, with a saved beside it,
// and answers the path of Claude Code's credentials.
func claudeLockAccounts(t *testing.T) (home, cred string) {
	t.Helper()
	home = claudeHome(t)
	cred = claudeSignIn(t, home, time.Now().Add(time.Hour))
	profile := filepath.Join(home, ".claude.json")
	writeFile(t, profile, map[string]any{"oauthAccount": map[string]any{"emailAddress": "a@example.com", "accountUuid": "u-a"}})
	rememberLogins(true)
	claudeWriteB(t, cred, "b")
	writeFile(t, profile, map[string]any{"oauthAccount": map[string]any{"emailAddress": "b@example.com", "accountUuid": "u-b"}})
	forgetClaudeCredential()
	rememberLogins(true)
	old := ClaudeHandedOver
	t.Cleanup(func() { ClaudeHandedOver = old })
	ClaudeHandedOver = func(string) {}
	return home, cred
}

func claudeWriteB(t *testing.T, cred, rt string) {
	t.Helper()
	writeFile(t, cred, map[string]any{"claudeAiOauth": map[string]any{"accessToken": "sk-ant-oat01-" + rt, "refreshToken": "sk-ant-ort01-" + rt,
		"expiresAt": time.Now().Add(time.Hour).UnixMilli(), "subscriptionType": "pro"}})
}

func savedAuth(t *testing.T, user string) string {
	t.Helper()
	for _, l := range readLogins() {
		if l.Agent == "claude" && l.User == user {
			return string(l.Auth)
		}
	}
	t.Fatalf("%s not saved", user)
	return ""
}

// #4532: a switch while Claude Code refreshes b's sign-in under its
// .oauth_refresh.lock waits for the refresh, and saves b as refreshed: read
// before, b's refresh token was the one the refresh spent, and b was
// signed out of magpie on its next use.
func TestClaudeSwitchWaitsForClaudeCodesRefresh(t *testing.T) {
	for _, name := range []string{"refresh", "legacy", "config"} {
		t.Run(name, func(t *testing.T) {
			home, cred := claudeLockAccounts(t)
			lock := map[string]string{
				"refresh": filepath.Join(home, ".claude", ".oauth_refresh.lock"),
				"legacy":  filepath.Join(home, ".claude.lock"),
				"config":  filepath.Join(home, ".claude.json.lock"),
			}[name]
			if err := os.Mkdir(lock, 0o755); err != nil { // Claude Code holds it
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- SwitchLogin("claude", "a@example.com") }()
			// it waits as long as the lock is held, and touched
			for range 5 {
				select {
				case err := <-done:
					t.Fatalf("switched while Claude Code held %s: %v", filepath.Base(lock), err)
				case <-time.After(100 * time.Millisecond):
				}
				now := time.Now()
				_ = os.Chtimes(lock, now, now)
			}
			if name != "config" {
				// the refresh: a new pair written, the old refresh token spent
				claudeWriteB(t, cred, "b2")
			}
			if err := os.Remove(lock); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("the switch never went on after the lock was let go")
			}
			if name != "config" {
				if a := savedAuth(t, "b@example.com"); !strings.Contains(a, "sk-ant-ort01-b2") {
					t.Fatalf("b saved with the refresh token Claude Code spent: %s", a)
				}
			}
			b, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
			if !strings.Contains(string(b), "a@example.com") {
				t.Fatalf("Claude Code not signed in to a: %s", b)
			}
			// every lock let go
			for _, l := range []string{".claude/.oauth_refresh.lock", ".claude.lock", ".claude.json.lock"} {
				if _, err := os.Stat(filepath.Join(home, l)); !os.IsNotExist(err) {
					t.Fatalf("%s left behind: %v", l, err)
				}
			}
		})
	}
}

// A lock its holder left (not touched for longer than Claude Code's stale
// time) is taken over, as Claude Code does; one held past the wait leaves
// the sign-in as it was and says why.
func TestClaudeSwitchStaleAndHeldLocks(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		home, _ := claudeLockAccounts(t)
		lock := filepath.Join(home, ".claude", ".oauth_refresh.lock")
		if err := os.Mkdir(lock, 0o755); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-2 * time.Minute)
		_ = os.Chtimes(lock, old, old)
		done := make(chan error, 1)
		go func() { done <- SwitchLogin("claude", "a@example.com") }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("a stale lock held the switch")
		}
	})
	t.Run("held", func(t *testing.T) {
		home, cred := claudeLockAccounts(t)
		w := claudelock.Wait
		claudelock.Wait = 300 * time.Millisecond
		t.Cleanup(func() { claudelock.Wait = w })
		if err := os.Mkdir(home+"/.claude.lock", 0o755); err != nil {
			t.Fatal(err)
		}
		err := SwitchLogin("claude", "a@example.com")
		if !errors.Is(err, errClaudeLocked) {
			t.Fatalf("got %v", err)
		}
		b, _ := os.ReadFile(cred)
		if !strings.Contains(string(b), "sk-ant-ort01-b") {
			t.Fatalf("Claude Code's sign-in written over while it held its lock: %s", b)
		}
		// the first lock, taken, is let go when the second can't be
		if _, err := os.Stat(filepath.Join(home, ".claude", ".oauth_refresh.lock")); !os.IsNotExist(err) {
			t.Fatalf(".oauth_refresh.lock left behind: %v", err)
		}
	})
}

// #4532: a switch from one magpie process waits for Claude Code another
// runs in the account's own directory (the app reading its /usage while
// `magpie accounts switch` runs), and hands Claude Code the sign-in that
// run refreshed. Before, the wait was in-process only: the switch handed
// over the refresh token the run then spent.
func TestClaudeSwitchWaitsForAnotherMagpiesRun(t *testing.T) {
	claudeThree(t)
	home := os.Getenv("HOME")
	old := ClaudeHandedOver
	t.Cleanup(func() { ClaudeHandedOver = old })
	ClaudeHandedOver = func(string) {}
	dir, err := claudeSavedDir("max@x.com")
	if err != nil {
		t.Fatal(err)
	}
	// the other magpie's run, as markClaudeRun holds it there
	mark := claudeAccountDir("max@x.com") + ".run-99999-1.lock"
	unlock, held := filelock.Lock(mark, time.Second)
	if !held {
		t.Fatal("the run's lock wasn't to be had")
	}
	done := make(chan error, 1)
	go func() { done <- SwitchLogin("claude", "max@x.com") }()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("switched while another magpie ran Claude Code in the account's directory (%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	// its Claude Code refreshes the sign-in there, and ends
	writeFile(t, filepath.Join(dir, ".credentials.json"), map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "tok-rotated", "refreshToken": "r-rotated",
		"expiresAt": time.Now().Add(8 * time.Hour).UnixMilli(), "subscriptionType": "max"}})
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the switch never went on after the run ended")
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil || !strings.Contains(string(b), "r-rotated") {
		t.Fatalf("Claude Code was handed the spent sign-in: %s %v", b, err)
	}
	if m, _ := filepath.Glob(claudeRunMarks("max@x.com")); len(m) != 0 {
		t.Fatalf("run locks left behind: %v", m)
	}
}

// A run that outlasts the wait leaves Claude Code signed in as it was, and
// says why; a lock file whose run ended without removing it holds nothing.
func TestClaudeSwitchRunHeldAndLeft(t *testing.T) {
	t.Run("held", func(t *testing.T) {
		claudeThree(t)
		home := os.Getenv("HOME")
		w := claudeRunWait
		claudeRunWait = 300 * time.Millisecond
		t.Cleanup(func() { claudeRunWait = w })
		unlock, held := filelock.Lock(claudeAccountDir("max@x.com")+".run-99999-1.lock", time.Second)
		if !held {
			t.Fatal("not held")
		}
		defer unlock()
		if err := SwitchLogin("claude", "max@x.com"); !errors.Is(err, errClaudeRunning) {
			t.Fatalf("got %v", err)
		}
		b, _ := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
		if !strings.Contains(string(b), "r-pro@x.com") {
			t.Fatalf("Claude Code's sign-in written over while a run held the account: %s", b)
		}
	})
	t.Run("left", func(t *testing.T) {
		claudeThree(t)
		mark := claudeAccountDir("max@x.com") + ".run-99999-1.lock"
		if err := os.MkdirAll(filepath.Dir(mark), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mark, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := SwitchLogin("claude", "max@x.com"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(mark); !os.IsNotExist(err) {
			t.Fatalf("a left lock file kept: %v", err)
		}
	})
}

// A run in a saved account's directory holds its lock file while it runs,
// and removes it as it ends.
func TestSavedClaudeRunHoldsItsLock(t *testing.T) {
	claudeThree(t)
	var seen []string
	err := inSavedClaudeDir("max@x.com", func(string) error {
		seen, _ = filepath.Glob(claudeRunMarks("max@x.com"))
		if len(seen) == 1 {
			if unlock, held := filelock.Lock(seen[0], 50*time.Millisecond); held {
				unlock()
				t.Error("the run's lock was free while it ran")
			}
		}
		return nil
	})
	if err != nil || len(seen) != 1 {
		t.Fatalf("%v, run locks %v", err, seen)
	}
	if m, _ := filepath.Glob(claudeRunMarks("max@x.com")); len(m) != 0 {
		t.Fatalf("run locks left behind: %v", m)
	}
}

// Forgetting an account whose directory a run of another magpie process
// holds is refused (#3254): the folder and the saved sign-in stay. A lock
// file whose run ended holds nothing, and the forget goes on.
func TestClaudeForgetRefusedWhileAnotherMagpieRunsTheFolder(t *testing.T) {
	t.Run("held", func(t *testing.T) {
		claudeThree(t)
		if err := os.MkdirAll(claudeAccountDir("max@x.com"), 0o700); err != nil {
			t.Fatal(err)
		}
		w := claudeRunWait
		claudeRunWait = 300 * time.Millisecond
		t.Cleanup(func() { claudeRunWait = w })
		unlock, held := filelock.Lock(claudeAccountDir("max@x.com")+".run-99999-1.lock", time.Second)
		if !held {
			t.Fatal("not held")
		}
		defer unlock()
		if err := ForgetLogin("claude", "max@x.com"); !errors.Is(err, errClaudeRunning) {
			t.Fatalf("got %v", err)
		}
		if _, err := os.Stat(claudeAccountDir("max@x.com")); err != nil {
			t.Fatalf("the account's directory was removed under the run: %v", err)
		}
		if a := savedAuth(t, "max@x.com"); !strings.Contains(a, "r-max") {
			t.Fatalf("the saved sign-in went with the refused forget: %q", a)
		}
	})
	t.Run("ended", func(t *testing.T) {
		claudeThree(t)
		if err := os.MkdirAll(claudeAccountDir("max@x.com"), 0o700); err != nil {
			t.Fatal(err)
		}
		mark := claudeAccountDir("max@x.com") + ".run-99999-1.lock"
		if err := os.MkdirAll(filepath.Dir(mark), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(mark, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := ForgetLogin("claude", "max@x.com"); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(claudeAccountDir("max@x.com")); !os.IsNotExist(err) {
			t.Fatalf("the forgotten account's directory kept: %v", err)
		}
	})
}

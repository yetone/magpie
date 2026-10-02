package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestClaudeLocalPermissionFailureIsNotAccountRefusal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable permissions")
	}
	// Obtain the actual fork/exec error from a local non-executable file,
	// rather than inventing a Claude Code account-error message.
	exe := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, execErr := exec.Command(exe).Output()
	var pathErr *os.PathError
	if !errors.Is(execErr, os.ErrPermission) || !errors.As(execErr, &pathErr) || pathErr.Op != "fork/exec" {
		t.Fatalf("expected a local fork/exec permission error, got %v", execErr)
	}
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "without header", true: "fresh header"}[fresh], func(t *testing.T) {
			_, card := claudeUsageCards(t, "", nil)
			if fresh {
				NoteClaudeLimits("a@example.com", []ClaudeLimit{{Kind: "five_hour", Used: .42}})
			}
			UsageClaudeVia(func(context.Context) (string, error) { return "", execErr })
			for range 2 {
				q := card()
				if fresh {
					if q.Error != "" || q.AsOf != nil || len(q.Windows) != 1 || q.Windows[0].Used != 42 {
						t.Fatalf("a local execution error hid the fresh header: %+v", q)
					}
				} else if q.Error != execErr.Error() || q.AsOf != nil || len(q.Windows) != 0 {
					t.Fatalf("the local execution failure should remain visible without a header: %+v", q)
				}
			}
			timeout := errors.New("network timeout")
			UsageClaudeVia(func(context.Context) (string, error) { return "", timeout })
			retryClaudeUsage()
			q := card()
			if fresh {
				if q.Error != "" || q.AsOf != nil || len(q.Windows) != 1 || q.Windows[0].Used != 42 {
					t.Fatalf("a later timeout retained the local failure as an account refusal: %+v", q)
				}
			} else if q.Error != timeout.Error() {
				t.Fatalf("a later timeout was replaced by the prior local execution error: %+v", q)
			}
		})
	}
}

func TestClaudeUsageWithPermissionDeniedExplanation(t *testing.T) {
	const windows = "Current session: 40% used\nCurrent week (all models): 20% used"
	// This explanatory fixture is not a claimed Claude Code refusal.
	const note = "Local execution diagnostics: permission denied"
	for _, text := range []string{note + "\n" + windows, windows + "\n" + note} {
		t.Run(text, func(t *testing.T) {
			_, card := claudeUsageCards(t, text, nil)
			q := card()
			if q.Error != "" || q.AsOf != nil || len(q.Windows) != 2 || q.Windows[0].Used != 40 || q.Windows[1].Used != 20 {
				t.Fatalf("an explanatory line discarded a valid usage reading: %+v", q)
			}
		})
	}
}

func TestClaudeHeaderSurvivesUnclassifiedUsageFailure(t *testing.T) {
	_, card := claudeUsageCards(t, "offline", nil)
	reset := time.Now().Add(time.Hour).Unix()
	NoteClaudeLimits("a@example.com", []ClaudeLimit{{Kind: "five_hour", Used: .42, ResetsAt: reset}})
	claudeUsage.Lock()
	before := claudeUsage.m["a@example.com"]
	claudeUsage.Unlock()
	for range 2 {
		q := card()
		if q.Error != "" || q.AsOf != nil || len(q.Windows) != 1 || q.Windows[0].Used != 42 || q.Windows[0].ResetsAt.Unix() != reset {
			t.Fatalf("fresh header was replaced by a read failure: %+v", q)
		}
		claudeUsage.Lock()
		e := claudeUsage.m["a@example.com"]
		claudeUsage.Unlock()
		if !e.at.Equal(before.at) || !e.heard.Equal(before.heard) {
			t.Fatal("failed reads refreshed the header's timestamps")
		}
	}
	// The cached failure becomes visible once the header is too old,
	// even without another attempt at /usage.
	claudeUsage.Lock()
	e := claudeUsage.m["a@example.com"]
	e.heard = time.Now().Add(-2 * time.Hour)
	claudeUsage.m["a@example.com"] = e
	claudeUsage.Unlock()
	if q := card(); q.Error == "" || q.AsOf != nil || len(q.Windows) != 0 {
		t.Fatalf("an old header hid the read failure: %+v", q)
	}
}

func TestClaudeHeaderReceivedDuringFailedUsage(t *testing.T) {
	for _, prior := range []bool{false, true} {
		t.Run(map[bool]string{false: "first header", true: "newer header"}[prior], func(t *testing.T) {
			_, card := claudeUsageCards(t, "offline", nil)
			if prior {
				NoteClaudeLimits("a@example.com", []ClaudeLimit{{Kind: "five_hour", Used: .42}})
			}
			runs := 0
			UsageClaudeVia(func(context.Context) (string, error) {
				runs++
				NoteClaudeLimits("a@example.com", []ClaudeLimit{{Kind: "five_hour", Used: .7}})
				return "", errors.New("offline")
			})
			for range 2 {
				q := card()
				if q.Error != "" || q.AsOf != nil || len(q.Windows) != 1 || q.Windows[0].Used != 70 || runs != 1 {
					t.Fatalf("the newer header was lost: %+v (runs=%d)", q, runs)
				}
			}
		})
	}
}

func TestClaudeHeaderFallbackIsAccountScoped(t *testing.T) {
	_, card := claudeUsageCards(t, "offline", nil)
	NoteClaudeLimits("b@example.com", []ClaudeLimit{{Kind: "five_hour", Used: .42}})
	if q := card(); q.Error == "" || q.AsOf != nil || len(q.Windows) != 0 {
		t.Fatalf("another account's header hid this failure: %+v", q)
	}
}

func TestClaudeUsageReadingAloneDoesNotHideUnclassifiedFailure(t *testing.T) {
	out, card := claudeUsageCards(t, "Current session: 40% used", nil)
	if q := card(); q.Error != "" || len(q.Windows) != 1 || q.Windows[0].Used != 40 {
		t.Fatalf("first reading: %+v", q)
	}
	out.Store("offline")
	retryClaudeUsage()
	if q := card(); q.Error == "" || q.AsOf != nil || len(q.Windows) != 0 {
		t.Fatalf("a CLI reading was treated as a fresh header: %+v", q)
	}
}

func TestClaudeHeaderDoesNotClearAccountFailureOnAnotherReadError(t *testing.T) {
	for _, failure := range []string{"HTTP 401 Unauthorized", "HTTP 403 Forbidden", "You've hit your limit"} {
		t.Run(failure, func(t *testing.T) {
			out, card := claudeUsageCards(t, failure, nil)
			NoteClaudeLimits("a@example.com", []ClaudeLimit{{Kind: "five_hour", Used: .42}})
			if q := card(); q.Error == "" {
				t.Fatalf("the account failure was hidden: %+v", q)
			}
			out.Store("offline")
			retryClaudeUsage()
			if q := card(); q.Error == "" || q.AsOf != nil || len(q.Windows) != 0 {
				t.Fatalf("another read failure cleared the account failure: %+v", q)
			}
			// A genuinely new observation, rather than a failed read,
			// can establish that the account is usable again.
			NoteClaudeLimits("a@example.com", []ClaudeLimit{{Kind: "five_hour", Used: .3}})
			retryClaudeUsage()
			if q := card(); q.Error != "" || len(q.Windows) != 1 || q.Windows[0].Used != 30 {
				t.Fatalf("new header did not restore the account reading: %+v", q)
			}
		})
	}
}

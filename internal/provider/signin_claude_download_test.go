package provider

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

// Kabubu01 (X): with no Claude Code on the computer, the GUI's Claude
// sign-in failed with "install Claude Code first … or run `magpie
// claude-code install`". The sign-in downloads it now, as Devin's,
// Cursor's and Grok's install their CLIs, and goes on to the sign-in page.
func TestClaudeSignInDownloadsClaudeCode(t *testing.T) {
	claudeHome(t)
	noAnthropic(t)
	exe := fakeClaudeLogin(t, fakeClaudeAccount{email: "dl@example.com", plan: "max", refresh: "sk-ant-ort01-dl"}, "", false)
	var downloaded atomic.Bool
	claudeExecutable = func() string {
		if downloaded.Load() {
			return exe
		}
		return ""
	}
	downloadClaudeCode = func(context.Context) error {
		downloaded.Store(true)
		return nil
	}

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != "installing" || st.Installing != "Claude Code" {
		t.Fatalf("with no Claude Code: %+v", st)
	}
	st = waitPast(t, st.ID, "installing")
	if !downloaded.Load() || !st.PasteCode {
		t.Fatalf("after the download: %+v", st)
	}
	if err := SubmitSignInCallback(st.ID, "the-code#st"); err != nil {
		t.Fatal(err)
	}
	if st = waitDone(t, st.ID); st.State != "done" || st.User != "dl@example.com" {
		t.Fatalf("state %+v", st)
	}
}

// a download that fails says so, and the command that does it by hand
func TestClaudeSignInDownloadFails(t *testing.T) {
	claudeHome(t)
	downloadClaudeCode = func(context.Context) error { return errors.New("downloads.claude.ai: 403 Forbidden") }

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	st = waitPast(t, st.ID, "installing")
	if st.State != "failed" || !strings.Contains(st.Error, "403 Forbidden") || !strings.Contains(st.Error, "magpie claude-code install") {
		t.Fatalf("state %+v", st)
	}
}

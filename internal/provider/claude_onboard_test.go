package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Jaxxxxnn on X (magpie 0.1.2669, Claude Code 2.1.316): a saved account's
// config directory had a .claude.json without hasCompletedOnboarding, so
// Claude Code's interactive mode asked for a login method beside a sign-in
// that works, and every run and usage read there ended. The mark is added
// to the file that is there, every other key kept, both where the sign-in
// is put in and where it is there already.
func TestClaudeSavedDirMarksOnboardingDone(t *testing.T) {
	claudeThree(t)
	var users []string
	for _, l := range Logins("claude") {
		if !l.Active {
			users = append(users, l.User)
		}
	}
	if len(users) < 2 {
		t.Fatalf("%d saved accounts beside the active one, want 2", len(users))
	}
	read := func(dir string) map[string]any {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
		return m
	}
	// a .claude.json without the mark, as -p or an older magpie left it
	stale := `{"numStartups": 3, "firstStartTime": "2026-10-30T01:02:03.000Z", "userID": "abc", "big": 12345678901234567890}`

	// the sign-in not put in yet: the profile file there is kept
	fresh := claudeAccountDir(users[0])
	if err := os.MkdirAll(fresh, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fresh, ".claude.json"), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	// the sign-in there already, put in by an earlier run
	had, err := claudeSavedDir(users[1])
	if err != nil {
		t.Fatal(err)
	}
	if m := read(had); m["hasCompletedOnboarding"] != true || m["oauthAccount"] == nil {
		t.Fatalf("a new directory's .claude.json is %v, want the mark and the profile", m)
	}
	if err := os.WriteFile(filepath.Join(had, ".claude.json"), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, u := range users[:2] {
		dir, err := claudeSavedDir(u)
		if err != nil {
			t.Fatal(err)
		}
		m := read(dir)
		if m["hasCompletedOnboarding"] != true {
			t.Fatalf("%s: onboarding not marked done: %v", u, m)
		}
		if m["numStartups"] != float64(3) || m["userID"] != "abc" || m["firstStartTime"] != "2026-10-30T01:02:03.000Z" {
			t.Fatalf("%s: Claude Code's own keys not kept: %v", u, m)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, ".claude.json")); !strings.Contains(string(b), "12345678901234567890") {
			t.Fatalf("%s: a number Claude Code wrote was rewritten: %s", u, b)
		}
	}

	// a file being written (not JSON yet) is left as it is
	half := `{"numStartups": 3, "userI`
	if err := os.WriteFile(filepath.Join(had, ".claude.json"), []byte(half), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := claudeSavedDir(users[1]); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(had, ".claude.json")); string(b) != half {
		t.Fatalf("a half-written .claude.json was written over: %s", b)
	}
}

// A setup-token account's directory made before the mark was written, or
// one -p ran in, gets the mark too.
func TestClaudeTokenDirMarksOnboardingDone(t *testing.T) {
	claudeHome(t)
	dir := claudeAccountDir("token@x.com")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"numStartups": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := claudeTokenDir("token@x.com"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".claude.json"))
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m["hasCompletedOnboarding"] != true || m["numStartups"] != float64(1) {
		t.Fatalf(".claude.json is %s (%v), want the mark beside numStartups", b, err)
	}
}

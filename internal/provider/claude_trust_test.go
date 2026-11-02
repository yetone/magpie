package provider

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// mydosu's saved account (#4830, Windows, Claude Code 2.1.316) had a
// .claude.json with no projects at all, so Claude Code asked whether to
// trust magpie's work folder on every /usage read. ClaudeTrustFolder puts
// the entry there as Claude Code keeps it, every other key and the
// project's own fields kept, and writes nothing once it is there.
func TestClaudeTrustFolderRecordsMagpiesFolder(t *testing.T) {
	claudeHome(t)
	dir := claudeAccountDir("someone@example.com")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(dir, ".claude.json")
	key := "C:/Users/me/AppData/Local/Temp/magpie-claude-work"
	// onboarding done, magpie's folder there untrusted; a big number as
	// Claude Code writes them
	stale := `{"hasCompletedOnboarding": true, "numStartups": 3, "big": 12345678901234567890,
  "projects": {"C:/elsewhere": {"allowedTools": [], "hasTrustDialogAccepted": false},
    "` + key + `": {"lastCost": 0.5, "hasTrustDialogAccepted": false}}}`
	if err := os.WriteFile(profile, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ClaudeTrustFolder(dir, key, "/private/"+key); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(profile)
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	if m["hasCompletedOnboarding"] != true || m["numStartups"] != json.Number("3") || m["big"] != json.Number("12345678901234567890") {
		t.Fatalf("other keys changed: %s", b)
	}
	ps := m["projects"].(map[string]any)
	for _, k := range []string{key, "/private/" + key} {
		if p, _ := ps[k].(map[string]any); p["hasTrustDialogAccepted"] != true {
			t.Errorf("%s is %v, want trusted: %s", k, ps[k], b)
		}
	}
	if p := ps[key].(map[string]any); p["lastCost"] != json.Number("0.5") {
		t.Errorf("the project's own fields went: %v", p)
	}
	if p := ps["C:/elsewhere"].(map[string]any); p["hasTrustDialogAccepted"] != false {
		t.Errorf("another folder was trusted: %v", p)
	}
	// there already: nothing is written
	if err := os.WriteFile(profile, append(b, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ClaudeTrustFolder(dir, key); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(profile); !bytes.Equal(again, append(b, ' ')) {
		t.Errorf("a trusted folder was written again")
	}
}

// Only a saved account's directory is written: never the user's own
// ~/.claude.json, nor a file Claude Code may be half way through writing.
func TestClaudeTrustFolderOnlyMagpiesAccounts(t *testing.T) {
	home := claudeHome(t)
	key := "/tmp/magpie-claude-work-501"
	for _, dir := range []string{home, filepath.Join(home, ".claude"), claudeDirsRoot(), filepath.Join(claudeAccountDir("a"), "x")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := ClaudeTrustFolder(dir, key); err == nil {
			t.Errorf("%s was written", dir)
		}
		if _, err := os.Stat(filepath.Join(dir, ".claude.json")); !os.IsNotExist(err) {
			t.Errorf("%s has a .claude.json", dir)
		}
	}
	dir := claudeAccountDir("b")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	half := []byte(`{"numStartups": 3, "proj`)
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), half, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ClaudeTrustFolder(dir, key); err == nil {
		t.Errorf("a half-written file was taken")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".claude.json")); !bytes.Equal(b, half) {
		t.Errorf("a half-written file was changed: %s", b)
	}
}

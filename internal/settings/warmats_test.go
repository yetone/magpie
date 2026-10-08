package settings

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

// #1260 (Sinnhu): a daily warm-up has several times. A settings file from
// before them, with the one codexWarmAt, has that one; the list is kept
// as 06:00, earliest first, once each, and its first written as
// codexWarmAt for an older magpie.
func TestWarmAtsMigrate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(`{"codexWarmAt":"6:00","claudeWarmAt":"21:30"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Load()
	if !slices.Equal(s.CodexWarmAts, []string{"06:00"}) || s.CodexWarmAt != "06:00" || !slices.Equal(s.ClaudeWarmAts, []string{"21:30"}) {
		t.Fatalf("old file: %q %q %q", s.CodexWarmAts, s.CodexWarmAt, s.ClaudeWarmAts)
	}
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if s = Load(); !slices.Equal(s.CodexWarmAts, []string{"06:00"}) || s.ClaudeWarmAt != "21:30" {
		t.Fatalf("saved as read: %q %q", s.CodexWarmAts, s.ClaudeWarmAt)
	}
	// several, however given; the page still sends the first it was drawn with
	s.CodexWarmAts = []string{"19:10", " 9:00", "15:05:00", "09:00", ""}
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if s = Load(); !slices.Equal(s.CodexWarmAts, []string{"09:00", "15:05", "19:10"}) || s.CodexWarmAt != "09:00" {
		t.Fatalf("several: %q %q", s.CodexWarmAts, s.CodexWarmAt)
	}
	// what an older magpie reads of the file: the day's first
	b, _ := os.ReadFile(Path())
	var old struct {
		CodexWarmAt string `json:"codexWarmAt"`
	}
	if json.Unmarshal(b, &old) != nil || old.CodexWarmAt != "09:00" {
		t.Fatalf("first not kept for an older magpie:\n%s", b)
	}
	// one that isn't a time is refused, wherever it is in the list
	if Save(Settings{CodexWarmAts: []string{"09:00", "25:00"}}) == nil {
		t.Fatal("bad time in the list accepted")
	}
	// all removed, though the page still sends the first it was drawn with
	s.CodexWarmAts, s.CodexWarmAt = []string{}, "09:00"
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if s = Load(); len(s.CodexWarmAts) != 0 || s.CodexWarmAt != "" {
		t.Fatalf("off: %q %q", s.CodexWarmAts, s.CodexWarmAt)
	}
}

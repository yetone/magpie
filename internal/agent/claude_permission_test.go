package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// Claude Code's permission mode is the user's own (EricChenChannel on X:
// bypassPermissions set by hand in settings.json kept coming back as
// default). magpie writes it where Claude Code reads it, with the consent
// key bypass needs at launch, and nothing magpie does to the agent puts it
// back or takes it out.
func permissionHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://relay.example.com/v1", Key: "synthetic-key", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme": "dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, path
}

func permAt(t *testing.T, path, key string) string {
	t.Helper()
	v, _ := edit.GetJSON(path, key)
	return v
}

func TestClaudePermissionModeWritten(t *testing.T) {
	home, path := permissionHome(t)
	f := claude(home).Field("permission_mode")
	if f == nil {
		t.Fatal("Claude Code has no permission mode setting")
	}
	if !f.Own {
		t.Error("the permission mode is the user's own, not magpie's wiring")
	}
	if err := f.Set("bypassPermissions"); err != nil {
		t.Fatal(err)
	}
	// bypass without its consent key is ignored at launch by Claude Code
	if m, ok := permAt(t, path, "permissions.defaultMode"), permAt(t, path, "skipDangerousModePermissionPrompt"); m != "bypassPermissions" || ok != "true" {
		b, _ := os.ReadFile(path)
		t.Errorf("bypass written as:\n%s", b)
	}
	if err := f.Set("plan"); err != nil {
		t.Fatal(err)
	}
	if got := f.Get(); got != "plan" {
		t.Errorf("mode reads %q", got)
	}
	if err := f.Set("everything"); err == nil {
		t.Error("a mode Claude Code doesn't have was written")
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	if permAt(t, path, "permissions.defaultMode") != "" || permAt(t, path, "theme") != "dark" {
		b, _ := os.ReadFile(path)
		t.Errorf("not set should take out the mode alone:\n%s", b)
	}
}

func TestClaudePermissionModeOutlivesMagpie(t *testing.T) {
	home, path := permissionHome(t)
	if _, err := claude(home).ConnectHow(); err != nil {
		t.Fatal(err)
	}
	// picked in magpie, so Apply has seen it
	if err := claude(home).Pick("permission_mode", "bypassPermissions"); err != nil {
		t.Fatal(err)
	}
	if _, ok := appliedOf(claude(home).ID).Fields["permission_mode"]; ok {
		t.Error("the user's permission mode was recorded as magpie's")
	}
	if err := claude(home).Pick("model", "relay/flash"); err != nil {
		t.Fatal(err)
	}
	if err := claude(home).Disconnect(); err != nil {
		t.Fatal(err)
	}
	if m := permAt(t, path, "permissions.defaultMode"); m != "bypassPermissions" {
		b, _ := os.ReadFile(path)
		t.Errorf("disconnecting took the mode to %q:\n%s", m, b)
	}
	if _, err := claude(home).ConnectHow(); err != nil {
		t.Fatal(err)
	}
	if err := claude(home).Field("permission_mode").Set("plan"); err != nil {
		t.Fatal(err)
	}
	if err := claude(home).Disconnect(); err != nil {
		t.Fatal(err)
	}
	if m := permAt(t, path, "permissions.defaultMode"); m != "plan" {
		t.Errorf("the mode set while connected came back as %q", m)
	}
}

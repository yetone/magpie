package gui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// Claude Code's permission mode is the user's own setting (EricChenChannel
// on X): the Agents page gets it apart from the row's pickers, as the
// agent's own, with what settings.json holds and every mode to pick.
func TestClaudePermissionModeSentAsOwn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	path := filepath.Join(home, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(`{"permissions": {"defaultMode": "bypassPermissions"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := agent.Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	vals := a.Values()
	for _, f := range agentFields(a, vals) {
		if f.Key == "permission_mode" {
			t.Error("the permission mode is among the row's pickers")
		}
	}
	own := ownFields(a, vals)
	if len(own) != 1 || own[0].Key != "permission_mode" || own[0].Value != "bypassPermissions" || len(own[0].Options) != 6 {
		t.Fatalf("the agent's own settings: %+v", own)
	}
}

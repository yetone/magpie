package cli

import (
	"bytes"
	"os"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// magpie update channel stable|daily|every saves Settings' Update channel
// (ssharpcc on X); with nothing after it, it only says which; anything
// else is refused and nothing is saved.
func TestUpdateChannelCmd(t *testing.T) {
	groupsHome(t)
	for _, c := range []struct {
		args []string
		want string
		bad  bool
	}{
		{[]string{"update", "channel"}, "", false},
		{[]string{"update", "channel", "stable"}, "stable", false},
		{[]string{"update", "channel"}, "stable", false},
		{[]string{"update", "channel", "beta"}, "stable", true},
		{[]string{"update", "channel", "stable", "every"}, "stable", true},
		{[]string{"update", "channel", "every"}, "", false},
		{[]string{"update", "channel", "Stable"}, "stable", false},
		{[]string{"update", "channel", "all"}, "", false},
		{[]string{"update", "channel", "daily"}, "daily", false},
		{[]string{"update", "channel", "Daily"}, "daily", false},
		{[]string{"update", "channel", "weekly"}, "daily", true},
	} {
		err := updateCmd(c.args)
		if (err != nil) != c.bad {
			t.Fatalf("%v: %v", c.args, err)
		}
		if got := settings.Load().UpdateChannel; got != c.want {
			t.Fatalf("%v: saved %q, want %q", c.args, got, c.want)
		}
	}
}

// A settings.json that can't be read is never written over by it.
func TestUpdateChannelKeepsCorruptSettings(t *testing.T) {
	groupsHome(t)
	if err := os.MkdirAll(settings.Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"theme":"dark","githubToken":"SYNTHETIC_PRIVATE_TOKEN"`)
	if err := os.WriteFile(settings.Path(), original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updateCmd([]string{"update", "channel", "stable"}); err == nil {
		t.Error("the command reported success with corrupt settings")
	}
	if after, err := os.ReadFile(settings.Path()); err != nil || !bytes.Equal(after, original) {
		t.Errorf("the command overwrote the corrupt settings: %v", err)
	}
}

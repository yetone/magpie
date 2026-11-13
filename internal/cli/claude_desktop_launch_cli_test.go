package cli

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// `magpie claude-desktop launcher` (#5671): add and remove turn the
// isolated Desktop on and off, nothing else is a word it takes, --help
// changes nothing, and off a Mac it refuses.
func TestClaudeDesktopLauncher(t *testing.T) {
	helpHome(t)
	var calls []bool
	st := agent.DesktopIsolated{}
	here := true
	oldSet, oldHere := setDesktopIsolated, desktopIsolatedHere
	t.Cleanup(func() { setDesktopIsolated, desktopIsolatedHere = oldSet, oldHere })
	setDesktopIsolated = func(_ string, on bool) (string, error) {
		calls = append(calls, on)
		st.On = on
		st.Launcher = ""
		if on {
			st.Launcher = "/Users/x/Applications/Claude Magpie.app"
		}
		return st.Launcher, nil
	}
	desktopIsolatedHere = func() (agent.DesktopIsolated, bool) { return st, here }

	for _, c := range []struct {
		args  []string
		want  string
		calls []bool
	}{
		{[]string{"claude-desktop", "launcher"}, "run `magpie claude-desktop launcher add`", nil},
		{[]string{"claude-desktop", "launcher", "--help"}, "magpie claude-desktop launcher [add|remove]", nil},
		{[]string{"claude-desktop", "launcher", "add"}, "Claude Magpie: /Users/x/Applications/Claude Magpie.app", []bool{true}},
		{[]string{"claude-app", "launcher"}, "/Users/x/Applications/Claude Magpie.app\n", []bool{true}},
		{[]string{"claude-desktop", "launcher", "remove"}, "removed /Users/x/Applications/Claude Magpie.app", []bool{true, false}},
	} {
		out, err := runOut(t, c.args...)
		if err != nil || !strings.Contains(out, c.want) {
			t.Errorf("%v: %q %v, want %q", c.args, out, err, c.want)
		}
		if len(calls) != len(c.calls) {
			t.Fatalf("%v: calls %v, want %v", c.args, calls, c.calls)
		}
	}
	for _, bad := range [][]string{{"claude-desktop", "launcher", "--force"}, {"claude-desktop", "launcher", "add", "x"}} {
		if _, err := runOut(t, bad...); err == nil {
			t.Errorf("%v took", bad)
		}
	}
	here = false
	if _, err := runOut(t, "claude-desktop", "launcher", "add"); err == nil || !strings.Contains(err.Error(), "macOS only") {
		t.Errorf("off a Mac: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls %v", calls)
	}
}

// `magpie claude-desktop launcher claude-dir` (#6459): on and off reach
// the setting, nothing says which folder, another word refuses, and off a
// Mac it refuses too.
func TestClaudeDesktopLauncherClaudeDir(t *testing.T) {
	helpHome(t)
	var calls []bool
	st := agent.DesktopIsolated{On: true, Launcher: "/Users/x/Applications/Claude Magpie.app", ClaudeDir: "/Users/x/.claude-magpie-desktop"}
	here := true
	oldSet, oldHere := setDesktopIsolatedClaude, desktopIsolatedHere
	t.Cleanup(func() { setDesktopIsolatedClaude, desktopIsolatedHere = oldSet, oldHere })
	setDesktopIsolatedClaude = func(_ string, on bool) error {
		calls = append(calls, on)
		st.Claude = on
		return nil
	}
	desktopIsolatedHere = func() (agent.DesktopIsolated, bool) { return st, here }
	for _, c := range []struct {
		args  []string
		want  string
		calls []bool
	}{
		{[]string{"claude-desktop", "launcher", "claude-dir"}, "~/.claude: run", nil},
		{[]string{"claude-desktop", "launcher", "claude-dir", "on"}, "Quit Claude Magpie and open it again", []bool{true}},
		{[]string{"claude-desktop", "launcher", "claude-dir"}, "/Users/x/.claude-magpie-desktop\n", []bool{true}},
		{[]string{"claude-desktop", "launcher", "claude-dir", "off"}, "Claude Code in Claude Magpie: ~/.claude", []bool{true, false}},
	} {
		out, err := runOut(t, c.args...)
		if err != nil || !strings.Contains(out, c.want) {
			t.Errorf("%v: %q %v, want %q", c.args, out, err, c.want)
		}
		if len(calls) != len(c.calls) {
			t.Fatalf("%v: calls %v, want %v", c.args, calls, c.calls)
		}
	}
	for _, bad := range [][]string{{"claude-desktop", "launcher", "claude-dir", "yes"}, {"claude-desktop", "launcher", "claude-dir", "on", "x"}} {
		if _, err := runOut(t, bad...); err == nil {
			t.Errorf("%v took", bad)
		}
	}
	here = false
	if _, err := runOut(t, "claude-desktop", "launcher", "claude-dir", "on"); err == nil || !strings.Contains(err.Error(), "macOS only") {
		t.Errorf("off a Mac: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls %v", calls)
	}
}

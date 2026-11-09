package agent

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/desktopdir"
)

// isolatedSandbox is desktopSandbox on a Mac, with the user's choice held
// in the test and the app magpie would make stubbed: home, the user's own
// Desktop's folders and the isolated one's.
func isolatedSandbox(t *testing.T) (string, desktopPaths, desktopPaths, *bool) {
	t.Helper()
	old := desktopdir.OS
	desktopdir.OS = "darwin"
	t.Cleanup(func() { desktopdir.OS = old })
	home, _ := desktopSandbox(t)
	usual := desktopUsualSets("darwin", home, os.Getenv)[0]
	iso, ok := desktopIsolatedSet("darwin", home, os.Getenv)
	if !ok {
		t.Fatal("no isolated set on darwin")
	}
	on := false
	oldOn, oldSave, oldApp, oldAdd := desktopIsolatedOn, saveDesktopIsolated, desktopClaudeApp, desktopIsoAdd
	desktopIsolatedOn = func() bool { return on }
	saveDesktopIsolated = func(v bool) error { on = v; return nil }
	claudeApp := filepath.Join(home, "Applications", "Claude.app")
	desktopClaudeApp = func() string { return claudeApp }
	desktopIsoAdd = func(home, app, dir string) (string, error) {
		if app != claudeApp || dir != iso.dir3p {
			t.Errorf("app made for %q on %q", app, dir)
		}
		p := filepath.Join(home, "Applications", "Claude Magpie.app")
		return p, os.MkdirAll(p, 0o755)
	}
	t.Cleanup(func() {
		desktopIsolatedOn, saveDesktopIsolated, desktopClaudeApp, desktopIsoAdd = oldOn, oldSave, oldApp, oldAdd
	})
	return home, usual, iso, &on
}

// The isolated Desktop (#5671): turned on with Desktop on magpie, the
// user's own Desktop is put back as it was before magpie, byte for byte,
// and Claude-Magpie-3p gets magpie's 3p mode, with the user's language
// and MCP servers; the agent then reads and writes that folder. Off moves
// it back.
func TestClaudeDesktopIsolatedMovesThere(t *testing.T) {
	home, usual, iso, on := isolatedSandbox(t)
	desktopWrite(t, usual.config, `{"mcpServers":{"fs":{"command":"npx"}}}`)
	desktopWrite(t, usual.app, `{"locale":"zh-CN"}`)
	before := desktopTree(t, usual)

	f := claudeDesktop(home).Field("provider")
	if err := f.Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if !desktopWired(usual) || desktopWired(iso) {
		t.Fatal("not on magpie in the usual folders")
	}

	p, err := SetDesktopIsolated(home, true)
	if err != nil {
		t.Fatal(err)
	}
	if !*on || p != filepath.Join(home, "Applications", "Claude Magpie.app") {
		t.Fatalf("on %v, app %q", *on, p)
	}
	if after := desktopTree(t, usual); !reflect.DeepEqual(after, before) {
		t.Fatalf("the user's own Desktop:\n%v\nwas:\n%v", after, before)
	}
	if !desktopWired(iso) {
		t.Fatal("Claude-Magpie-3p not on magpie")
	}
	if m := desktopJSON(t, iso.config3p); m["deploymentMode"] != "3p" || !reflect.DeepEqual(m["mcpServers"], map[string]any{"fs": map[string]any{"command": "npx"}}) {
		t.Fatalf("Claude-Magpie-3p's config: %v", m)
	}
	if m := desktopJSON(t, iso.app3p); m["locale"] != "zh-CN" {
		t.Fatalf("Claude-Magpie-3p's language: %v", m)
	}

	a := claudeDesktop(home)
	if a.Path != usual.config {
		t.Errorf("row's file %q, want the user's own %q", a.Path, usual.config)
	}
	if a.Field("provider").Get() != "magpie" || a.Check() != "" {
		t.Fatalf("get %q, check %q", a.Field("provider").Get(), a.Check())
	}
	if a.Notice() != noticeClaudedesktopIsolated.String() {
		t.Errorf("notice %q", a.Notice())
	}
	if got := DesktopConfigs3p(home); got[len(got)-1] != iso.config3p {
		t.Errorf("3p configs %v miss %s", got, iso.config3p)
	}
	// off magpie and on again, while isolated: only Claude-Magpie-3p
	if err := a.Field("provider").Set(""); err != nil {
		t.Fatal(err)
	}
	if desktopWired(iso) || desktopWired(usual) {
		t.Fatal("still on magpie")
	}
	if err := a.Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if !desktopWired(iso) || desktopWired(usual) {
		t.Fatal("on again went to the user's own Desktop")
	}

	if _, err := SetDesktopIsolated(home, false); err != nil {
		t.Fatal(err)
	}
	if *on || !desktopWired(usual) || desktopWired(iso) {
		t.Fatalf("off: on %v, usual %v, isolated %v", *on, desktopWired(usual), desktopWired(iso))
	}
	if claudeDesktop(home).Notice() == noticeClaudedesktopIsolated.String() {
		t.Error("isolated notice after off")
	}
}

// Turned on with Desktop not on magpie, nothing of the user's is written:
// the app is made, and connecting later wires Claude-Magpie-3p only.
func TestClaudeDesktopIsolatedBeforeConnecting(t *testing.T) {
	home, usual, iso, _ := isolatedSandbox(t)
	desktopWrite(t, usual.config, `{"globalShortcut":"Alt+Space"}`)
	before := desktopTree(t, usual)
	if _, err := SetDesktopIsolated(home, true); err != nil {
		t.Fatal(err)
	}
	if after := desktopTree(t, usual); !reflect.DeepEqual(after, before) {
		t.Fatalf("on wrote the user's Desktop:\n%v", after)
	}
	if desktopWired(iso) {
		t.Fatal("wired before connecting")
	}
	if claudeDesktop(home).Notice() != noticeClaudedesktopIsolatedOff.String() {
		t.Error("notice")
	}
	if err := claudeDesktop(home).Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if !desktopWired(iso) || desktopWired(usual) {
		t.Fatal("connecting didn't wire Claude-Magpie-3p alone")
	}
	if after := desktopTree(t, usual); !reflect.DeepEqual(after, before) {
		t.Fatalf("connecting wrote the user's Desktop:\n%v", after)
	}
}

// Windows and Linux have no isolated Desktop: the switch refuses, and the
// agent wires the usual folders.
func TestClaudeDesktopIsolatedOnlyOnAMac(t *testing.T) {
	for _, goos := range []string{"windows", "linux"} {
		t.Run(goos, func(t *testing.T) {
			old := desktopdir.OS
			desktopdir.OS = goos
			t.Cleanup(func() { desktopdir.OS = old })
			home, _ := desktopSandbox(t)
			oldOn := desktopIsolatedOn
			desktopIsolatedOn = func() bool { return true }
			t.Cleanup(func() { desktopIsolatedOn = oldOn })
			if _, err := SetDesktopIsolated(home, true); !errors.Is(err, ErrDesktopIsolatedOS) {
				t.Fatalf("err %v", err)
			}
			if ps := desktopSets(goos, home, os.Getenv); ps[0].key == desktopIsolatedKey {
				t.Fatalf("isolated set on %s", goos)
			}
		})
	}
}

// Two Claude.app processes run with the isolated Desktop on: magpie's
// Restart and its "running on the old settings" read the one it wires.
func TestDesktopInstance(t *testing.T) {
	old := desktopdir.OS
	desktopdir.OS = "darwin"
	t.Cleanup(func() { desktopdir.OS = old })
	home, _ := desktopSandbox(t)
	iso := desktopdir.Isolated("darwin", home)
	exe := "/Applications/Claude.app/Contents/MacOS/Claude"
	theirs, ours := exe, exe+" --user-data-dir="+iso
	other := exe + " --user-data-dir=" + iso + "-x"
	on := false
	oldOn := desktopIsolatedOn
	desktopIsolatedOn = func() bool { return on }
	t.Cleanup(func() { desktopIsolatedOn = oldOn })
	for _, c := range []struct {
		on   bool
		cmd  string
		want bool
	}{
		{false, theirs, true}, {false, ours, false}, {false, theirs + " -psn_0_1", true},
		{true, theirs, false}, {true, ours, true}, {true, ours + " --flag", true}, {true, other, false},
	} {
		on = c.on
		if got := desktopInstance(c.cmd); got != c.want {
			t.Errorf("on %v, %q: %v", c.on, c.cmd, got)
		}
	}
	if restartApps["claude-desktop"].macCmd == nil {
		t.Error("Claude Desktop's Restart reads every Claude.app process")
	}
}

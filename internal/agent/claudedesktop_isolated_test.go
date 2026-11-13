package agent

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yetone/magpie/internal/desktopdir"
	"github.com/yetone/magpie/internal/desktopiso"
	"github.com/yetone/magpie/internal/edit"
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
	usual, iso, on, _ := isolatedFakes(t, home)
	return home, usual, iso, on
}

// isolatedFakes holds the isolated Desktop's choices in the test, its
// Claude Code folder's too, and stubs the app: made as magpie would make
// it (desktopiso.Here finds it), with the Claude Code folder it was last
// made with in *made. desktopdir.OS is darwin.
func isolatedFakes(t *testing.T, home string) (desktopPaths, desktopPaths, *bool, *[]string) {
	t.Helper()
	usual := desktopUsualSets("darwin", home, os.Getenv)[0]
	iso, ok := desktopIsolatedSet("darwin", home, os.Getenv)
	if !ok {
		t.Fatal("no isolated set on darwin")
	}
	on, claudeOn, chosen := false, false, false
	var made []string
	oldOn, oldSave, oldApp, oldAdd := desktopIsolatedOn, saveDesktopIsolated, desktopClaudeApp, desktopIsoAdd
	oldCOn, oldCSave, oldChosen := desktopIsolatedClaudeOn, saveDesktopIsolatedClaude, desktopIsolatedClaudeChosen
	desktopIsolatedOn = func() bool { return on }
	saveDesktopIsolated = func(v bool) error { on = v; return nil }
	desktopIsolatedClaudeOn = func() bool { return claudeOn }
	desktopIsolatedClaudeChosen = func() bool { return chosen }
	saveDesktopIsolatedClaude = func(v, c bool) error { claudeOn, chosen = v, chosen || c; return nil }
	claudeApp := filepath.Join(home, "Applications", "Claude.app")
	desktopClaudeApp = func() string { return claudeApp }
	desktopIsoAdd = func(home, app, dir, claudeDir string) (string, error) {
		if app != claudeApp || dir != iso.dir3p {
			t.Errorf("app made for %q on %q", app, dir)
		}
		made = append(made, claudeDir)
		p := desktopiso.Path(home)
		if err := os.MkdirAll(filepath.Join(p, "Contents"), 0o755); err != nil {
			return "", err
		}
		return p, os.WriteFile(filepath.Join(p, "Contents", "Info.plist"), []byte("<key>CFBundleIdentifier</key><string>"+desktopiso.BundleID+"</string>"), 0o644)
	}
	t.Cleanup(func() {
		desktopIsolatedOn, saveDesktopIsolated, desktopClaudeApp, desktopIsoAdd = oldOn, oldSave, oldApp, oldAdd
		desktopIsolatedClaudeOn, saveDesktopIsolatedClaude, desktopIsolatedClaudeChosen = oldCOn, oldCSave, oldChosen
	})
	return usual, iso, &on, &made
}

// The isolated Desktop (#5671): turned on with Desktop on magpie, the
// user's own Desktop is put back as it was before magpie, byte for byte,
// and Claude-Magpie-3p gets magpie's 3p mode, with the user's language
// and MCP servers; the agent then reads and writes that folder. Off moves
// it back, there having been its wiring before.
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
	// the library gives the twin servers of its own, not the user's
	// Desktop's (xMopf7RF on X)
	for _, p := range DesktopConfigs3p(home) {
		if p == iso.config3p {
			t.Errorf("3p configs %v have the twin's %s", DesktopConfigs3p(home), iso.config3p)
		}
	}
	if tw := DesktopTwinApp(home); tw == nil || tw.Path != iso.config3p || tw.Dir != iso.dir3p || !tw.Detected() {
		t.Errorf("the twin's app %+v, want %s, here", tw, iso.config3p)
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

// The isolated Desktop's Code tab on a Claude Code folder of its own
// (#6459, xMopf7RF on X): Claude Magpie.app is made again with
// CLAUDE_CONFIG_DIR on ~/.claude-magpie-desktop, the windows and
// capabilities of Desktop's models leave ~/.claude/settings.json (the
// user's own keys stay) and go to that folder's, which is a Claude Code
// row of its own named for the app. Off takes them back, the app made
// again without the folder; the folder stays. The isolated Desktop
// turned off brings them back to ~/.claude too, its folder no longer
// being what any Desktop runs on.
func TestClaudeDesktopIsolatedClaudeDir(t *testing.T) {
	home, path := desktopWindowSandbox(t)
	old := desktopdir.OS
	desktopdir.OS = "darwin"
	t.Cleanup(func() { desktopdir.OS = old })
	_, iso, _, made := isolatedFakes(t, home)
	// the user chose ~/.claude for it, so a new Claude Magpie keeps that
	if err := saveDesktopIsolatedClaude(false, true); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(home, ".claude-magpie-desktop", "settings.json")
	get := func(f, k string) string { v, _ := edit.GetJSON(f, "env."+k); return v }
	where := func(want string) {
		t.Helper()
		for _, f := range []string{path, own} {
			w, c := get(f, claudeContextEnv), get(f, claudeCapsEnv)
			if f == want && (w == "" || c == "") || f != want && (w != "" || c != "") {
				t.Fatalf("%s: window %q, caps %q; want them in %s", f, w, c, want)
			}
		}
		if v, _ := edit.GetJSON(path, "theme"); v != "dark" {
			t.Fatalf("the user's own theme: %q", v)
		}
	}

	if err := claudeDesktop(home).Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	where(path)
	window := get(path, claudeContextEnv)
	if _, err := SetDesktopIsolated(home, true); err != nil {
		t.Fatal(err)
	}
	if !desktopWired(iso) {
		t.Fatal("isolated Desktop not on magpie")
	}
	// on ~/.claude still, as before
	where(path)
	if !reflect.DeepEqual(*made, []string{""}) {
		t.Fatalf("app made with %q", *made)
	}

	if err := SetDesktopIsolatedClaude(home, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*made, []string{"", filepath.Join(home, ".claude-magpie-desktop")}) {
		t.Fatalf("app made with %q", *made)
	}
	where(own)
	if got := get(own, claudeContextEnv); got != window {
		t.Errorf("its window %q, ~/.claude's was %q", got, window)
	}
	st, ok := DesktopIsolatedHere()
	if !ok || !st.Claude || st.ClaudeDir != filepath.Dir(own) {
		t.Errorf("state %+v", st)
	}
	var row *Agent
	for _, a := range claudeDirAgents(home) {
		if a.Path == own {
			row = a
		}
	}
	if row == nil || row.Name != "Claude Code · Claude Magpie" {
		t.Fatalf("its row: %+v", row)
	}
	// the round that syncs every agent keeps it so
	if err := claude(home).Sync(); err != nil {
		t.Fatal(err)
	}
	if err := row.Sync(); err != nil {
		t.Fatal(err)
	}
	where(own)

	if err := SetDesktopIsolatedClaude(home, false); err != nil {
		t.Fatal(err)
	}
	where(path)
	if (*made)[len(*made)-1] != "" {
		t.Fatalf("app made with %q", *made)
	}
	if _, err := os.Stat(own); err != nil {
		t.Fatal("its folder went:", err)
	}

	// on again, then the isolated Desktop off: ~/.claude again
	if err := SetDesktopIsolatedClaude(home, true); err != nil {
		t.Fatal(err)
	}
	where(own)
	if _, err := SetDesktopIsolated(home, false); err != nil {
		t.Fatal(err)
	}
	where(path)
}

// Off a Mac there is no such folder to give it.
func TestClaudeDesktopIsolatedClaudeDirOnlyOnAMac(t *testing.T) {
	old := desktopdir.OS
	desktopdir.OS = "windows"
	t.Cleanup(func() { desktopdir.OS = old })
	home, _ := desktopSandbox(t)
	if err := SetDesktopIsolatedClaude(home, true); !errors.Is(err, ErrDesktopIsolatedOS) {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude-magpie-desktop")); err == nil {
		t.Fatal("folder made off a Mac")
	}
}

// A Claude Magpie made beside a Desktop not on magpie is a twin, like a
// phone's app clone (xMopf7RF on X): turning it on and connecting, as
// "Use Claude Magpie" does, writes nothing of the user's own Desktop or
// ~/.claude, since its Code tab gets a Claude Code folder of its own, and
// turning it off leaves the user's own Desktop off magpie.
func TestClaudeMagpieTwinLeavesTheOriginal(t *testing.T) {
	home, path := desktopWindowSandbox(t)
	old := desktopdir.OS
	desktopdir.OS = "darwin"
	t.Cleanup(func() { desktopdir.OS = old })
	usual, iso, on, made := isolatedFakes(t, home)
	desktopWrite(t, usual.config, `{"mcpServers":{"fs":{"command":"npx"}}}`)
	desktopWrite(t, usual.app, `{"locale":"zh-CN"}`)
	before := desktopTree(t, usual)
	claudeWas, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	same := func(when string) {
		t.Helper()
		if after := desktopTree(t, usual); !reflect.DeepEqual(after, before) {
			t.Fatalf("%s, the user's own Desktop:\n%v\nwas:\n%v", when, after, before)
		}
		if b, _ := os.ReadFile(path); string(b) != string(claudeWas) {
			t.Fatalf("%s, ~/.claude/settings.json:\n%s\nwas:\n%s", when, b, claudeWas)
		}
	}

	if _, err := SetDesktopIsolated(home, true); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(home, ".claude-magpie-desktop")
	if !*on || !desktopIsolatedClaudeOn() || desktopIsolatedClaudeChosen() {
		t.Fatalf("on %v, its own folder %v, chosen %v", *on, desktopIsolatedClaudeOn(), desktopIsolatedClaudeChosen())
	}
	if !reflect.DeepEqual(*made, []string{own}) {
		t.Fatalf("app made with %q", *made)
	}
	if err := claudeDesktop(home).Field("provider").Set("magpie"); err != nil {
		t.Fatal(err)
	}
	if !desktopWired(iso) || desktopWiredAny(desktopUsualSets("darwin", home, os.Getenv)) {
		t.Fatal("connecting didn't wire Claude-Magpie-3p alone")
	}
	if w, _ := edit.GetJSON(filepath.Join(own, "settings.json"), "env."+claudeContextEnv); w == "" {
		t.Fatal("its Code tab's folder has no window")
	}
	same("on and connected")
	// the round that syncs every agent keeps it so
	if err := claude(home).Sync(); err != nil {
		t.Fatal(err)
	}
	same("synced")

	if _, err := SetDesktopIsolated(home, false); err != nil {
		t.Fatal(err)
	}
	if *on || desktopWired(iso) || desktopWiredAny(desktopUsualSets("darwin", home, os.Getenv)) {
		t.Fatalf("off: on %v, isolated %v; the user's own on magpie %v", *on, desktopWired(iso), desktopWiredAny(desktopUsualSets("darwin", home, os.Getenv)))
	}
	same("off")
}

// A new Claude Magpie's own Claude Code folder never overrides what was
// there: the user's off, a Claude Magpie made before (its chats in
// Claude-Magpie-3p, its Code tab on ~/.claude), or one on already.
func TestClaudeMagpieOwnFolderKeepsWhatWasSet(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, iso desktopPaths, on *bool)
	}{
		{"chosen off", func(t *testing.T, _ desktopPaths, _ *bool) {
			if err := saveDesktopIsolatedClaude(false, true); err != nil {
				t.Fatal(err)
			}
		}},
		{"made before", func(t *testing.T, iso desktopPaths, _ *bool) {
			if err := os.MkdirAll(iso.dir3p, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"on already", func(_ *testing.T, _ desktopPaths, on *bool) { *on = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, _, iso, on := isolatedSandbox(t)
			tc.setup(t, iso, on)
			if _, err := SetDesktopIsolated(home, true); err != nil {
				t.Fatal(err)
			}
			if desktopIsolatedClaudeOn() {
				t.Fatal("its own folder turned on")
			}
			if _, err := os.Stat(filepath.Join(home, ".claude-magpie-desktop")); err == nil {
				t.Fatal("folder made")
			}
		})
	}
}

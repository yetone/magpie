package library

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// fakeDshPlugins stands in for `dsh plugin add`: it lays the two packages
// down in the profile as pnpm would, at version ver, and counts the runs.
func fakeDshPlugins(t *testing.T, ver string, fail error) *[][]string {
	t.Helper()
	var runs [][]string
	was, wasRun := dshTool, dshPluginRun
	dshTool = func(*agent.Agent) (string, string) { return ver, "dsh" }
	dshPluginRun = func(bin, dir, cwd string, args ...string) (string, error) {
		runs = append(runs, append([]string{cwd}, args...))
		if fail != nil {
			return "", fail
		}
		for _, pkg := range []string{dshHooksPkg, dshHooksProtocol} {
			b, _ := json.Marshal(map[string]string{"name": pkg, "version": ver})
			write(t, filepath.Join(cwd, "node_modules", filepath.FromSlash(pkg), "package.json"), string(b))
		}
		return "", nil
	}
	t.Cleanup(func() {
		dshTool, dshPluginRun = was, wasRun
		dshInstallMu.Lock()
		clear(dshInstallFailed)
		dshInstallMu.Unlock()
	})
	return &runs
}

// A hook given to DeepSeek Harness lands in magpie's Claude-shaped hooks
// file under dsh's own tool names, the plugin that runs it is installed in
// every profile at dsh's version, and each profile's patch list gets one
// marked row naming the file. The user's rows stay; taking the last hook
// away takes the row away and leaves the user's rows.
func TestHooksDsh(t *testing.T) {
	h := sandbox(t)
	web := filepath.Join(h, ".dsh/profiles/web/cordis.patch.yml")
	other := filepath.Join(h, ".dsh/profiles/other/cordis.patch.yml")
	write(t, web, "- id: some-plugin\n  disabled: false\n")
	write(t, other, "[]\n")
	runs := fakeDshPlugins(t, "0.2.2-rc.5", nil)

	ok(t)(SaveHook("", Hook{Name: "guard", Event: "before-tool", Matcher: "shell", Command: "check.sh", Timeout: 5, Agents: []string{"dsh"}}))
	ok(t)(SaveHook("", Hook{Name: "fmt", Event: "after-tool", Matcher: "edit", Command: "prettier --write .", Agents: []string{"dsh"}}))

	file := filepath.Join(h, ".dsh", "magpie-hooks.json")
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type, Command string
				Timeout       int
			}
		}
	}
	if err := json.Unmarshal([]byte(read(t, file)), &doc); err != nil {
		t.Fatalf("%v\n%s", err, read(t, file))
	}
	if g := doc.Hooks["PreToolUse"]; len(g) != 1 || g[0].Matcher != "bash|pwsh" || g[0].Hooks[0].Command != "check.sh" || g[0].Hooks[0].Timeout != 5 {
		t.Errorf("PreToolUse: %+v", doc.Hooks["PreToolUse"])
	}
	if g := doc.Hooks["PostToolUse"]; len(g) != 1 || g[0].Matcher != "write|edit|str_replace_editor" {
		t.Errorf("PostToolUse: %+v", doc.Hooks["PostToolUse"])
	}
	if len(*runs) != 2 {
		t.Errorf("the plugin was installed %d times, want once a profile: %v", len(*runs), *runs)
	}
	row := "- insert: # magpie\n    - id: magpie-hooks\n      name: \"@deepseek-ai/dsh-hooks-claude-code\"\n      config:\n        configPath: " + dshScalar(file) + "\n"
	for _, p := range []string{web, other} {
		if s := read(t, p); strings.Count(s, "id: magpie-hooks\n") != 1 || !strings.Contains(s, row) {
			t.Errorf("%s:\n%s", p, s)
		}
	}
	if s := read(t, web); !strings.HasPrefix(s, "- id: some-plugin\n  disabled: false\n") {
		t.Errorf("the user's row moved:\n%s", s)
	}

	// a second hook installs nothing again and writes one row still
	ok(t)(SaveHook("", Hook{Name: "bark", Event: "turn-end", Command: "notify", Agents: []string{"dsh"}}))
	if len(*runs) != 2 || strings.Count(read(t, web), "id: magpie-hooks\n") != 1 {
		t.Errorf("runs %d\n%s", len(*runs), read(t, web))
	}

	ok(t)(RemoveHooks([]string{"guard", "fmt", "bark"}))
	if s := read(t, web); strings.Contains(s, "id: magpie-hooks\n") || !strings.Contains(s, "some-plugin") {
		t.Errorf("after removing every hook:\n%s", s)
	}
	if s := read(t, other); strings.Contains(s, "id: magpie-hooks\n") {
		t.Errorf("after removing every hook:\n%s", s)
	}
}

// With the plugin not installed, no row is written: dsh would warn of the
// row, leave it out, and run no hook, so a deny hook would allow everything.
// The failure is said, and the next sync installs it.
func TestHooksDshNeedsItsPlugin(t *testing.T) {
	h := sandbox(t)
	web := filepath.Join(h, ".dsh/profiles/web/cordis.patch.yml")
	write(t, web, "[]\n")
	fakeDshPlugins(t, "0.2.2-rc.5", errors.New("pnpm: no network"))

	res, err := SaveHook("", Hook{Name: "guard", Event: "before-tool", Command: "check.sh", Agents: []string{"dsh"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) == 0 || !strings.Contains(res.Problems[0].Error, "no network") {
		t.Fatalf("problems: %+v", res.Problems)
	}
	if s := read(t, web); strings.Contains(s, "id: magpie-hooks\n") {
		t.Fatalf("a row for a plugin that isn't there:\n%s", s)
	}
}

// A plugin of another version than dsh's is installed again: its peer
// dependencies are pinned to dsh's exact version.
func TestHooksDshReinstallsOnVersion(t *testing.T) {
	h := sandbox(t)
	web := filepath.Join(h, ".dsh/profiles/web/cordis.patch.yml")
	write(t, web, "[]\n")
	for _, pkg := range []string{dshHooksPkg, dshHooksProtocol} {
		write(t, filepath.Join(h, ".dsh/profiles/web/node_modules", filepath.FromSlash(pkg), "package.json"), `{"version":"0.2.1-rc.2"}`)
	}
	runs := fakeDshPlugins(t, "0.2.2-rc.5", nil)
	ok(t)(SaveHook("", Hook{Name: "guard", Event: "before-tool", Command: "check.sh", Agents: []string{"dsh"}}))
	if len(*runs) != 1 || !strings.Contains(strings.Join((*runs)[0], " "), dshHooksPkg+"@0.2.2-rc.5") {
		t.Fatalf("runs: %v", *runs)
	}
}

// A user's own row for the plugin is left as it is.
func TestHooksDshLeavesUsersRow(t *testing.T) {
	h := sandbox(t)
	web := filepath.Join(h, ".dsh/profiles/web/cordis.patch.yml")
	mine := "- insert:\n    - id: my-hooks\n      name: \"@deepseek-ai/dsh-hooks-claude-code\"\n      config:\n        configPath: /x/hooks.json\n"
	write(t, web, mine)
	fakeDshPlugins(t, "0.2.2-rc.5", nil)
	res, err := SaveHook("", Hook{Name: "guard", Event: "before-tool", Command: "check.sh", Agents: []string{"dsh"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) == 0 {
		t.Fatal("no problem said")
	}
	if s := read(t, web); s != mine {
		t.Fatalf("the user's row was changed:\n%s", s)
	}
}

// dsh has the Claude Code events its bridge runs and no more: a hook under
// another event is refused, not written where nothing reads it.
func TestHooksDshEvents(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".dsh/profiles/web/cordis.patch.yml"), "[]\n")
	fakeDshPlugins(t, "0.2.2-rc.5", nil)
	res, err := SaveHook("", Hook{Name: "n", Event: "dsh:Notification", Command: "x", Agents: []string{"dsh"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) == 0 || !strings.Contains(res.Problems[0].Error, "no event called Notification") {
		t.Fatalf("problems: %+v", res.Problems)
	}
	if read(t, filepath.Join(h, ".dsh", "magpie-hooks.json")) != "" && strings.Contains(read(t, filepath.Join(h, ".dsh", "magpie-hooks.json")), "Notification") {
		t.Fatal("written")
	}
}

// A user who removes the plugin from a profile finds no row of magpie's left
// pointing at it: the next sync can't install it again, and takes the row out.
func TestHooksDshRemovedPluginLeavesNoRow(t *testing.T) {
	h := sandbox(t)
	web := filepath.Join(h, ".dsh/profiles/web/cordis.patch.yml")
	write(t, web, "- id: some-plugin\n  disabled: false\n")
	fakeDshPlugins(t, "0.2.2-rc.5", nil)
	ok(t)(SaveHook("", Hook{Name: "guard", Event: "before-tool", Command: "check.sh", Agents: []string{"dsh"}}))
	if !strings.Contains(read(t, web), "id: magpie-hooks\n") {
		t.Fatal("no row to start from")
	}
	// the user removes the plugin, and the network is down
	if err := os.RemoveAll(filepath.Join(h, ".dsh/profiles/web/node_modules")); err != nil {
		t.Fatal(err)
	}
	fakeDshPlugins(t, "0.2.2-rc.5", errors.New("pnpm: no network"))
	res, err := SaveHook("guard", Hook{Name: "guard", Event: "before-tool", Command: "check2.sh", Agents: []string{"dsh"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) == 0 {
		t.Fatal("no problem said")
	}
	if s := read(t, web); strings.Contains(s, "id: magpie-hooks\n") || !strings.Contains(s, "some-plugin") {
		t.Fatalf("a row left pointing at a plugin that isn't there:\n%s", s)
	}
}

package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// CLJY (Discord): on Windows npm can't replace Claude Code while a copy of
// its claude.exe runs, and an update stopped partway and left only the sh
// shim, which Windows can't run. Before an npm update there, magpie lets go
// of what it keeps running of that CLI; and an update that leaves the CLI
// gone is a failure that says to quit it and reinstall, not "updated".
func TestUpdateCLIOnWindowsLetsGoFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts as the CLI")
	}
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	prefix := filepath.Join(tmp, "npm")
	pkgDir := filepath.Join(prefix, "lib/node_modules/@anthropic-ai/claude-code")
	pkgJSON := filepath.Join(pkgDir, "package.json")
	file(t, pkgJSON, `{"name":"@anthropic-ai/claude-code","version":"2.1.298"}`)
	file(t, filepath.Join(pkgDir, "bin/claude.exe"), "#!/bin/sh\necho '2.1.298 (Claude Code)'\n")
	file(t, filepath.Join(prefix, "bin/npm"), "#!/bin/sh\nexit 1\n")
	shim := filepath.Join(prefix, "bin/claude")
	link(t, "../lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe", shim)
	t.Setenv("PATH", filepath.Join(prefix, "bin"))
	t.Setenv("HOME", filepath.Join(tmp, "home"))

	oldRun, oldLetGo, oldGOOS := runUpdate, letGoBeforeUpdate, updateGOOS
	t.Cleanup(func() {
		runUpdate, letGoBeforeUpdate, updateGOOS = oldRun, oldLetGo, oldGOOS
		versions, latests = memo{}, memo{}
	})
	versions, latests = memo{}, memo{}
	latests.get("npm:@anthropic-ai/claude-code", "", 1<<40, 1<<40, func() (string, error) { return "2.1.299", nil })
	updateGOOS = "windows"
	var order []string
	letGoBeforeUpdate = func(_ context.Context, id string) { order = append(order, "let go of "+id) }
	runUpdate = func(_ context.Context, u *updater) ([]byte, error) {
		order = append(order, u.via)
		// npm's reify: the old shims removed, the new ones never written
		os.Remove(shim)
		os.WriteFile(pkgJSON, []byte(`{"name":"@anthropic-ai/claude-code","version":"2.1.299"}`), 0o644)
		return []byte("npm warn cleanup Failed to remove some directories\nchanged 1 package in 3s"), nil
	}
	a := &Agent{ID: "claude", Name: "Claude Code", Bin: "claude"}
	if c, ok := a.CLI(); !ok || c.Via != "npm" {
		t.Fatalf("before: %+v", c)
	}
	_, err := a.UpdateCLI()
	if got := strings.Join(order, ", "); got != "let go of claude, npm" {
		t.Errorf("ran %q; want magpie's own runs let go of before npm", got)
	}
	if err == nil || !strings.Contains(err.Error(), "is gone") {
		t.Fatalf("an update that left no claude to run: %v", err)
	}

	// elsewhere a running program doesn't hold its file: nothing let go of
	updateGOOS = "darwin"
	order = nil
	file(t, pkgJSON, `{"name":"@anthropic-ai/claude-code","version":"2.1.298"}`)
	link(t, "../lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe", shim)
	versions = memo{}
	runUpdate = func(_ context.Context, u *updater) ([]byte, error) {
		order = append(order, u.via)
		os.WriteFile(pkgJSON, []byte(`{"name":"@anthropic-ai/claude-code","version":"2.1.299"}`), 0o644)
		return nil, nil
	}
	a.UpdateCLI()
	if got := strings.Join(order, ", "); !strings.HasPrefix(got, "npm") || strings.Contains(got, "let go") {
		t.Errorf("ran %q off Windows", got)
	}
}

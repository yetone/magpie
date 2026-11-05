package agent

import (
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The agents whose npm package has an install script it needs: on
// 2026-11-05 `npm view <pkg> scripts` named a postinstall or install for
// each of these and for none of the others' packages. npm 12 runs one
// only for a package it is told to (--allow-scripts), as bun (--trust) and
// pnpm 10 (--allow-build) do; without it @opencode/cli 2.2.5 installs a
// stub that says "@opencode/cli's postinstall script was not run" (npm
// 12.2.2 and bun 1.4.2 on mote).
func TestScriptsAgents(t *testing.T) {
	var got []string
	for id, s := range cliSpecs {
		if s.scripts {
			got = append(got, id)
		}
	}
	sort.Strings(got)
	want := []string{"claude", "cline", "crush", "deveco", "mothx", "omo", "opencode", "snow"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scripts: %q, want %q", got, want)
	}
}

// Installing and updating such a package lets its own install script run,
// with each manager's flag, the version last; one without a script is
// installed as before.
func TestInstallLetsScriptsRun(t *testing.T) {
	cmds := func(id string) []string {
		var out []string
		for _, c := range installCommands(id, "linux", true) {
			if c.Via == "npm" {
				out = append(out, c.Command)
			}
		}
		return out
	}
	if got := cmds("crush"); !reflect.DeepEqual(got, []string{"npm install -g --allow-scripts=@charmland/crush @charmland/crush"}) {
		t.Errorf("crush: %q", got)
	}
	if got := cmds("codex"); !reflect.DeepEqual(got, []string{"npm install -g @openai/codex"}) {
		t.Errorf("codex: %q", got)
	}

	for _, c := range []struct {
		via     string
		scripts bool
		want    []string
	}{
		{"npm", true, []string{"npm", "install", "-g", "--allow-scripts=x", "x@latest"}},
		{"bun", true, []string{"bun", "add", "-g", "--trust", "x@latest"}},
		{"pnpm", true, []string{"pnpm", "add", "-g", "--allow-build=x", "x@latest"}},
		{"npm", false, []string{"npm", "install", "-g", "x@latest"}},
	} {
		base := []string{c.via, "add", "-g"}
		if c.via == "npm" {
			base = []string{"npm", "install", "-g"}
		}
		if got := withScripts(base, c.via, "x", c.scripts); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %v: %q, want %q", c.via, c.scripts, got, c.want)
		}
	}
}

// A real npm global of crush is updated with its script let run, and the
// way to mend it says so too.
func TestUpdateLetsScriptsRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Mac or Linux prefix")
	}
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	prefix := filepath.Join(tmp, "node")
	file(t, filepath.Join(prefix, "lib/node_modules/@charmland/crush/bin/crush"), "#!/bin/sh\n")
	npm := file(t, filepath.Join(prefix, "bin/npm"), "#!/bin/sh\n")
	crush := link(t, "../lib/node_modules/@charmland/crush/bin/crush", filepath.Join(prefix, "bin/crush"))
	u := howInstalled(cliSpecs["crush"], crush)
	if u == nil || !reflect.DeepEqual(u.cmd, []string{npm, "install", "-g", "--prefix", prefix, "--allow-scripts=@charmland/crush", "@charmland/crush@latest"}) {
		t.Fatalf("npm: %+v", u)
	}
	if h := mendHint(u, "0.80.0"); !strings.Contains(h, "--allow-scripts=@charmland/crush @charmland/crush@0.80.0 --include=optional") {
		t.Errorf("mend: %s", h)
	}
	// Codex's package has no script: nothing is let run
	file(t, filepath.Join(prefix, "lib/node_modules/@openai/codex/bin/codex.js"), "")
	codex := link(t, "../lib/node_modules/@openai/codex/bin/codex.js", filepath.Join(prefix, "bin/codex"))
	if u := howInstalled(cliSpecs["codex"], codex); u == nil || slices.ContainsFunc(u.cmd, func(a string) bool { return strings.HasPrefix(a, "--allow") }) {
		t.Errorf("codex: %+v", u)
	}
}

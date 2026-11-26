package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/library"
	"github.com/yetone/magpie/internal/testenv"
)

func pluginOut(t *testing.T, args ...string) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	err = libraryCmd(append([]string{"library", "plugin"}, args...))
	w.Close()
	os.Stdout = old
	out := <-done
	r.Close()
	return out, err
}

// magpie library plugin import (#7429, jinpwng): Find plugins and Install
// from a terminal, through the call the page makes. An extension-only Pi
// package is imported from a folder typed relative to the one you're in,
// listed, and given to a project with the project command; anything
// unknown is refused with nothing written, and --help prints usage.
func TestLibraryPluginImportCmd(t *testing.T) {
	proj := projectSandbox(t)
	h := os.Getenv("HOME")
	put := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	testenv.Program(t, filepath.Join(h, "bin", "pi"), "#!/bin/sh\n")
	put(filepath.Join(h, "bin", "pi.exe"), "")
	put(filepath.Join(h, ".pi/agent/settings.json"), "{}\n")
	guard := filepath.Join(filepath.Dir(proj), "pkgs", "guard")
	put(filepath.Join(guard, "package.json"), `{"name":"@acme/pi-guard","version":"1.2.0","pi":{"extensions":["./extension.ts"]}}`)
	put(filepath.Join(guard, "extension.ts"), "export default function (pi: any) {}\n")
	kit := filepath.Join(filepath.Dir(proj), "pkgs", "kit")
	put(filepath.Join(kit, ".claude-plugin/plugin.json"), `{"name":"kit","version":"1.0.0"}`)
	put(filepath.Join(kit, "skills/tdd/SKILL.md"), "---\nname: tdd\ndescription: Test first\n---\nWrite the test first.\n")
	market := filepath.Join(filepath.Dir(proj), "pkgs", "market")
	put(filepath.Join(market, ".claude-plugin/marketplace.json"), `{"name":"acme","plugins":[{"name":"one","source":"./one"},{"name":"two","source":"./two"}]}`)
	for _, n := range []string{"one", "two"} {
		put(filepath.Join(market, n, "skills", n+"-skill", "SKILL.md"), "---\nname: "+n+"-skill\ndescription: "+n+"\n---\n")
	}

	plugins := func() []library.PluginView {
		t.Helper()
		v, err := library.Read(nil)
		if err != nil {
			t.Fatal(err)
		}
		return v.Plugins
	}
	for _, bad := range [][]string{
		{"import"},
		{"import", "--evil"},
		{"import", "../pkgs/guard", "--yes"},
		{"import", "../pkgs/guard", "-x"},
		{"import", "../pkgs/guard", "agents=zz"},
		{"import", "../pkgs/guard", "agents=none", "agents=claude"},
		{"import", "../pkgs/guard", "ref=main"},
		{"import", "agents=none"},
		{"import", "../pkgs/market"},        // two plugins, none named
		{"import", "../pkgs/market", "zzz"}, // not one of them
		{"import", "../pkgs/missing"},
		{"nope"},
		{"list", "--yes"},
		{"list", "extra"},
	} {
		if out, err := pluginOut(t, bad...); err == nil {
			t.Errorf("library plugin %v was taken:\n%s", bad, out)
		}
	}
	if _, err := pluginOut(t, "import", "../pkgs/market"); err == nil || !strings.Contains(err.Error(), "one, two") {
		t.Errorf("a marketplace's plugins aren't named: %v", err)
	}
	if got := plugins(); len(got) != 0 {
		t.Fatalf("a refused command installed %+v", got)
	}
	for _, help := range [][]string{{"--help"}, {"help"}, {"import", "--help"}, {"import", "../pkgs/guard", "-h"}} {
		out, err := pluginOut(t, help...)
		if err != nil || !strings.Contains(out, "magpie library plugin import <source>") {
			t.Errorf("library plugin %v: %v\n%s", help, err, out)
		}
	}
	if got := plugins(); len(got) != 0 {
		t.Fatalf("--help installed %+v", got)
	}
	// what main prints for --help anywhere in the words, before the command runs
	for _, help := range [][]string{{"library", "plugin", "--help"}, {"library", "plugin", "import", "../pkgs/guard", "--help"}} {
		if !helpAsked(help) || !strings.Contains(helpFor(help), "magpie library plugin import <source>") {
			t.Errorf("magpie %v --help prints:\n%s", help, helpFor(help))
		}
	}

	must := func(args ...string) string {
		t.Helper()
		out, err := pluginOut(t, args...)
		if err != nil {
			t.Fatalf("library plugin %v: %v\n%s", args, err, out)
		}
		return out
	}
	if out := must("list"); !strings.Contains(out, "none yet") {
		t.Errorf("an empty list:\n%s", out)
	}
	if out := must("import", "../pkgs/guard", "agents=none"); !strings.Contains(out, "acme-pi-guard") {
		t.Errorf("import:\n%s", out)
	}
	got := plugins()
	if len(got) != 1 || got[0].Name != "acme-pi-guard" || len(got[0].Agents) != 0 || !slices.Equal(got[0].Extensions, []string{"extension.ts"}) {
		t.Fatalf("imported: %+v", got)
	}
	if want, _ := filepath.Abs(guard); got[0].Source != want {
		t.Errorf("source %q, want the whole path %q", got[0].Source, want)
	}

	// with no agents=, every agent the Library shows, as the page's Install
	must("import", kit)
	must("import", "../pkgs/market", "two")
	var listed struct {
		Plugins []struct {
			Name       string
			Agents     []string
			Skills     []string
			Extensions []string
		}
	}
	if err := json.Unmarshal([]byte(must("list", "--json")), &listed); err != nil {
		t.Fatal(err)
	}
	byName := map[string][]string{}
	for _, p := range listed.Plugins {
		byName[p.Name] = p.Agents
	}
	if len(listed.Plugins) != 3 || len(byName["acme-pi-guard"]) != 0 || !slices.Contains(byName["kit"], "claude") || !slices.Contains(byName["two"], "claude") {
		t.Fatalf("list: %+v", listed.Plugins)
	}
	if _, err := os.Stat(filepath.Join(h, ".claude/skills/tdd/SKILL.md")); err != nil {
		t.Error("kit's skill isn't given to Claude Code")
	}
	if out := must(); !strings.Contains(out, "acme-pi-guard") || !strings.Contains(out, "1 Pi extension") {
		t.Errorf("list:\n%s", out)
	}

	// the round trip: the imported package given to a project
	if out, err := projectOut(t, "add", "."); err != nil {
		t.Fatalf("project add: %v\n%s", err, out)
	}
	if out, err := projectOut(t, "plugin", "on", ".", "acme-pi-guard", "agents=pi"); err != nil {
		t.Fatalf("project plugin on: %v\n%s", err, out)
	}
	b, _ := os.ReadFile(filepath.Join(proj, ".pi", "settings.json"))
	// a Windows path is written with its backslashes escaped: \\
	if !strings.Contains(strings.ReplaceAll(string(b), `\\`, "/"), "plugins/acme-pi-guard") {
		t.Errorf(".pi/settings.json has no entry for the package:\n%s", b)
	}
}

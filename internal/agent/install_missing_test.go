package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// An agent whose settings are left but whose CLI is gone (#843: dsh
// uninstalled, ~/.dsh kept) is still listed, and Install another agent
// offers it again, marked; one found where users' tools go though not on
// PATH (#839) isn't missing, nor one whose folder an app keeps too.
func TestCLIMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	// no desktop app either, whatever this machine has installed
	was := appFolders
	appFolders = func() []string { return nil }
	t.Cleanup(func() { appFolders = was })
	bin := "magpie-test-gone-dsh"
	dir := filepath.Join(home, ".dsh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dsh := &Agent{ID: "dsh", Name: "DeepSeek Harness", Icon: "deepseek-color", Bin: bin, Dir: dir, Path: filepath.Join(dir, "config.yaml")}
	codex := &Agent{ID: "codex", Name: "Codex", Bin: "magpie-test-gone-codex", Dir: dir, Path: filepath.Join(dir, "config.toml")}
	never := &Agent{ID: "crush", Name: "Crush", Bin: "magpie-test-gone-crush", Dir: filepath.Join(home, "nope"), Path: filepath.Join(home, "nope", "crush.json")}
	wsl := &Agent{ID: "dsh@wsl:Ubuntu", Name: "DeepSeek Harness", Bin: bin, WSL: "Ubuntu", Dir: dir, Path: filepath.Join(dir, "config.yaml")}
	if !dsh.Detected() || !dsh.CLIMissing() {
		t.Fatalf("dsh with ~/.dsh and no dsh: detected %v, missing %v", dsh.Detected(), dsh.CLIMissing())
	}
	if codex.CLIMissing() || never.CLIMissing() || wsl.CLIMissing() {
		t.Fatalf("missing: codex %v, crush not here %v, wsl %v", codex.CLIMissing(), never.CLIMissing(), wsl.CLIMissing())
	}
	got := installsOf([]*Agent{dsh, codex, never, wsl}, "darwin", true)
	ids := map[string]Install{}
	for _, x := range got {
		ids[x.ID] = x
	}
	if x, ok := ids["dsh"]; !ok || !x.Missing || len(x.Commands) != 1 || x.Commands[0].Command != "npm install -g @deepseek-ai/dsh" {
		t.Fatalf("dsh offered as %+v (all: %+v)", x, got)
	}
	if x, ok := ids["crush"]; !ok || x.Missing {
		t.Fatalf("crush, not here at all, offered as %+v", x)
	}
	if _, ok := ids["codex"]; ok {
		t.Fatal("codex, its folder here, offered")
	}

	// installed in ~/.local/bin, which magpie's PATH hasn't: found
	local := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	name := bin
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	testenv.Program(t, filepath.Join(local, name), "#!/bin/sh\n")
	if dsh.CLIMissing() {
		t.Fatal("dsh in ~/.local/bin taken for missing")
	}
	for _, x := range installsOf([]*Agent{dsh}, "darwin", true) {
		t.Fatalf("installed dsh offered: %+v", x)
	}
}

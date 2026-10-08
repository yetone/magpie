package library

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/testenv"
)

// A file where an agent keeps its folder (~/.dsh, another tool's) is not
// that agent, even with a dsh of another tool's on PATH: the library
// neither lists it nor writes it, and says nothing went wrong with it.
// Before, every change said "open ~/.dsh/AGENTS.md: not a directory".
func TestAgentHomeTakenByAFile(t *testing.T) {
	h := sandbox(t)
	os.RemoveAll(filepath.Join(h, ".dsh"))
	write(t, filepath.Join(h, ".dsh"), "ls -la\n")
	if runtime.GOOS != "windows" {
		bin := filepath.Join(h, "bin")
		testenv.Program(t, filepath.Join(bin, "dsh"), "#!/bin/sh\n")
		t.Setenv("PATH", bin)
	}
	a, err := agent.Find("dsh")
	if err != nil {
		t.Fatal(err)
	}
	if a.Detected() {
		t.Error("DeepSeek Harness detected with ~/.dsh a file")
	}
	if slices.Contains(ids(Targets()), "dsh") {
		t.Errorf("dsh is a target: %v", ids(Targets()))
	}

	// a part of an agent that is there, where a file is in the way: that
	// part is left out, the rest given
	write(t, filepath.Join(h, ".gemini/skills"), "")
	g := targetByID("gemini")
	if g == nil || g.Skills != "" || g.Instructions == "" || g.MCP == nil {
		t.Errorf("gemini target: %+v", g)
	}

	shared := "Use tabs."
	ok(t)(SaveInstructions(InstructionsChange{Shared: &shared, Agents: []string{"claude", "gemini", "dsh"}}))
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Agents: []string{"claude", "gemini"}}))
	ok(t)(Sync())
	if b, err := os.ReadFile(filepath.Join(h, ".dsh")); err != nil || string(b) != "ls -la\n" {
		t.Errorf("~/.dsh: %q %v", b, err)
	}
	v, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, av := range v.Agents {
		if av.ID == "dsh" {
			t.Errorf("the page lists dsh: %+v", av)
		}
	}
}

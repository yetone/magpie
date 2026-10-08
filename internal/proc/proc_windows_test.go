package proc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHideWithoutConsole(t *testing.T) {
	defer func(f func() bool) { hasConsole = f }(hasConsole)

	hasConsole = func() bool { return false }
	if cmd := Command("cmd", "/c", "ver"); cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("no console: CREATE_NO_WINDOW not set: %+v", cmd.SysProcAttr)
	}
	cmd := CommandContext(context.Background(), "cmd", "/c", "ver")
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("no console: CREATE_NO_WINDOW not set: %+v", cmd.SysProcAttr)
	}
	if out, err := cmd.Output(); err != nil || len(out) == 0 {
		t.Fatalf("a hidden child's output should still be piped back: %q, %v", out, err)
	}

	hasConsole = func() bool { return true }
	if cmd := Command("cmd", "/c", "ver"); cmd.SysProcAttr != nil {
		t.Fatalf("with a console the child should share it: %+v", cmd.SysProcAttr)
	}
}

// A .cmd in a folder with a space, given an argument with a space, hands
// the program it runs the arguments as given: npm.cmd under nvm-windows'
// C:\Program Files\nodejs, installing into that same folder, had cmd.exe
// run "C:\Program" instead (wztlink1013 on Discord). The script passes %*
// on, as npm.cmd does to node, to this test binary, which prints what it
// was given. A " in an argument isn't among them: it comes back doubled to
// Go's parser, which reads "" the way C runtimes did before 2008.
func TestBatchFileInFolderWithSpace(t *testing.T) {
	if os.Getenv("MAGPIE_PROC_ECHO") == "1" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "Program Files", "nodejs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "@echo off\r\n\"" + self + "\" -test.run=TestEchoArgs -- %*\r\n"
	for _, name := range []string{"npm.cmd", "tool.bat"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	args := []string{"install", "-g", "--prefix", dir, "@anthropic-ai/claude-code@latest",
		"x&y", "a b|c", "%PATH%", `C:\a dir\`, `C:\b\`, "", "^(<>)!,;=", "中文 路径"}
	want, _ := json.Marshal(args)
	t.Setenv("MAGPIE_PROC_ECHO", "1")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, name := range []string{filepath.Join(dir, "npm.cmd"), filepath.Join(dir, "tool.bat"), "npm"} {
		for _, cmd := range []*exec.Cmd{Command(name, args...), CommandContext(context.Background(), name, args...)} {
			out, err := cmd.CombinedOutput()
			got, _, _ := strings.Cut(string(out), "\n")
			if err != nil || got != string(want) {
				t.Errorf("%s: got %q, %v; want %s", name, out, err, want)
			}
		}
	}
}

// TestEchoArgs is the program TestBatchFileInFolderWithSpace's scripts run.
func TestEchoArgs(t *testing.T) {
	if os.Getenv("MAGPIE_PROC_ECHO") != "1" {
		t.Skip("run by TestBatchFileInFolderWithSpace")
	}
	i := slices.Index(os.Args, "--")
	b, _ := json.Marshal(append([]string{}, os.Args[i+1:]...))
	fmt.Fprintf(os.Stdout, "%s\n", b)
	os.Exit(0)
}

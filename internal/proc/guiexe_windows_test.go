package proc

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openCodeInstall lays out OpenCode's desktop app as its installer puts it
// (%LOCALAPPDATA%\Programs\ai.opencode.desktop): OpenCode.exe, a GUI
// program, and resources\opencode-cli.exe, a console one. Both are copies
// of this test binary, which notes in ran that it was started
// (TestMain); OpenCode.exe has its subsystem set to GUI, as Electron's is.
func openCodeInstall(t *testing.T) (dir, ran string) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if guiPE(b) {
		t.Fatal("the test binary is already a GUI program")
	}
	root := t.TempDir()
	ran = filepath.Join(root, "ran")
	t.Setenv("MAGPIE_PROC_RAN", ran)
	dir = filepath.Join(root, "Programs", "ai.opencode.desktop")
	if err := os.MkdirAll(filepath.Join(dir, "resources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "resources", "opencode-cli.exe"), b, 0o755); err != nil {
		t.Fatal(err)
	}
	gui := append([]byte(nil), b...)
	opt := int(binary.LittleEndian.Uint32(gui[0x3c:])) + 4 + 20
	binary.LittleEndian.PutUint16(gui[opt+68:], peSubsystemGUI)
	if err := os.WriteFile(filepath.Join(dir, "OpenCode.exe"), gui, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, ran
}

// With OpenCode's install folder on PATH, `opencode` is OpenCode.exe
// (Windows looks names up without case), and a version probe never runs
// it: run, it opens OpenCode's window, which magpie's version check did at
// each start and after each update (saint carol on Discord). The CLI
// beside it still answers.
func TestProbeNeverOpensAWindowsApp(t *testing.T) {
	dir, ran := openCodeInstall(t)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := ProbeContext(ctx, "opencode", "--version")
	if !strings.EqualFold(filepath.Base(cmd.Path), "OpenCode.exe") {
		t.Fatalf("opencode found as %s, want OpenCode.exe", cmd.Path)
	}
	if out, err := cmd.Output(); err == nil {
		t.Fatalf("OpenCode.exe was run and said %q", out)
	}
	if v := Version(filepath.Join(dir, "OpenCode.exe")); v != "" {
		t.Fatalf("OpenCode.exe said version %q", v)
	}
	if b, _ := os.ReadFile(ran); len(b) != 0 {
		t.Fatalf("a probe started %s", b)
	}
	out, err := ProbeContext(ctx, filepath.Join(dir, "resources", "opencode-cli.exe"), "--version").Output()
	if err != nil || strings.TrimSpace(string(out)) != "2.2.5" {
		t.Fatalf("the bundled CLI answered %q, %v", out, err)
	}
	if b, _ := os.ReadFile(ran); !strings.Contains(string(b), "opencode-cli") || strings.Contains(strings.ToLower(string(b)), "opencode.exe") {
		t.Fatalf("ran %q, want only the CLI", b)
	}
}

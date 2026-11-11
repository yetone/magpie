package library

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func bunBiliSandbox(t *testing.T, bun string) (home, name string) {
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("APPDATA", filepath.Join(home, "AppData", "Roaming"))
	t.Setenv("BUN_INSTALL", bun)
	t.Setenv("PATH", t.TempDir())
	name = "bili"
	if runtime.GOOS == "windows" {
		name = "bili.exe"
	}
	return home, name
}

// Zerenjie on X: on Windows magpie 0.1.3693 didn't find billion-context
// installed with bun. `bun add -g` puts bili.exe (a shim, bili.bunx beside
// it) in the bin of BUN_INSTALL, and biliPath looked for bili.cmd on PATH
// and in npm's folder only. bun's folder, and an exe, count too.
func TestBiliPathFindsBunsInstall(t *testing.T) {
	bun := filepath.Join(t.TempDir(), "bun-home")
	_, name := bunBiliSandbox(t, bun)
	if got := biliPath(); got != "" {
		t.Fatalf("nothing installed, found %q", got)
	}
	if err := os.MkdirAll(filepath.Join(bun, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(bun, "bin", name)
	if err := os.WriteFile(want, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := biliPath(); got != want {
		t.Errorf("bun's bili: %q, want %q", got, want)
	}
}

// bun's default folder, ~/.bun/bin, with no BUN_INSTALL set.
func TestBiliPathFindsDefaultBun(t *testing.T) {
	home, name := bunBiliSandbox(t, "")
	dir := filepath.Join(home, ".bun", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, name)
	if err := os.WriteFile(want, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := biliPath(); got != want {
		t.Errorf("~/.bun/bin's bili: %q, want %q", got, want)
	}
}

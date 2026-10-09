package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// junction makes path a junction to the folder target, as install.ps1's
// New-Item -ItemType Junction does, in place of one already there.
func junction(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err == nil {
		if err := os.Remove(path); err != nil { // the junction, not what it leads to
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", path, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
}

// Codex's install.ps1 puts %LOCALAPPDATA%\Programs\OpenAI\Codex\bin on PATH,
// a junction to ~\.codex\packages\standalone\current\bin, and current is a
// junction to releases\<version>-<target>, retargeted by each update
// (xiaoxiaofeixz on Discord: the Agents page kept the old Codex version). It
// is Codex's own install, updated by codex update, and each update is a new
// binary to ask, though npm's archive gives every version's codex.exe the
// same time.
func TestCodexInstallPs1Junctions(t *testing.T) {
	home := t.TempDir()
	releases := filepath.Join(home, `.codex\packages\standalone\releases`)
	old := filepath.Join(releases, "0.161.0-x86_64-pc-windows-msvc")
	cur := filepath.Join(releases, "0.162.0-x86_64-pc-windows-msvc")
	when := time.Date(1985, 10, 26, 8, 15, 0, 0, time.UTC)
	for _, r := range []string{old, cur} {
		exe := file(t, filepath.Join(r, `bin\codex.exe`), "MZ")
		if err := os.Chtimes(exe, when, when); err != nil {
			t.Fatal(err)
		}
	}
	current := filepath.Join(home, `.codex\packages\standalone\current`)
	junction(t, old, current)
	visible := filepath.Join(home, `AppData\Local\Programs\OpenAI\Codex\bin`)
	junction(t, filepath.Join(current, "bin"), visible)
	bin := filepath.Join(visible, "codex.exe")

	if u := howInstalled(cliSpecs["codex"], bin); u == nil || u.via != "self" || u.pkg != "@openai/codex" || !reflect.DeepEqual(u.cmd, []string{bin, "update"}) {
		t.Errorf("install.ps1's codex: %+v", u)
	}

	says := "codex-cli 0.161.0"
	wasRun := runVersion
	runVersion = func(string) string { return says }
	t.Cleanup(func() { runVersion = wasRun })
	if v := installedVersion(bin); v != "0.161.0" {
		t.Fatalf("installed: %q", v)
	}
	// codex update: the new release, current retargeted to it
	junction(t, cur, current)
	says = "codex-cli 0.162.0"
	if v := installedVersion(bin); v != "0.162.0" {
		t.Errorf("after the update: %q, want 0.162.0", v)
	}
}

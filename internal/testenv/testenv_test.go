package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// A stand-in is the program a lookup finds, before an installed one of the
// same name further down PATH, on Windows too, where a file with no
// extension isn't a program: the gateway's tests make theirs with
// StandIns, and read the user's own Codex's version through it (#1827).
func TestStandInsComeBeforeTheInstalled(t *testing.T) {
	stand, installed := t.TempDir(), t.TempDir()
	if err := StandIns(stand, []string{"codex"}); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(installed, "codex")
	body := "#!/bin/sh\necho codex-cli 0.162.0\n"
	if runtime.GOOS == "windows" {
		real, body = real+".cmd", "@echo codex-cli 0.162.0\r\n"
	}
	if err := os.WriteFile(real, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stand+string(os.PathListSeparator)+installed)
	got, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(got) != stand {
		t.Fatalf("codex is %s, not the stand-in in %s", got, stand)
	}
	if out, err := exec.Command(got).Output(); err == nil || len(out) > 0 {
		t.Fatalf("the stand-in ran: %q, %v", out, err)
	}
}

func TestIsolate(t *testing.T) {
	for _, k := range append(folderVars, "XDG_CONFIG_DIRS", "CODEX_HOME", "PATH", Marker) {
		t.Setenv(k, os.Getenv(k)) // restored after the test
	}
	os.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "codex"))
	// the machine's system folders, where a package keeps its autostart entry
	os.Setenv("XDG_CONFIG_DIRS", "/etc/xdg")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9") // the machine's own, which net/http would freeze into its first read
	home, err := Isolate()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remove(home) })
	for _, k := range proxyVars {
		if _, ok := os.LookupEnv(k); ok {
			t.Errorf("%s is still set", k)
		}
	}
	for _, f := range []func() (string, error){os.UserHomeDir, os.UserConfigDir, os.UserCacheDir} {
		if p, err := f(); err != nil || !within(p, home) {
			t.Errorf("%q (%v) is outside %q", p, err, home)
		}
	}
	if p := os.Getenv("XDG_CONFIG_DIRS"); !within(p, home) {
		t.Errorf("XDG_CONFIG_DIRS is %q, outside %q", p, home)
	}
	if _, ok := os.LookupEnv("CODEX_HOME"); ok {
		t.Error("CODEX_HOME is still set")
	}
	remove(home)
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("the test home is still there: %v", err)
	}
}

// wsl.exe, which magpie runs by that name to list and open the WSL
// distros, is a failing stand-in under Isolate: the distros' homes lie
// outside the test home, and a Windows test read the real one's Codex
// sessions (#4177).
func TestIsolateStandsInForWSL(t *testing.T) {
	for _, k := range append(folderVars, "PATH", Marker) {
		t.Setenv(k, os.Getenv(k)) // restored after the test
	}
	home, err := Isolate()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remove(home) })
	got, err := exec.LookPath("wsl.exe")
	if err != nil || !within(got, home) {
		t.Fatalf("wsl.exe is %q (%v), not a stand-in in %s", got, err, home)
	}
	if out, err := exec.Command(got, "-l", "-q").Output(); err == nil || len(out) > 0 {
		t.Fatalf("the stand-in listed distros: %q, %v", out, err)
	}
}

// A binary started again by a test keeps that test's homes, all in the
// temporary folder; one pointing anywhere else is refused.
func TestCheckChild(t *testing.T) {
	sandbox, fixture := t.TempDir(), t.TempDir()
	for _, k := range append(folderVars, "CODEX_HOME") {
		t.Setenv(k, "")
	}
	SetHome(t, fixture)
	t.Setenv("CODEX_HOME", filepath.Join(fixture, ".codex"))
	if err := checkChild(sandbox); err != nil {
		t.Errorf("a fixture home in the temporary folder: %v", err)
	}
	outside, _ := filepath.Abs(filepath.Join(filepath.VolumeName(sandbox)+string(filepath.Separator), "not-temp", "home"))
	t.Setenv("CODEX_HOME", outside)
	if err := checkChild(sandbox); err == nil {
		t.Error("CODEX_HOME outside the temporary folder passed")
	}
	t.Setenv("CODEX_HOME", "")
	if err := checkChild(outside); err == nil {
		t.Error("a sandbox outside the temporary folder passed")
	}
}

func TestRemoveTouchesOnlyItsOwn(t *testing.T) {
	dir := t.TempDir()
	other := filepath.Join(dir, prefix+"x")
	os.MkdirAll(other, 0o755)
	remove(other) // not in os.TempDir() itself
	remove("")
	remove(".")
	if _, err := os.Stat(other); err != nil {
		t.Errorf("remove deleted a folder it didn't make: %v", err)
	}
}

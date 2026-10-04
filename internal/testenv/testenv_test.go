package testenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsolate(t *testing.T) {
	for _, k := range append(folderVars, "CODEX_HOME", "PATH", Marker) {
		t.Setenv(k, os.Getenv(k)) // restored after the test
	}
	os.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), "codex"))
	home, err := Isolate()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remove(home) })
	for _, f := range []func() (string, error){os.UserHomeDir, os.UserConfigDir, os.UserCacheDir} {
		if p, err := f(); err != nil || !within(p, home) {
			t.Errorf("%q (%v) is outside %q", p, err, home)
		}
	}
	if _, ok := os.LookupEnv("CODEX_HOME"); ok {
		t.Error("CODEX_HOME is still set")
	}
	remove(home)
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("the test home is still there: %v", err)
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

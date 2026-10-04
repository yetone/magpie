package appdir

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestHomeRefusesEmptyOrRelative(t *testing.T) {
	v := homeVar()
	for _, h := range []string{"", "relative/home", "."} {
		t.Setenv(v, h)
		if got, err := Home(); err == nil {
			t.Errorf("%s=%q: Home() = %q, want an error", v, h, got)
		}
		if _, err := CheckEnv(); err == nil {
			t.Errorf("%s=%q: CheckEnv() passed", v, h)
		}
	}
	abs := t.TempDir()
	t.Setenv(v, abs)
	if got, err := Home(); err != nil || got != abs {
		t.Errorf("Home() = %q, %v, want %q", got, err, abs)
	}
}

func TestConfigNeverRelative(t *testing.T) {
	t.Setenv("APPIMAGE", "")
	t.Cleanup(func() { UseExecutable("") })
	UseExecutable("")
	home := t.TempDir()
	t.Setenv(homeVar(), home)
	t.Setenv("XDG_CONFIG_HOME", "relative/cfg")
	t.Setenv("XDG_CACHE_HOME", "relative/cache")
	if got, want := Config(), filepath.Join(home, ".config", "magpie"); got != want {
		t.Errorf("Config() = %q, want %q", got, want)
	}
	if got, want := Cache(), filepath.Join(home, ".cache", "magpie"); got != want {
		t.Errorf("Cache() = %q, want %q", got, want)
	}
	t.Setenv(homeVar(), "")
	defer func() {
		if recover() == nil {
			t.Error("Config() with no home went on instead of stopping")
		}
	}()
	Config()
}

// A relative folder variable reads as unset inside magpie and stays in the
// environment for the programs magpie starts; one a login shell lends later
// (proc.UserPath sets it with os.Setenv) is read the same way.
func TestRelativeFolderVariablesIgnoredNotUnset(t *testing.T) {
	t.Setenv(homeVar(), t.TempDir())
	abs := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", abs)
	t.Setenv("XDG_DATA_HOME", "data")
	t.Setenv("CODEX_HOME", "./codex")
	t.Setenv("HANA_HOME", "~/.hanako")
	t.Setenv("PI_PROFILE", "work")
	t.Setenv("KIMI_SHARE_DIR", "")
	ignored, err := CheckEnv()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(ignored)
	if want := []string{"CODEX_HOME", "XDG_DATA_HOME"}; !slices.Equal(ignored, want) {
		t.Errorf("ignored %v, want %v", ignored, want)
	}
	for v, want := range map[string]string{"XDG_CONFIG_HOME": abs, "HANA_HOME": "~/.hanako", "PI_PROFILE": "work", "XDG_DATA_HOME": "", "CODEX_HOME": ""} {
		if got := Getenv(v); got != want {
			t.Errorf("Getenv(%s) = %q, want %q", v, got, want)
		}
	}
	if _, ok := LookupEnv("CODEX_HOME"); ok {
		t.Error("LookupEnv(CODEX_HOME) says set")
	}
	for v, want := range map[string]string{"XDG_DATA_HOME": "data", "CODEX_HOME": "./codex"} {
		if got := os.Getenv(v); got != want {
			t.Errorf("the environment's %s = %q, want %q kept for children", v, got, want)
		}
	}
	os.Setenv("CLAUDE_CONFIG_DIR", "claude") // as the shell import would
	t.Cleanup(func() { os.Unsetenv("CLAUDE_CONFIG_DIR") })
	if got := Getenv("CLAUDE_CONFIG_DIR"); got != "" {
		t.Errorf("a relative value lent by the shell read as %q", got)
	}
}

// On Windows a path rooted on the current drive is no working-folder path:
// \Users\x and Git Bash's /c/x are kept.
func TestRootedOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive-relative roots are Windows'")
	}
	t.Setenv(homeVar(), t.TempDir())
	for _, v := range []string{`\Users\x\.codex`, "/c/Users/x/.codex"} {
		t.Setenv("CODEX_HOME", v)
		if got := Getenv("CODEX_HOME"); got != v {
			t.Errorf("Getenv(CODEX_HOME) = %q, want %q kept", got, v)
		}
	}
	t.Setenv(homeVar(), `\Users\x`)
	if _, err := Home(); err != nil {
		t.Errorf(`a home of \Users\x: %v`, err)
	}
}

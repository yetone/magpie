package appdir

import (
	"os"
	"path/filepath"
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

func TestCheckEnvDropsRelativeFolders(t *testing.T) {
	t.Setenv(homeVar(), t.TempDir())
	abs := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", abs)
	t.Setenv("XDG_DATA_HOME", "data")
	t.Setenv("CODEX_HOME", "./codex")
	t.Setenv("HANA_HOME", "~/.hanako")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PI_PROFILE", "work")
	dropped, err := CheckEnv()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(dropped)
	if want := []string{"CODEX_HOME", "XDG_DATA_HOME"}; !slices.Equal(dropped, want) {
		t.Errorf("dropped %v, want %v", dropped, want)
	}
	for v, want := range map[string]string{"XDG_CONFIG_HOME": abs, "HANA_HOME": "~/.hanako", "PI_PROFILE": "work"} {
		if got := os.Getenv(v); got != want {
			t.Errorf("%s = %q, want %q kept", v, got, want)
		}
	}
	for _, v := range []string{"XDG_DATA_HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		if _, ok := os.LookupEnv(v); ok {
			t.Errorf("%s still set", v)
		}
	}
}

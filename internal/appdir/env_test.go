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
	t.Setenv("MAGPIE_ZED_CONFIG_DIR", "./zedg-config")
	t.Setenv("MAGPIE_ZED_BIN", "zedg")
	t.Setenv("MAGPIE_ZED_PROCESS_NAMES", "zedg,ZedG")
	t.Setenv("KIMI_SHARE_DIR", "")
	ignored, err := CheckEnv()
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(ignored)
	if want := []string{"CODEX_HOME", "MAGPIE_ZED_CONFIG_DIR", "XDG_DATA_HOME"}; !slices.Equal(ignored, want) {
		t.Errorf("ignored %v, want %v", ignored, want)
	}
	for v, want := range map[string]string{"XDG_CONFIG_HOME": abs, "HANA_HOME": "~/.hanako", "PI_PROFILE": "work", "MAGPIE_ZED_CONFIG_DIR": "", "MAGPIE_ZED_BIN": "zedg", "MAGPIE_ZED_PROCESS_NAMES": "zedg,ZedG", "XDG_DATA_HOME": "", "CODEX_HOME": ""} {
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

// A literal tilde is relative too, unless the variable's readers expand it.
func TestTildeFolderVariables(t *testing.T) {
	t.Setenv(homeVar(), t.TempDir())
	for _, tc := range []struct {
		name, value string
		keep        bool
	}{
		{"XDG_CONFIG_HOME", "~/.config", false},
		{"XDG_CACHE_HOME", "~/.cache", false},
		{"XDG_DATA_HOME", "~/.local/share", false},
		{"XDG_STATE_HOME", "~/.local/state", false},
		{"XDG_RUNTIME_DIR", "~/run", false},
		{"APPDATA", "~/AppData/Roaming", false},
		{"LOCALAPPDATA", "~/AppData/Local", false},
		{"MAGPIE_BIN_DIR", "~/.local/bin", false},
		{"CODEX_HOME", "~/.codex", false},
		{"OPENCODE_CONFIG_DIR", "~/opencode", false},
		{"OPENCHAMBER_DATA_DIR", "~", false},
		{"PI_CODING_AGENT_DIR", "~/pi", true},
		{"PI_CODING_AGENT_SESSION_DIR", "~/sessions", true},
		{"HANA_HOME", "~", true},
		{"T3CODE_HOME", "~/.t3", true},
		{"HANA_HOME", "~other/hanako", false},
		{"PI_CODING_AGENT_DIR", "~other/pi", false},
		{"PI_CODING_AGENT_DIR", `~\pi`, runtime.GOOS == "windows"},
		{"PRIME_AGENT_CODING_AGENT_DIR", "~", true},
		{"PRIME_AGENT_CODING_AGENT_DIR", "~/pa", true},
		{"PRIME_AGENT_CODING_AGENT_DIR", "~other/pa", false},
		{"PRIME_AGENT_CODING_AGENT_DIR", `~\pa`, runtime.GOOS == "windows"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			t.Setenv(tc.name, tc.value)
			want := ""
			if tc.keep {
				want = tc.value
			}
			if got, ok := LookupEnv(tc.name); got != want || ok != tc.keep {
				t.Errorf("LookupEnv(%s) = %q, %v; want %q, %v", tc.name, got, ok, want, tc.keep)
			}
			ignored, err := CheckEnv()
			if err != nil {
				t.Fatal(err)
			}
			if slices.Contains(ignored, tc.name) == tc.keep {
				t.Errorf("CheckEnv ignored %v; keep %s = %v", ignored, tc.name, tc.keep)
			}
			if got := os.Getenv(tc.name); got != tc.value {
				t.Errorf("environment's %s = %q, want %q kept for children", tc.name, got, tc.value)
			}
		})
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

//go:build !windows

package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// Settings' Command line (PAMI on Discord): which magpie each shell runs,
// and this app's put there — a link in the folder on PATH, over another
// copy that ran first, or in ~/.local/bin with the one shell's profile the
// user picked given that folder. Everything is in a temporary home; no
// shell is run.
func TestCLIOnPath(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("ZDOTDIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	exe := filepath.Join(h, "Applications", "magpie.app", "Contents", "MacOS", "magpie")
	bin := filepath.Join(h, ".local", "bin")
	other := filepath.Join(h, "other")
	for _, d := range []string{filepath.Dir(exe), bin, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	testenv.Program(t, exe, "#!/bin/sh\n")
	paths := map[string][]string{
		"/bin/zsh":  {bin, "/usr/bin"},
		"/bin/fish": {other, "/usr/bin"},
	}
	oldExe, oldShells, oldPath, oldStuck := cliExe, cliShells, cliShellPath, cliStuck
	t.Cleanup(func() { cliExe, cliShells, cliShellPath, cliStuck = oldExe, oldShells, oldPath, oldStuck })
	cliExe = func() (string, error) { return exe, nil }
	cliShells = func() []cliShell {
		return []cliShell{{Name: "zsh", Path: "/bin/zsh", Default: true}, {Name: "fish", Path: "/bin/fish"}}
	}
	cliShellPath = func(sh string) []string { return paths[sh] }
	cliStuck = func() string { return "" }

	// none yet: the folder on zsh's PATH takes it
	v := ReadCLI()
	if v.Ours || v.Command != "" || v.Dir != bin || !v.Shells[0].HasDir || v.Shells[1].HasDir {
		t.Fatalf("before: %+v", v)
	}
	if v.Shells[0].Profile != "~/.zshrc" || v.Shells[1].Profile != "~/.config/fish/config.fish" {
		t.Fatalf("profiles: %+v", v.Shells)
	}
	v, err := AddCLI("")
	if err != nil {
		t.Fatal(err)
	}
	if !v.Ours || v.Command != filepath.Join(bin, "magpie") || !v.Shells[0].Ours || v.Shells[1].Ours {
		t.Fatalf("after Add: %+v", v)
	}
	if l, err := os.Readlink(filepath.Join(bin, "magpie")); err != nil || l != exe {
		t.Fatalf("link = %q, %v", l, err)
	}
	if _, err := os.Stat(filepath.Join(h, ".zshrc")); !os.IsNotExist(err) {
		t.Fatal("a profile was written though the folder was on PATH")
	}

	// fish, picked, gets ~/.local/bin in its own profile, once
	for range 2 {
		if _, err := AddCLI("fish"); err != nil {
			t.Fatal(err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(h, ".config", "fish", "config.fish"))
	if strings.Count(string(b), "set -gx PATH $HOME/.local/bin $PATH") != 1 {
		t.Fatalf("config.fish:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(h, ".zshrc")); !os.IsNotExist(err) {
		t.Fatal("picking fish wrote zsh's profile")
	}

	// another copy that runs first is replaced by the link, as install.sh's ln -sf
	copied := filepath.Join(other, "magpie")
	testenv.Program(t, copied, "#!/bin/sh\necho old\n")
	paths["/bin/zsh"] = []string{other, bin}
	if v = ReadCLI(); v.Ours || v.Command != copied || v.Dir != other {
		t.Fatalf("shadowed: %+v", v)
	}
	if v, err = AddCLI(""); err != nil || !v.Ours {
		t.Fatalf("over the copy: %+v, %v", v, err)
	}
	if l, _ := os.Readlink(copied); l != exe {
		t.Fatalf("the copy is still there: %q", l)
	}

	// no folder on PATH: Add says so, and zsh picked gets its profile
	os.Remove(copied)
	os.Remove(filepath.Join(bin, "magpie"))
	paths["/bin/zsh"] = []string{"/usr/bin"}
	if v = ReadCLI(); v.Dir != "" {
		t.Fatalf("no folder: %+v", v)
	}
	if _, err := AddCLI(""); err == nil {
		t.Fatal("Add with no folder on PATH said nothing")
	}
	os.WriteFile(filepath.Join(h, ".zshrc"), []byte("alias ll='ls -l'"), 0o644)
	if _, err := AddCLI("zsh"); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(h, ".zshrc"))
	if !strings.HasPrefix(string(b), "alias ll='ls -l'\n") || !strings.Contains(string(b), "\n"+`export PATH="$HOME/.local/bin:$PATH"`+"\n") {
		t.Fatalf(".zshrc:\n%s", b)
	}
	if l, _ := os.Readlink(filepath.Join(bin, "magpie")); l != exe {
		t.Fatal("zsh picked didn't link the command in ~/.local/bin")
	}
	if _, err := AddCLI("nu"); err == nil {
		t.Fatal("a shell that isn't offered was written")
	}

	// a magpie.app elsewhere says its version without being run
	app := filepath.Join(h, "Old", "magpie.app", "Contents")
	os.MkdirAll(filepath.Join(app, "MacOS"), 0o755)
	testenv.Program(t, filepath.Join(app, "MacOS", "magpie"), "#!/bin/sh\nexit 1\n")
	os.WriteFile(filepath.Join(app, "Info.plist"), []byte("<dict><key>CFBundleShortVersionString</key>\n<string>0.1.500</string></dict>"), 0o644)
	if got := otherVersion(filepath.Join(app, "MacOS", "magpie")); got != "0.1.500" {
		t.Fatalf("version = %q", got)
	}

	// a translocated app is refused, nothing written
	cliStuck = func() string { return "translocated" }
	os.Remove(filepath.Join(bin, "magpie"))
	if _, err := AddCLI(""); err == nil {
		t.Fatal("a translocated app was linked")
	}
	if _, err := os.Lstat(filepath.Join(bin, "magpie")); !os.IsNotExist(err) {
		t.Fatal("a translocated app's link was made")
	}
}

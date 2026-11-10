//go:build !windows

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/update"
)

// Typing agy just works (bestony on Discord): picking one of magpie's
// models for agy gives the user's shells a function agy that starts it
// through magpie, the next start puts it back for a user wired before,
// and stepping out takes it out, the user's own lines as they were.
func TestAgyShell(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ZDOTDIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	mine := "alias ll='ls -l'\n"
	rcs := []string{".zshrc", ".bashrc", ".bash_profile"}
	for _, n := range rcs {
		os.WriteFile(filepath.Join(home, n), []byte(mine), 0o644)
	}
	a, err := Find("agy")
	if err != nil {
		t.Fatal(err)
	}
	if s := a.Start(); s.Env != nil || s.Model != "" {
		t.Fatalf("Start before: %+v", s)
	}
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	in := update.ShellFunctionIn("agy")
	if len(in) == 0 {
		t.Fatal("no shell's profile has agy")
	}
	for _, p := range in {
		b, _ := os.ReadFile(p)
		if s := string(b); !strings.HasPrefix(s, mine+"\n# >>> magpie agy >>>\n"+agyPosixHead) || !strings.Contains(s, ` agy run "$@"; else command agy "$@"; fi`) || !strings.HasSuffix(s, "}\n# <<< magpie agy <<<\n") {
			t.Fatalf("%s:\n%s", p, s)
		}
	}
	if a.Typed() != "agy" {
		t.Fatalf("typed: %q", a.Typed())
	}
	if n := a.Notice(); !strings.HasPrefix(n, "Type agy in a new terminal: magpie added it to ~/") || !strings.HasSuffix(n, "(in a terminal open now: "+a.Launch()+").") {
		t.Fatalf("notice: %q", n)
	}
	if s := a.Start(); s.Model != "magpie/deepseek/pro" || strings.Join(s.Env, " ") != "GEMINI_API_KEY="+gateway.TokenFor("agy")+" GOOGLE_GEMINI_BASE_URL="+gateway.URL() {
		t.Fatalf("Start: %+v", s)
	}

	// wired before the function was given: the next start gives it
	for _, p := range in {
		os.WriteFile(p, []byte(mine), 0o644)
	}
	if a.Typed() != "" {
		t.Fatal("typed with no function")
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := update.ShellFunctionIn("agy"); strings.Join(got, ",") != strings.Join(in, ",") {
		t.Fatalf("after Sync: %v, want %v", got, in)
	}

	// out: as the user had them
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	for _, n := range rcs {
		if b, _ := os.ReadFile(filepath.Join(home, n)); string(b) != mine {
			t.Fatalf("%s after stepping out:\n%s", n, b)
		}
	}
	if a.Typed() != "" {
		t.Fatal("typed after stepping out")
	}
	// and a start off magpie doesn't give it again
	a.Sync()
	if got := update.ShellFunctionIn("agy"); len(got) != 0 {
		t.Fatalf("Sync off magpie: %v", got)
	}
}

// The function in each shell runs this magpie's `agy run`, the agy on
// PATH when magpie is gone, its path quoted for that shell.
func TestAgyFunction(t *testing.T) {
	prog := "/Users/o'neil/magpie"
	for kind, want := range map[string]string{
		"posix":      `if [ -x '/Users/o'\''neil/magpie' ]; then '/Users/o'\''neil/magpie' agy run "$@"; else command agy "$@"; fi`,
		"fish":       `'/Users/o\'neil/magpie' agy run $argv`,
		"powershell": `foreach ($l in @(& '/Users/o''neil/magpie' agy env))`,
	} {
		if got := agyFunction(kind, prog); !strings.Contains(got, want) {
			t.Errorf("%s:\n%s\nwant it to have %s", kind, got, want)
		}
	}
}

//go:build !windows

package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agentenv"
)

// A desktop app with launchd's PATH finds a claude installed under a custom
// npm prefix: from the folders such tools use, and from the login shell's
// PATH, whatever its profile prints around it.
func TestUserPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	npm := filepath.Join(home, ".npm-global", "bin")
	os.MkdirAll(npm, 0o755)
	os.WriteFile(filepath.Join(npm, "claude"), []byte("#!/bin/sh\n"), 0o755)
	sh := filepath.Join(home, "sh")
	os.WriteFile(sh, []byte("#!/bin/sh\necho 'welcome back!'\nPATH=/from/profile:$PATH\neval \"$2\"\necho bye\n"), 0o755)
	t.Setenv("SHELL", sh)
	t.Setenv("PATH", "/usr/bin:/bin")

	UserPath()
	p := os.Getenv("PATH")
	if !strings.HasPrefix(p, "/usr/bin:/bin:") || !strings.Contains(p, npm) {
		t.Fatalf("known folders not added after the old PATH: %q", p)
	}
	for end := time.Now().Add(5 * time.Second); !strings.Contains(os.Getenv("PATH"), "/from/profile"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("the login shell's PATH never came: %q", os.Getenv("PATH"))
		}
	}
	if n := strings.Count(os.Getenv("PATH"), "/usr/bin:"); n != 1 {
		t.Fatalf("a folder PATH had was added again: %q", os.Getenv("PATH"))
	}
}

// A desktop app started from the Finder doesn't have the variables a shell
// profile exports either; the agents' folders among them come from the login
// shell by the time UserPath returns (atie on Discord: PI_CODING_AGENT_DIR in
// the shell, Pi's models.json still written to ~/.pi/agent). One magpie was
// started with is kept, and one the profile leaves empty isn't set.
func TestUserPathTakesAgentVars(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	pi := filepath.Join(home, "pi agent") // a space survives
	sh := filepath.Join(home, "sh")
	os.WriteFile(sh, []byte("#!/bin/sh\necho 'a profile that talks'\n"+
		"export PI_CODING_AGENT_DIR='"+pi+"'\nexport CODEX_HOME=/from/profile/codex\nexport GROK_HOME=\n"+
		"eval \"$2\"\necho bye\n"), 0o755)
	t.Setenv("SHELL", sh)
	t.Setenv("PATH", "/usr/bin:/bin")
	for _, v := range []string{"PI_CODING_AGENT_DIR", "GROK_HOME", "CLAUDE_CONFIG_DIR"} {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
	t.Setenv("CODEX_HOME", "/started/with")
	// the Mac checks a new script on its first run, which can take longer
	// than UserPath waits; a login shell is no new script
	exec.Command(sh, "-c", ":").Run()

	UserPath()
	if got := os.Getenv("PI_CODING_AGENT_DIR"); got != pi {
		t.Fatalf("PI_CODING_AGENT_DIR = %q, want the profile's %q", got, pi)
	}
	if got := os.Getenv("CODEX_HOME"); got != "/started/with" {
		t.Fatalf("CODEX_HOME = %q: the one magpie was started with was replaced", got)
	}
	for _, v := range []string{"GROK_HOME", "CLAUDE_CONFIG_DIR"} {
		if _, set := os.LookupEnv(v); set {
			t.Fatalf("%s was set though the shell has no value for it", v)
		}
	}
	if !strings.Contains(os.Getenv("PATH"), "/usr/bin") {
		t.Fatalf("PATH lost: %q", os.Getenv("PATH"))
	}
}

// A login shell that doesn't expand "$CLAUDE_CONFIG_DIR" prints the
// reference itself. That is no folder: the variable counts as unset,
// rather than magpie setting CLAUDE_CONFIG_DIR to "$CLAUDE_CONFIG_DIR" and
// then looking for Claude Code's sign-in under it (#738).
func TestParseShellEnvUnexpanded(t *testing.T) {
	s := shellMark + "$PATH"
	for _, v := range agentenv.Vars {
		s += "\x00$" + v
	}
	p, vars := parseShellEnv("hello from the profile\n"+s+shellMark, shellMark)
	if p != "" {
		t.Fatalf("PATH = %q, want none", p)
	}
	if len(vars) != len(agentenv.Vars) {
		t.Fatalf("%d variables, want %d", len(vars), len(agentenv.Vars))
	}
	for v, got := range vars {
		if got != "" {
			t.Fatalf("%s = %q, want none", v, got)
		}
	}
}

// nushell is asked in its own way, and answers with its PATH and the
// variables a child of it gets, as a POSIX shell does; where nushell isn't
// installed, the command's shape is all that can be checked.
func TestShellEnvNushell(t *testing.T) {
	probe := shellProbe("/opt/homebrew/bin/nu")
	if !strings.HasPrefix(probe, "^/bin/sh -c 'printf \""+shellMark+"%s") || !strings.HasSuffix(probe, `"$`+agentenv.Vars[len(agentenv.Vars)-1]+`"'`) {
		t.Fatalf("nushell's probe: %q", probe)
	}
	if strings.Contains(probe, `'`+shellMark) || strings.Count(probe, "'") != 2 {
		t.Fatalf("nushell's probe must hold sh's command in one pair of single quotes: %q", probe)
	}
	nu, err := exec.LookPath("nu")
	if err != nil {
		t.Skip("nushell isn't installed")
	}
	t.Setenv("CODEX_HOME", "/from/the/environment")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	os.Unsetenv("CLAUDE_CONFIG_DIR")
	out, err := exec.Command(nu, "-ilc", shellProbe(nu)).Output()
	if err != nil {
		t.Fatalf("nu: %v", err)
	}
	p, vars := parseShellEnv(string(out), shellMark)
	if p == "" || strings.Contains(p, "$PATH") {
		t.Fatalf("PATH from nushell: %q", p)
	}
	if got := vars["CODEX_HOME"]; got != "/from/the/environment" {
		t.Fatalf("CODEX_HOME = %q: nushell's environment didn't reach sh", got)
	}
	if got := vars["CLAUDE_CONFIG_DIR"]; got != "" {
		t.Fatalf("CLAUDE_CONFIG_DIR = %q, want none", got)
	}
}

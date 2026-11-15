package provider

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/claudecode"
	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/testenv"
)

// npmClaude installs a Claude Code of version v as npm does in a Node
// version's folder (nvm's layout: bin/claude a link to
// lib/node_modules/@anthropic-ai/claude-code/cli.js beside its
// package.json) and gives bin's folder. cli.js runs body.
func npmClaude(t *testing.T, root, v, body string) string {
	t.Helper()
	pkg := filepath.Join(root, "lib", "node_modules", "@anthropic-ai", "claude-code")
	bin := filepath.Join(root, "bin")
	for _, d := range []string{pkg, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@anthropic-ai/claude-code","version":"`+v+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	testenv.Program(t, filepath.Join(pkg, "cli.js"), body)
	if err := os.Symlink("../lib/node_modules/@anthropic-ai/claude-code/cli.js", filepath.Join(bin, "claude")); err != nil {
		t.Fatal(err)
	}
	return bin
}

// oldClaude is Claude Code 2.1.38 as zi_xiang5806 (X) had it first on
// PATH: its `auth login` has no --claudeai.
const oldClaude = "#!/bin/sh\n[ \"$1\" = --version ] && { echo '2.1.38 (Claude Code)'; exit 0; }\necho \"error: unknown option '--claudeai'\" >&2\nexit 1\n"

// claudeOnPath makes PATH these Claude Codes, first to last, and the
// system's own folders (none of the machine's own claude), and has the
// sign-in look Claude Code up as magpie does (claudecode.Find).
func claudeOnPath(t *testing.T, bins ...string) {
	t.Helper()
	t.Setenv("PATH", strings.Join(append(bins, "/usr/bin", "/bin"), string(os.PathListSeparator)))
	proc.Forget()
	t.Cleanup(proc.Forget)
	old := claudeExecutable
	claudeExecutable = claudecode.Find
	t.Cleanup(func() { claudeExecutable = old })
}

// With two Claude Codes on PATH, an old one first and the one `claude
// update` updated after it, the sign-in runs the newer one and adds the
// account, rather than failing on the old one's unknown --claudeai.
func TestClaudeSignInRunsTheNewestClaudeCode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh scripts and npm's Unix layout")
	}
	claudeHome(t)
	noAnthropic(t)
	exe := fakeClaudeLogin(t, fakeClaudeAccount{email: "newest@example.com", plan: "max", refresh: "sk-ant-ort01-newest"}, "", false)
	d := t.TempDir()
	oldBin := npmClaude(t, filepath.Join(d, "node18"), "2.1.38", oldClaude)
	newBin := npmClaude(t, filepath.Join(d, "node22"), "2.1.325", fmt.Sprintf("#!/bin/sh\nexec '%s' \"$@\"\n", exe))
	claudeOnPath(t, oldBin, newBin)

	st, err := StartSignIn("claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := SubmitSignInCallback(st.ID, "the-code#st"); err != nil {
		t.Fatal(err)
	}
	if st = waitDone(t, st.ID); st.State != "done" || st.User != "newest@example.com" {
		t.Fatalf("state %+v", st)
	}
}

// When the only Claude Code is too old for `auth login --claudeai`, the
// sign-in says so, with its version and where it is, and how to update it.
func TestClaudeSignInSaysClaudeCodeIsTooOld(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh scripts and npm's Unix layout")
	}
	claudeHome(t)
	noAnthropic(t)
	fakeClaudeLogin(t, fakeClaudeAccount{email: "x@example.com", plan: "pro", refresh: "sk-ant-ort01-x"}, "", false)
	oldBin := npmClaude(t, filepath.Join(t.TempDir(), "node18"), "2.1.38", oldClaude)
	claudeOnPath(t, oldBin)

	_, err := StartSignIn("claude")
	if err == nil {
		t.Fatal("signed in through a Claude Code with no --claudeai")
	}
	for _, want := range []string{"too old", "2.1.38", filepath.Join(oldBin, "claude"), "claude update", "unknown option '--claudeai'"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q doesn't say %q", err, want)
		}
	}
}

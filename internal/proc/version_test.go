package proc

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// A CLI is asked its version once per binary: one that fails to run (macOS
// stopping it as malware, #864) isn't started again until it changes, one
// cut off by the timeout is, and an npm CLI's is read from its package
// without running it.
func TestVersionRunsOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	bin := filepath.Join(dir, "codex")
	write := func(body string) {
		testenv.Program(t, bin, "#!/bin/sh\necho x >> '"+runs+"'\n"+body)
	}
	count := func() int {
		b, _ := os.ReadFile(runs)
		return strings.Count(string(b), "x")
	}
	write("exit 9\n")
	for range 3 {
		if v := Version(bin); v != "" {
			t.Fatalf("a failing CLI said %q", v)
		}
	}
	if n := count(); n != 1 {
		t.Fatalf("a failing CLI was run %d times", n)
	}
	// changed (reinstalled), it is asked again
	write("echo codex-cli 0.161.1\n")
	os.Chtimes(bin, time.Now().Add(time.Minute), time.Now().Add(time.Minute))
	if v := Version(bin); v != "codex-cli 0.161.1" {
		t.Fatalf("got %q", v)
	}
	if n := count(); n != 2 {
		t.Fatalf("run %d times", n)
	}

	// a timeout is asked again
	other := filepath.Join(dir, "slow")
	testenv.Program(t, other, "#!/bin/sh\n")
	old := runVersion
	t.Cleanup(func() { runVersion = old })
	asked := 0
	runVersion = func(string) (string, error) { asked++; return "", context.DeadlineExceeded }
	Version(other)
	Version(other)
	if asked != 2 {
		t.Fatalf("a timed-out CLI was asked %d times", asked)
	}

	// an npm CLI: the package says it, nothing is run
	asked = 0
	pkg := filepath.Join(dir, "lib", "node_modules", "@openai", "codex")
	os.MkdirAll(filepath.Join(pkg, "bin"), 0o755)
	os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@openai/codex","version":"0.162.0"}`), 0o644)
	os.WriteFile(filepath.Join(pkg, "bin", "codex.js"), []byte("#!/usr/bin/env node\n"), 0o755)
	link := filepath.Join(dir, "bin", "codex")
	os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(filepath.Join(pkg, "bin", "codex.js"), link); err != nil {
		t.Fatal(err)
	}
	if v := Version(link); v != "0.162.0" || asked != 0 {
		t.Fatalf("npm CLI: %q, run %d times", v, asked)
	}
}

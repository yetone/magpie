//go:build !windows

package proc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// openCodeBundle lays out OpenCode.app as its desktop app ships on a Mac:
// its own program OpenCode (the window), named by Info.plist's
// CFBundleExecutable, and a CLI beside it. Each program writes its name to
// ran when it is started.
func openCodeBundle(t *testing.T, plist string) (macos, ran string) {
	dir := t.TempDir()
	ran = filepath.Join(dir, "ran")
	contents := filepath.Join(dir, "Applications", "OpenCode.app", "Contents")
	macos = filepath.Join(contents, "MacOS")
	if err := os.MkdirAll(macos, 0o755); err != nil {
		t.Fatal(err)
	}
	if plist != "" {
		if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	testenv.Program(t, filepath.Join(macos, "OpenCode"), "#!/bin/sh\necho window >> '"+ran+"'\n")
	testenv.Program(t, filepath.Join(macos, "opencode-cli"), "#!/bin/sh\necho cli >> '"+ran+"'\necho 2.2.5\n")
	return macos, ran
}

const openCodePlist = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
	<key>CFBundleExecutable</key>
	<string>OpenCode</string>
	<key>CFBundleIdentifier</key>
	<string>ai.opencode.desktop</string>
</dict></plist>`

func probeVersion(t *testing.T, bin string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := ProbeContext(ctx, bin, "--version").Output()
	return strings.TrimSpace(string(out)), err
}

func ranOf(t *testing.T, ran string) string {
	b, err := os.ReadFile(ran)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// An opencode that is OpenCode.app's own program, reached through a link
// or a folder of the bundle on PATH, is never run by a version probe: run,
// it opens OpenCode's window, which magpie's version check did at each
// start and after each update (saint carol on Discord). The CLI beside it
// in the bundle is still asked.
func TestProbeNeverOpensAnApp(t *testing.T) {
	macos, ran := openCodeBundle(t, openCodePlist)
	bin := filepath.Join(t.TempDir(), "opencode")
	if err := os.Symlink(filepath.Join(macos, "OpenCode"), bin); err != nil {
		t.Fatal(err)
	}
	if _, err := probeVersion(t, bin); err == nil {
		t.Fatal("a link to the app's own program was run")
	}
	if v := Version(bin); v != "" {
		t.Fatalf("the app's own program said version %q", v)
	}
	// PATH holding the bundle's folder: on a Mac's case-insensitive disk
	// "opencode" finds OpenCode itself
	if _, err := os.Stat(filepath.Join(macos, "opencode")); err == nil {
		t.Setenv("PATH", macos)
		if _, err := probeVersion(t, "opencode"); err == nil {
			t.Fatal("opencode found in the bundle's folder was run")
		}
	}
	if got := ranOf(t, ran); got != "" {
		t.Fatalf("a probe started %q", got)
	}
	if v, err := probeVersion(t, filepath.Join(macos, "opencode-cli")); err != nil || v != "2.2.5" {
		t.Fatalf("the bundle's CLI answered %q, %v", v, err)
	}
	if got := ranOf(t, ran); got != "cli" {
		t.Fatalf("ran %q, want only the CLI", got)
	}
}

// A bundle whose Info.plist can't be read (none, or a binary one, as Kiro
// CLI.app's is): the program named as the bundle is its main one and is
// not run, the CLI beside it is.
func TestProbeUnreadPlistByName(t *testing.T) {
	for name, plist := range map[string]string{"none": "", "binary": "bplist00\x01\x02"} {
		t.Run(name, func(t *testing.T) {
			macos, ran := openCodeBundle(t, plist)
			if _, err := probeVersion(t, filepath.Join(macos, "OpenCode")); err == nil {
				t.Error("OpenCode was run")
			}
			if v, err := probeVersion(t, filepath.Join(macos, "opencode-cli")); err != nil || v != "2.2.5" {
				t.Errorf("the bundle's CLI answered %q, %v", v, err)
			}
			if got := ranOf(t, ran); got != "cli" {
				t.Fatalf("ran %q, want only the CLI", got)
			}
		})
	}
}

// ChatGPT.app ships its codex as CodexCLI.app's main executable, inside
// its own bundle: a CLI, and asked its version (Info.plist as 26.1027's).
func TestProbeRunsANestedAppsCLI(t *testing.T) {
	dir := t.TempDir()
	contents := filepath.Join(dir, "ChatGPT.app", "Contents", "Resources", "codex-cli", "CodexCLI.app", "Contents")
	if err := os.MkdirAll(filepath.Join(contents, "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
	<key>CFBundleDisplayName</key><string>ChatGPT</string>
	<key>CFBundleExecutable</key><string>codex</string>
	<key>CFBundleIdentifier</key><string>com.openai.codex.cli</string>
	<key>CFBundleName</key><string>ChatGPT</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleVersion</key><string>1</string>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	codex := filepath.Join(contents, "MacOS", "codex")
	testenv.Program(t, codex, "#!/bin/sh\necho codex-cli 0.166.0\n")
	if v, err := probeVersion(t, codex); err != nil || v != "codex-cli 0.166.0" {
		t.Fatalf("ChatGPT's codex answered %q, %v", v, err)
	}
}

// A program outside any bundle, or in a folder only named like one, runs.
func TestAppMainOnlyBundles(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{
		filepath.Join(dir, "bin", "opencode"),
		filepath.Join(dir, "MacOS", "opencode"),
		filepath.Join(dir, "Contents", "MacOS", "opencode"),
		filepath.Join(dir, "x.apps", "Contents", "MacOS", "opencode"),
		filepath.Join(dir, "X.app", "Contents", "MacOS", "x-cli"),
		filepath.Join(dir, "A.app", "Contents", "Frameworks", "X.app", "Contents", "MacOS", "X"),
	} {
		if appMain(p) {
			t.Errorf("%s taken for an app's own program", p)
		}
	}
	if !appMain(filepath.Join(dir, "X.app", "Contents", "MacOS", "X")) {
		t.Error("a bundle with no Info.plist: the program named as it was taken for a CLI")
	}
}

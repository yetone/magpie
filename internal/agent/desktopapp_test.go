package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/proc"
	"github.com/yetone/magpie/internal/testenv"
)

// #1823 (mdwsk88): HasApp read the developer's own /Applications, so on a
// Mac with DeepSeek Harness.app installed
// TestAsideOfflineDisconnectRestoresWithoutDaemon found dsh's app, and its
// CLI was never missing. Every folder magpie looks for installed apps in —
// the desktop apps', Pencil's, T3 Code's, Reasonix Studio's, Cursor Private
// Inference's — lies under the test's home, on every OS, and an app a test
// installs in the sandbox's /Applications is the one found.
func TestAppFoldersStayInTheTestHome(t *testing.T) {
	sandbox := os.Getenv(testenv.Marker)
	if sandbox == "" {
		t.Fatal("the package's tests aren't isolated (testenv)")
	}
	inside := func(what, dir string) {
		t.Helper()
		rel, err := filepath.Rel(sandbox, dir)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			t.Errorf("%s looks in %s, outside the test's home %s", what, dir, sandbox)
		}
	}
	for _, d := range appFolders() {
		inside("appFolders", d)
	}
	for _, d := range macAppFolders(sandbox) {
		inside("macAppFolders", d)
	}
	for _, d := range cursorLocalRoots() {
		inside("cursorLocalRoots", d)
	}
	if runtime.GOOS != "darwin" {
		return
	}
	dsh := &Agent{ID: "dsh", Name: "DeepSeek Harness", Bin: "dsh"}
	if dsh.HasApp() {
		t.Fatal("an empty sandbox has DeepSeek Harness.app")
	}
	apps := proc.SystemDirs("/Applications")[0]
	t.Cleanup(func() { os.RemoveAll(filepath.Join(apps, "DeepSeek Harness.app")) })
	installApp(t, apps, "DeepSeek Harness.app", codexAppPlist("com.deepseek.dsh", "0.2.0"))
	if v := dsh.AppVersion(); v != "0.2.0" {
		t.Fatalf("the sandbox's /Applications/DeepSeek Harness.app: %q", v)
	}
}

// codexAppPlist is the Codex app's Info.plist as macOS has it (ChatGPT.app
// 26.930.31730, its keys in their order), cut to the keys around the ones
// read, with the bundle id and version given: com.openai.chat stands for
// the ChatGPT chat app, installed under the same name.
func codexAppPlist(id, version string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>BundleSigningBaseName</key>
	<string>Codex</string>
	<key>CFBundleDisplayName</key>
	<string>ChatGPT</string>
	<key>CFBundleIdentifier</key>
	<string>` + id + `</string>
	<key>CFBundleName</key>
	<string>ChatGPT</string>
	<key>CFBundleShortVersionString</key>
	<string>` + version + `</string>
	<key>CFBundleVersion</key>
	<string>12947</string>
</dict>
</plist>
`
}

func installApp(t *testing.T, dir, name, plist string) {
	t.Helper()
	c := filepath.Join(dir, name, "Contents")
	if err := os.MkdirAll(c, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c, "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
}

// #1334 (laacmilan): with Codex's CLI and its desktop app both installed, the
// Agents page showed the CLI's version only. The app's version is read from
// its bundle — by its id, so the ChatGPT chat app of the same name isn't
// taken for it — and given with the CLI's, and alone where no CLI is on PATH.
func TestCodexAppVersion(t *testing.T) {
	apps, userApps := t.TempDir(), t.TempDir()
	was, wasStore := appFolders, windowsStorePackages
	appFolders = func() []string { return []string{apps, userApps} }
	windowsStorePackages = func() []string { return nil }
	t.Cleanup(func() { appFolders, windowsStorePackages = was, wasStore })
	t.Setenv("PATH", t.TempDir()) // no codex CLI
	codex := &Agent{ID: "codex", Name: "Codex", Bin: "codex"}

	if v := codex.AppVersion(); v != "" {
		t.Fatalf("no app installed: %q", v)
	}
	if c, ok := codex.CLI(); ok {
		t.Fatalf("neither CLI nor app, yet %+v", c)
	}

	installApp(t, apps, "ChatGPT.app", codexAppPlist("com.openai.chat", "1.2026.280"))
	if v := codex.AppVersion(); v != "" {
		t.Fatalf("the ChatGPT chat app taken for Codex's: %q", v)
	}

	installApp(t, userApps, "ChatGPT.app", codexAppPlist("com.openai.codex", "26.930.31730"))
	if v := codex.AppVersion(); v != "26.930.31730" {
		t.Fatalf("Codex app in ~/Applications: %q", v)
	}
	c, ok := codex.CLI()
	if !ok || c.App != "26.930.31730" || c.Version != "" {
		t.Fatalf("the app alone, no CLI: %+v %v", c, ok)
	}

	// with the CLI on PATH as well, both (a script stands for it, so not
	// on Windows)
	if runtime.GOOS != "windows" {
		bin := t.TempDir()
		t.Setenv("PATH", bin)
		wasRun := runVersion
		runVersion = func(string) string { return "codex-cli 0.130.0" }
		t.Cleanup(func() { runVersion = wasRun })
		if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		c, ok = codex.CLI()
		if !ok || c.Version != "0.130.0" || c.App != "26.930.31730" {
			t.Fatalf("CLI and app: %+v %v", c, ok)
		}
	}

	// an agent with no app magpie knows, and Codex's twin in WSL, have none
	if v := (&Agent{ID: "gemini", Bin: "gemini"}).AppVersion(); v != "" {
		t.Fatalf("gemini: %q", v)
	}
	if v := (&Agent{ID: "codex@wsl:Ubuntu", WSL: "Ubuntu", Bin: "codex"}).AppVersion(); v != "" {
		t.Fatalf("WSL twin: %q", v)
	}
}

// TestCodexWindowsStoreAppVersion: on Windows the Codex app is the Microsoft
// Store's OpenAI.Codex package (#1334, laacmilan: C:\Program Files\
// WindowsApps\OpenAI.Codex_26.1002.7124.0_x64__2p2nqsd0c76g0), read from the
// newest main package registered for the user.
func TestCodexWindowsStoreAppVersion(t *testing.T) {
	was, wasStore := appFolders, windowsStorePackages
	appFolders = func() []string { return nil }
	windowsStorePackages = func() []string {
		return []string{
			"OpenAI.Codex_not-a-version_x64__2p2nqsd0c76g0",
			"OpenAI.Codex_26.928.2636.0_x64__2p2nqsd0c76g0",
			"OpenAI.Codex_26.1002.7124.0_arm64__2p2nqsd0c76g0",
			"OpenAI.Codex_99.0.0.0_x64__wrong-publisher",
			// a resource package left behind, as the Windows box's registry
			// keeps Clipchamp's after its main package moved on
			"OpenAI.Codex_27.0.0.0_neutral_split.scale-125_2p2nqsd0c76g0",
		}
	}
	t.Cleanup(func() { appFolders, windowsStorePackages = was, wasStore })

	codex := &Agent{ID: "codex", Name: "Codex", Bin: "codex"}
	if v := codex.AppVersion(); v != "26.1002.7124.0" {
		t.Fatalf("Codex Store app: %q", v)
	}
	if !codex.HasApp() {
		t.Fatal("Codex Store app not detected")
	}
	if pkg, version := storeAppPackage(windowsStorePackages(), "OpenAI.Codex", "2p2nqsd0c76g0"); pkg != "OpenAI.Codex_26.1002.7124.0_arm64__2p2nqsd0c76g0" || version != "26.1002.7124.0" {
		t.Fatalf("the selected Store package and its version differ: %q %q", pkg, version)
	}
	if v := (&Agent{ID: "codex@wsl:Ubuntu", WSL: "Ubuntu", Bin: "codex"}).AppVersion(); v != "" {
		t.Fatalf("WSL twin: %q", v)
	}
}

// A test binary testenv isolated never reads the machine's Store apps, in
// whatever package it is: on a Windows machine with the Codex app installed,
// library's tests saw it and codex's CLI was never gone (#2254, ggbdpq).
func TestStorePackagesNotTheMachinesInTests(t *testing.T) {
	if !proc.Isolated() {
		t.Fatal("agent's tests run outside testenv's home")
	}
	if got := storePackages(); len(got) != 0 {
		t.Fatalf("a test read this machine's Store packages: %d of them, %v", len(got), got[:min(len(got), 3)])
	}
}

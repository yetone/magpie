package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

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
	was := appFolders
	appFolders = func() []string { return []string{apps, userApps} }
	t.Cleanup(func() { appFolders = was })
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

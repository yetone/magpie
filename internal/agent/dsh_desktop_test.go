package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dshAppPlist is DeepSeek Harness Desktop's Info.plist as its 0.2.0-rc.2
// macOS build ships it (download.deepseek.com's dsh-latest-macos-arm64.dmg),
// cut to the keys around the ones read, in their order and indentation.
const dshAppPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>CFBundleDisplayName</key>
    <string>DeepSeek Harness</string>
    <key>CFBundleExecutable</key>
    <string>DeepSeek Harness</string>
    <key>CFBundleIconFile</key>
    <string>icon.icns</string>
    <key>CFBundleIdentifier</key>
    <string>com.deepseek.dsh</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>CFBundleName</key>
    <string>DeepSeek Harness</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>0.2.0-rc.2</string>
    <key>CFBundleVersion</key>
    <string>0.2.0-rc.2</string>
  </dict>
</plist>
`

// dshDesktopPatch is the patch list dsh writes for a new profile
// (PROFILE_PATCH_TEMPLATE in the app's dsh-app-boot), which is what the
// desktop app's profiles/desktop/cordis.patch.yml holds after its first start.
const dshDesktopPatch = "# Your patch layer for this dsh profile, applied after every bundle layer:\n" +
	"# a top-level YAML array of loader patch entries (id-targeted config\n" +
	"# overrides, disables, and insert lists; `!!js` expressions allowed).\n" +
	"[]\n"

// star on Discord (2026-10-09): 有一个bug DeepSeek Harness 就是现在已经有桌面版
// 了，但是他还是非要找CLI. With DeepSeek Harness Desktop in use and no dsh
// CLI, the Agents page said 未找到 CLI and offered dsh's npm install. The
// desktop app keeps ~/.dsh as the CLI does and needs no CLI, so with it
// installed the folder is no sign of an uninstall: its version is shown
// instead, on macOS and Windows alike.
func TestDshDesktopAppIsNoMissingCLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("PATH", t.TempDir()) // no dsh CLI
	apps, userApps := t.TempDir(), t.TempDir()
	was := appFolders
	appFolders = func() []string { return []string{apps, userApps} }
	t.Cleanup(func() { appFolders = was })
	desktop := filepath.Join(home, ".dsh", "profiles", "desktop", "cordis.patch.yml")
	if err := os.MkdirAll(filepath.Dir(desktop), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktop, []byte(dshDesktopPatch), 0o644); err != nil {
		t.Fatal(err)
	}
	a := dsh(home)
	// a name no machine has: proc.FindTool also looks where users' tools go
	a.Bin = "magpie-test-gone-dsh"
	offered := func() bool {
		for _, x := range installsOf([]*Agent{a}, "darwin", true) {
			if x.ID == "dsh" {
				return true
			}
		}
		return false
	}

	// no app: ~/.dsh left without its CLI is still said (#843)
	if !a.Detected() || !a.CLIMissing() || !offered() {
		t.Fatalf("no app, no CLI: detected %v, missing %v, offered %v", a.Detected(), a.CLIMissing(), offered())
	}

	// the macOS app in ~/Applications
	installApp(t, userApps, "DeepSeek Harness.app", dshAppPlist)
	if a.CLIMissing() || offered() {
		t.Fatalf("with the desktop app: missing %v, offered %v", a.CLIMissing(), offered())
	}
	if c, ok := a.CLI(); !ok || c.App != "0.2.0-rc.2" || c.Version != "" {
		t.Fatalf("the desktop app's version: %+v %v", c, ok)
	}

	// another app under that name is not dsh's
	os.RemoveAll(filepath.Join(userApps, "DeepSeek Harness.app"))
	installApp(t, apps, "DeepSeek Harness.app", strings.Replace(dshAppPlist, "com.deepseek.dsh", "com.example.other", 1))
	if !a.CLIMissing() {
		t.Fatal("another bundle id taken for the desktop app")
	}

	// Windows: the per-user installer's DeepSeek Harness.exe
	exe := filepath.Join(apps, "DeepSeek Harness", "DeepSeek Harness.exe")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	if a.CLIMissing() || offered() {
		t.Fatalf("with the Windows app: missing %v, offered %v", a.CLIMissing(), offered())
	}

	// dsh in a WSL distro is not the Windows app's
	if (&Agent{ID: "dsh@wsl:Ubuntu", WSL: "Ubuntu", Bin: "dsh"}).HasApp() {
		t.Fatal("WSL twin has the Windows app")
	}
}

// The desktop app alone, its profile as its first start leaves it: magpie's
// model set goes into that profile and dsh's key store, and nothing reads it
// as no longer going through magpie.
func TestDshDesktopOnlyWired(t *testing.T) {
	home, dir, _ := dshRouteHome(t)
	t.Setenv("PATH", t.TempDir())
	desktop := filepath.Join(dir, "profiles", "desktop", "cordis.patch.yml")
	if err := os.MkdirAll(filepath.Dir(desktop), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktop, []byte(dshDesktopPatch), 0o644); err != nil {
		t.Fatal(err)
	}
	a := dsh(home)
	if a.Path != desktop {
		t.Fatalf("edits %s, not the desktop app's profile", a.Path)
	}
	if err := a.Field("model").Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(desktop)
	if s := string(b); !strings.Contains(s, "apiKeyEnv: "+dshKeyRef) || !strings.Contains(s, "provider: "+dshRoute) {
		t.Fatalf("the desktop profile has no route or start:\n%s", s)
	}
	if v, _ := os.ReadFile(filepath.Join(dir, ".credentials.yaml")); !strings.Contains(string(v), dshKeyRef+": ") {
		t.Fatalf("no key in dsh's store:\n%s", v)
	}
	if got := a.Field("model").Get(); got != "magpie/deepseek/flash" {
		t.Fatalf("model reads %q", got)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("check: %s", d)
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift: %+v", d)
	}
}

// The desktop app first opened while magpie runs, after the CLI's web
// profile was wired: its new profile, dsh's empty template, listed none of
// magpie's models, and the Agents page said DeepSeek Harness no longer goes
// through magpie until Reapply or magpie's next start (only the catalog sync
// filled new profiles). The serving round now fills a profile dsh has just
// made; one with entries of its own, the user's, is still left to Reapply.
func TestDshDesktopProfileMadeWhileServing(t *testing.T) {
	home, dir, web := dshRouteHome(t)
	if err := os.WriteFile(web, []byte(dshDesktopPatch), 0o644); err != nil {
		t.Fatal(err)
	}
	a := dsh(home)
	if err := a.Field("model").Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("web wired: %s", d)
	}
	desktop := filepath.Join(dir, "profiles", "desktop", "cordis.patch.yml")
	if err := os.MkdirAll(filepath.Dir(desktop), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktop, []byte(dshDesktopPatch), 0o644); err != nil {
		t.Fatal(err)
	}
	if d := a.Check(); !strings.Contains(d, "desktop profile") {
		t.Fatalf("the new desktop profile not said: %q", d)
	}
	if trouble := dshWiredOnce(); trouble != "" {
		t.Fatal(trouble)
	}
	b, _ := os.ReadFile(desktop)
	if s := string(b); !strings.Contains(s, "apiKeyEnv: "+dshKeyRef) || !strings.Contains(s, "provider: "+dshRoute) {
		t.Fatalf("the round left the desktop profile bare:\n%s", s)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("after the round: %s", d)
	}

	// a profile with the user's own entries and no route is theirs: the
	// round doesn't put magpie in it
	own := "- id: agent-default-model\n  config:\n    provider: deepseek-official\n    model: deepseek-v4-pro\n"
	if err := os.WriteFile(desktop, []byte(own), 0o644); err != nil {
		t.Fatal(err)
	}
	if trouble := dshWiredOnce(); trouble != "" {
		t.Fatal(trouble)
	}
	if b, _ := os.ReadFile(desktop); string(b) != own {
		t.Fatalf("the user's profile written:\n%s", b)
	}
}

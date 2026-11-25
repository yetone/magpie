package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/yetone/magpie/internal/proc"
)

// dshNextPlist is the Info.plist of DSH NEXT 2.0.24-next's macOS build
// (DSH-NEXT-2.0.24-next-arm64.dmg from github.com/anywhere-labs/
// dsh-desktop's release v2.0.24-next, sha256 059e312f… as its
// SHA256SUMS.txt says), cut to the keys around the ones read, in their
// order and indentation.
const dshNextPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>CFBundleDisplayName</key>
    <string>DSH NEXT</string>
    <key>CFBundleExecutable</key>
    <string>DSH NEXT</string>
    <key>CFBundleIdentifier</key>
    <string>ai.deepseek.dsh.desktop.next</string>
    <key>CFBundleName</key>
    <string>DSH NEXT</string>
    <key>CFBundleShortVersionString</key>
    <string>2.0.24-next</string>
    <key>CFBundleVersion</key>
    <string>2.0.24-next</string>
  </dict>
</plist>
`

// dshNextManifest is that bundle's Contents/Resources/app/package.json,
// cut to the fields around the dsh it runs.
const dshNextManifest = `{
  "name": "dsh-desktop-next",
  "version": "2.0.24-next",
  "main": "lib/main.js",
  "dependencies": {
    "@deepseek-ai/cordis": "4.2.0-alpha.1",
    "@deepseek-ai/dsh": "0.2.2-rc.5",
    "@deepseek-ai/dsh-agent": "0.2.2-rc.5"
  }
}
`

// tresgrosplan on X (magpie 0.1.4762, macOS 27.2.1): magpie didn't detect
// "DeepSeek Harness NEXT", the community's DSH NEXT (anywhere-labs/
// dsh-desktop). It runs dsh on ~/.dsh and its profile desktop, as
// DeepSeek Harness Desktop does, so it is dsh's app: with it alone, no
// CLI and no ~/.dsh, the dsh row is listed, not gone, and wires the
// desktop profile. So is the same vendor's DSH Desktop. Another app under
// the name is not, nor is DSH Desktop Beta, which keeps ~/.dsh-beta.
func TestDshCommunityShells(t *testing.T) {
	judgeFor(t, "darwin")
	home, dir, apps, reg := dshOfficialHome(t)
	gone := func() *Agent {
		a := dsh(home)
		a.Bin = "magpie-test-gone-dsh"
		return a
	}
	if a := gone(); a.Detected() || a.HasApp() || dshAppsVersion() != "" {
		t.Fatalf("nothing installed: detected %v, app %v, dsh %q", a.Detected(), a.HasApp(), dshAppsVersion())
	}

	// DSH NEXT.app as its dmg has it
	next := filepath.Join(apps, "DSH NEXT.app")
	installApp(t, apps, "DSH NEXT.app", dshNextPlist)
	writeT(t, filepath.Join(next, "Contents", "Resources", "app", "package.json"), dshNextManifest)
	a := gone()
	if !a.Detected() || a.CLIMissing() || !a.HasApp() {
		t.Fatalf("DSH NEXT 2.0.24-next: detected %v, CLI missing %v, app %v", a.Detected(), a.CLIMissing(), a.HasApp())
	}
	if want := filepath.Join(dir, "profiles", "desktop", "cordis.patch.yml"); a.Path != want {
		t.Fatalf("names %s, not the desktop profile", a.Path)
	}
	if v := a.AppVersion(); v != "2.0.24-next" {
		t.Fatalf("app version %q", v)
	}
	if c, ok := a.CLI(); !ok || c.App != "2.0.24-next" {
		t.Fatalf("the app's version not shown: %+v %v", c, ok)
	}
	// the dsh it runs is what says whether Gemini's route can be written
	if v := dshAppsVersion(); v != "0.2.2-rc.5" {
		t.Fatalf("dsh in DSH NEXT %q", v)
	}
	// connecting wires the profile it starts on
	if err := a.Field("model").Set("magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(a.Path)
	if s := string(b); !strings.Contains(s, "provider: "+dshRoute) || !strings.Contains(s, "apiKeyEnv: "+dshKeyRef) {
		t.Fatalf("the desktop profile has no route:\n%s", s)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	// a range, or no manifest, is a dsh not known: too old for Gemini
	writeT(t, filepath.Join(next, "Contents", "Resources", "app", "package.json"), strings.Replace(dshNextManifest, `"@deepseek-ai/dsh": "0.2.2-rc.5"`, `"@deepseek-ai/dsh": "^0.2.2"`, 1))
	if v := dshAppsVersion(); v != "unknown" {
		t.Fatalf("a range read as %q", v)
	}
	os.Remove(filepath.Join(next, "Contents", "Resources", "app", "package.json"))
	if v := dshAppsVersion(); v != "unknown" {
		t.Fatalf("no manifest read as %q", v)
	}
	// beside DeepSeek Harness Desktop, the older dsh of the two
	writeT(t, filepath.Join(next, "Contents", "Resources", "app", "package.json"), dshNextManifest)
	installApp(t, apps, "DeepSeek Harness.app", dshOfficialPlist)
	if v := dshAppsVersion(); v != "0.2.1-rc.2" {
		t.Fatalf("with both apps: %q", v)
	}
	os.RemoveAll(filepath.Join(apps, "DeepSeek Harness.app"))

	// another app under the name is not DSH NEXT, nor is the Beta
	installApp(t, apps, "DSH NEXT.app", strings.Replace(dshNextPlist, "ai.deepseek.dsh.desktop.next", "com.example.other", 1))
	installApp(t, apps, "DSH Desktop Beta.app", strings.NewReplacer(
		"ai.deepseek.dsh.desktop.next", "ai.deepseek.dsh.desktop.beta",
		"DSH NEXT", "DSH Desktop Beta", "2.0.24-next", "2.0.24-beta.2").Replace(dshNextPlist))
	if a := gone(); a.Detected() || a.HasApp() {
		t.Fatalf("another bundle id taken for DSH NEXT: detected %v, app %v", a.Detected(), a.HasApp())
	}
	os.RemoveAll(next)

	// DSH Desktop, the same vendor's build on ~/.dsh
	installApp(t, apps, "DSH Desktop.app", strings.NewReplacer(
		"ai.deepseek.dsh.desktop.next", "ai.deepseek.dsh.desktop",
		"DSH NEXT", "DSH Desktop", "2.0.24-next", "2.0.18").Replace(dshNextPlist))
	if a := gone(); !a.Detected() || a.CLIMissing() || a.AppVersion() != "2.0.18" {
		t.Fatalf("DSH Desktop: detected %v, CLI missing %v, version %q", a.Detected(), a.CLIMissing(), a.AppVersion())
	}
	os.RemoveAll(filepath.Join(apps, "DSH Desktop.app"))

	// Windows: the per-user installer's folder, or the one it registered,
	// under electron-builder's guid for the appId
	ns := uuid.MustParse("50e065bc-3134-11e6-9bab-38c9862bdaf3")
	builds := desktopApps["dsh"].more
	for _, b := range builds {
		if got := uuid.NewSHA1(ns, []byte(b.id)).String(); got != b.guid {
			t.Fatalf("%s: guid %s, the installer's %s", b.id, b.guid, got)
		}
	}
	writeT(t, filepath.Join(apps, "DSH NEXT", "DSH NEXT.exe"), "MZ")
	if a := gone(); !a.Detected() || a.CLIMissing() {
		t.Fatalf("DSH NEXT.exe: detected %v, CLI missing %v", a.Detected(), a.CLIMissing())
	}
	os.RemoveAll(filepath.Join(apps, "DSH NEXT"))
	picked := t.TempDir()
	reg["4a513c97-f710-5eb4-97f6-b8e556e2c341"] = [2]string{picked, "2.0.24-next"}
	if gone().Detected() {
		t.Fatal("a registered folder without the app taken for it")
	}
	writeT(t, filepath.Join(picked, "DSH NEXT.exe"), "MZ")
	if a := gone(); !a.Detected() || a.AppVersion() != "2.0.24-next" {
		t.Fatalf("the registered DSH NEXT: detected %v, version %q", a.Detected(), a.AppVersion())
	}
	// the dsh it runs is in app.asar there: not known
	if v := dshAppsVersion(); v != "unknown" {
		t.Fatalf("Windows DSH NEXT's dsh %q", v)
	}
	delete(reg, "4a513c97-f710-5eb4-97f6-b8e556e2c341")
	if gone().Detected() {
		t.Fatal("found with nothing installed")
	}
}

// DSH NEXT's and DSH Desktop's Linux debs, in electron-builder's
// /opt/<productName> with their executableName.
func TestDshCommunityShellsOnLinux(t *testing.T) {
	home := dshLinuxSandbox(t)
	for _, d := range proc.SystemDirs("/opt/DSH NEXT", "/opt/DSH Desktop") {
		if _, err := os.Stat(d); err == nil {
			t.Fatalf("an empty sandbox has %s", d)
		}
		t.Cleanup(func() { os.RemoveAll(d) })
	}
	for _, c := range []struct{ dir, exe string }{{"/opt/DSH NEXT", "dsh-desktop-next"}, {"/opt/DSH Desktop", "dsh-desktop"}} {
		a := dsh(home)
		a.Bin = "magpie-test-gone-dsh"
		if a.HasApp() || a.Detected() {
			t.Fatalf("%s: found before it is there", c.dir)
		}
		deb := proc.SystemDirs(c.dir)[0]
		writeTestFile(t, filepath.Join(deb, c.exe), "\x7fELF", 0o755)
		writeTestFile(t, filepath.Join(deb, "resources", "app.asar"), "asar", 0o644)
		if !a.HasApp() || !a.Detected() || a.CLIMissing() {
			t.Fatalf("%s: app %v, detected %v, CLI missing %v", c.dir, a.HasApp(), a.Detected(), a.CLIMissing())
		}
		if got, want := dsh(home).Path, dshDesktopFile(filepath.Join(home, ".dsh")); got != want {
			t.Fatalf("%s: wires %s, want %s", c.dir, got, want)
		}
		os.RemoveAll(deb)
	}
}

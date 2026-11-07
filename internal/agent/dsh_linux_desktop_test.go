package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/proc"
)

// dshAURDesktopEntry is /usr/share/applications/deepseek-ai-dsh-desktop.desktop
// as the AUR's deepseek-harness-desktop 0.2.2rc.1-1 PKGBUILD writes it.
const dshAURDesktopEntry = `[Desktop Entry]
Type=Application
Name=DeepSeek Harness
Comment=DeepSeek Harness desktop application
Exec=deepseek-harness-desktop %U
Icon=deepseek-harness
Terminal=false
StartupNotify=true
StartupWMClass=deepseek-ai-dsh-desktop
Categories=Development;
Keywords=AI;agent;deepseek;harness;dsh;
MimeType=x-scheme-handler/dsh;
`

// dshAppImageEntry is the entry AppImageLauncher writes to
// ~/.local/share/applications for an AppImage it integrates: the
// AppImage's own (electron-builder's, with the app's protocol as its
// MimeType), its Exec pointed at where the AppImage now is.
const dshAppImageEntry = `[Desktop Entry]
Name=DeepSeek Harness
Exec="IMAGE" --no-sandbox %U
Terminal=false
Type=Application
Icon=appimagekit_0d1f-deepseek-harness
StartupWMClass=DeepSeek Harness
X-AppImage-Version=0.2.2-rc.1
Comment=DeepSeek Harness
MimeType=x-scheme-handler/dsh;
Categories=Development;
TryExec=IMAGE
X-AppImage-Old-Icon=deepseek-harness
X-AppImage-Identifier=0d1f
`

// dshLinuxSandbox is a Linux machine with none of DeepSeek Harness's
// installs, whatever runs the test: the system linuxApp looks on, the
// one CLIMissing judges for, the user's data folder and the system's
// (under the test's proc.SystemDirs), and a PATH with no dsh. It gives
// the home.
func dshLinuxSandbox(t *testing.T) string {
	t.Helper()
	judgeFor(t, "linux")
	wasOS, wasFolders, wasStore, wasReg := linuxAppOS, appFolders, windowsStorePackages, windowsRegisteredApp
	linuxAppOS = "linux"
	appFolders = func() []string { return nil }
	windowsStorePackages = func() []string { return nil }
	windowsRegisteredApp = func(string) (string, string) { return "", "" }
	t.Cleanup(func() {
		linuxAppOS, appFolders, windowsStorePackages, windowsRegisteredApp = wasOS, wasFolders, wasStore, wasReg
	})
	resetEntryScans := func() {
		entryScans.Lock()
		entryScans.m = map[string]entryScan{}
		entryScans.Unlock()
	}
	resetEntryScans()
	t.Cleanup(resetEntryScans)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DSH_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_DATA_DIRS", "/usr/local/share:/usr/share")
	t.Setenv("PATH", t.TempDir())
	for _, d := range proc.SystemDirs("/usr/lib/deepseek-harness-desktop", "/opt/DeepSeek Harness", "/usr/share/applications", "/usr/local/share/applications") {
		if _, err := os.Stat(d); err == nil {
			t.Fatalf("an empty sandbox has %s", d)
		}
		t.Cleanup(func() { os.RemoveAll(d) })
	}
	return home
}

// writeTestFile writes body to path, making its folder.
func writeTestFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// xianhongtao on Discord (magpie 0.1.3232, Arch Linux): with DeepSeek
// Harness Desktop from the AUR (deepseek-harness-desktop 0.2.2rc.1-1) and
// no dsh CLI, magpie found ~/.dsh and said "DeepSeek Harness CLI not
// found", and knew the app on macOS and Windows only. Its Linux installs
// are found now: the AUR's folder, a .deb's under /opt, and any install
// whose desktop entry is the app's, an AppImage's among them.
func TestDshLinuxDesktopApp(t *testing.T) {
	home := dshLinuxSandbox(t)
	for _, p := range []string{"web", "desktop"} {
		writeTestFile(t, filepath.Join(home, ".dsh", "profiles", p, "cordis.patch.yml"), dshDesktopPatch, 0o644)
	}
	a := dsh(home)
	a.Bin = "magpie-test-gone-dsh"
	if a.HasApp() || !a.Detected() || !a.CLIMissing() {
		t.Fatalf("nothing installed: app %v, detected %v, CLI missing %v", a.HasApp(), a.Detected(), a.CLIMissing())
	}

	// the AUR's build: /usr/lib/deepseek-harness-desktop, the reporter's
	aur := proc.SystemDirs("/usr/lib/deepseek-harness-desktop")[0]
	writeTestFile(t, filepath.Join(aur, "deepseek-harness"), "\x7fELF", 0o755)
	if a.HasApp() {
		t.Fatal("an executable with no Electron app beside it taken for the app")
	}
	writeTestFile(t, filepath.Join(aur, "resources", "app.asar"), "asar", 0o644)
	if !a.HasApp() || a.CLIMissing() {
		t.Fatalf("the AUR's app: app %v, CLI missing %v", a.HasApp(), a.CLIMissing())
	}
	// it isn't looked for on macOS or Windows
	linuxAppOS = "darwin"
	if a.HasApp() {
		t.Fatal("a Linux folder read on macOS")
	}
	linuxAppOS = "linux"
	os.RemoveAll(aur)

	// a .deb's, in electron-builder's /opt/<productName>
	deb := proc.SystemDirs("/opt/DeepSeek Harness")[0]
	writeTestFile(t, filepath.Join(deb, "deepseek-harness"), "\x7fELF", 0o755)
	writeTestFile(t, filepath.Join(deb, "resources", "app.asar"), "asar", 0o644)
	if !a.HasApp() {
		t.Fatal("the .deb's app in /opt not found")
	}
	os.RemoveAll(deb)
	if a.HasApp() {
		t.Fatal("the app found with nothing installed")
	}

	// dsh in a WSL distro is not this machine's app
	if (&Agent{ID: "dsh@wsl:Ubuntu", WSL: "Ubuntu", Bin: "dsh"}).HasApp() {
		t.Fatal("WSL twin")
	}
}

// An install magpie knows no folder of is found by its desktop entry: the
// AUR's in /usr/share/applications (its program a script on PATH), an
// AppImage's in ~/.local/share/applications (its program the AppImage,
// anywhere). Only the app's entry counts: named DeepSeek Harness and
// handling dsh:// links, its program there, not hidden by the user's own.
func TestDshLinuxDesktopEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		// An entry's program is found as the host finds one: on PATH
		// (Windows wants a PATHEXT extension) or an executable file
		// (Windows has no exec bit). On Windows the check never runs
		// (linuxAppOS is runtime.GOOS), so there is nothing to test (#5617).
		t.Skip("a Linux program can't be found on a Windows host")
	}
	home := dshLinuxSandbox(t)
	a := &Agent{ID: "dsh", Bin: "dsh"}
	sys := filepath.Join(proc.SystemDirs("/usr/share")[0], "applications")
	user := filepath.Join(home, ".local", "share", "applications")

	// the AUR's entry, whose program is /usr/bin/deepseek-harness-desktop
	writeTestFile(t, filepath.Join(sys, "deepseek-ai-dsh-desktop.desktop"), dshAURDesktopEntry, 0o644)
	if a.HasApp() {
		t.Fatal("an entry whose program isn't on PATH")
	}
	bin := t.TempDir()
	writeTestFile(t, filepath.Join(bin, "deepseek-harness-desktop"), "#!/bin/sh\nexec /usr/lib/deepseek-harness-desktop/deepseek-harness \"$@\"\n", 0o755)
	t.Setenv("PATH", bin)
	if !a.HasApp() {
		t.Fatal("the AUR's entry, its program on PATH, not found")
	}
	// the user's own copy of it, hidden, hides it (the menu editors'
	// way to take an app out of the menu)
	writeTestFile(t, filepath.Join(user, "deepseek-ai-dsh-desktop.desktop"), dshAURDesktopEntry+"Hidden=true\n", 0o644)
	if a.HasApp() {
		t.Fatal("an entry the user hid still counts")
	}
	os.RemoveAll(user)
	t.Setenv("PATH", t.TempDir())

	// another app's entries: the same name with no dsh:// links, dsh://
	// under another name, the scheme only in another group
	for i, other := range []string{
		strings.Replace(dshAURDesktopEntry, "MimeType=x-scheme-handler/dsh;\n", "", 1),
		strings.Replace(dshAURDesktopEntry, "Name=DeepSeek Harness\n", "Name=DeepSeek Harness EAC\n", 1),
		strings.Replace(dshAURDesktopEntry, "MimeType=x-scheme-handler/dsh;\n", "[Desktop Action new]\nMimeType=x-scheme-handler/dsh;\n", 1),
	} {
		os.Remove(filepath.Join(sys, "deepseek-ai-dsh-desktop.desktop"))
		name := filepath.Join(sys, fmt.Sprintf("other-%d.desktop", i))
		writeTestFile(t, name, other, 0o644)
		t.Setenv("PATH", bin)
		if a.HasApp() {
			t.Fatalf("another app's entry taken for DeepSeek Harness's:\n%s", other)
		}
		os.Remove(name)
	}
	os.RemoveAll(sys)
	t.Setenv("PATH", t.TempDir())

	// an AppImage that AppImageLauncher integrated, in ~/Applications
	image := filepath.Join(home, "Applications", "deepseek-harness-0.2.2-rc.1-linux-x86_64_0d1f.AppImage")
	entry := filepath.Join(user, "appimagekit_0d1f-DeepSeek_Harness.desktop")
	writeTestFile(t, entry, strings.ReplaceAll(dshAppImageEntry, "IMAGE", image), 0o644)
	if a.HasApp() {
		t.Fatal("an entry whose AppImage isn't there")
	}
	writeTestFile(t, image, "\x7fELF", 0o755)
	if !a.HasApp() {
		t.Fatal("the AppImage's entry not found")
	}
	// the AppImage deleted, its entry left
	os.Remove(image)
	if a.HasApp() {
		t.Fatal("an AppImage deleted since still counts")
	}
}

// With the app on Linux and no profile yet, magpie wires the app's
// profile, profiles/desktop, as on macOS and Windows: the web profile is a
// dsh CLI's, which this machine doesn't have.
func TestDshLinuxDesktopAppProfile(t *testing.T) {
	home := dshLinuxSandbox(t)
	dir := filepath.Join(home, ".dsh")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	aur := proc.SystemDirs("/usr/lib/deepseek-harness-desktop")[0]
	writeTestFile(t, filepath.Join(aur, "deepseek-harness"), "\x7fELF", 0o755)
	writeTestFile(t, filepath.Join(aur, "resources", "app.asar"), "asar", 0o644)
	if got := dsh(home).Path; got != dshDesktopFile(dir) {
		t.Fatalf("the profile magpie wires = %s, want the desktop app's %s", got, dshDesktopFile(dir))
	}
}

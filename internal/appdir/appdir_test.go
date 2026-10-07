package appdir

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// root is a temp folder with its links followed (/var is /private/var on a
// Mac), as Resolve gives its answers.
func root(t *testing.T) string {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func touch(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestResolve(t *testing.T) {
	t.Setenv("APPIMAGE", "")
	r := root(t)

	// no data folder: installed
	exe := touch(t, filepath.Join(r, "plain", "magpie"))
	if got := Resolve(exe); got != "" {
		t.Errorf("no data folder: %q, want installed", got)
	}
	// a file named data is not the folder
	touch(t, filepath.Join(r, "plain", "data"))
	if got := Resolve(exe); got != "" {
		t.Errorf("data is a file: %q, want installed", got)
	}

	// a data folder beside it
	exe = touch(t, filepath.Join(r, "port", "magpie.exe"))
	mkdir(t, filepath.Join(r, "port", "data"))
	if got, want := Resolve(exe), filepath.Join(r, "port", "data"); got != want {
		t.Errorf("data folder: %q, want %q", got, want)
	}

	// a .portable marker: the data folder is made beside it when written
	exe = touch(t, filepath.Join(r, "marker", "magpie"))
	touch(t, filepath.Join(r, "marker", ".portable"))
	if got, want := Resolve(exe), filepath.Join(r, "marker", "data"); got != want {
		t.Errorf(".portable: %q, want %q", got, want)
	}

	// a Mac app: beside the bundle, never inside it
	exe = touch(t, filepath.Join(r, "mac", "Magpie.app", "Contents", "MacOS", "magpie"))
	mkdir(t, filepath.Join(r, "mac", "Magpie.app", "Contents", "MacOS", "data"))
	if got := Resolve(exe); got != "" {
		t.Errorf("data inside the bundle: %q, want installed", got)
	}
	mkdir(t, filepath.Join(r, "mac", "data"))
	if got, want := Resolve(exe), filepath.Join(r, "mac", "data"); got != want {
		t.Errorf("Mac app: %q, want %q", got, want)
	}
}

func TestResolveSymlinkedExe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	t.Setenv("APPIMAGE", "")
	r := root(t)
	real := touch(t, filepath.Join(r, "app", "magpie"))
	mkdir(t, filepath.Join(r, "bin"))
	link := filepath.Join(r, "bin", "magpie")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// a data folder beside the link only: the link isn't where magpie is
	mkdir(t, filepath.Join(r, "bin", "data"))
	if got := Resolve(link); got != "" {
		t.Errorf("data beside the link: %q, want installed", got)
	}
	mkdir(t, filepath.Join(r, "app", "data"))
	if got, want := Resolve(link), filepath.Join(r, "app", "data"); got != want {
		t.Errorf("symlinked exe: %q, want %q", got, want)
	}
}

func TestResolveAppImage(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("AppImage is Linux's")
	}
	r := root(t)
	exe := touch(t, filepath.Join(r, "mount", "usr", "bin", "magpie"))
	img := touch(t, filepath.Join(r, "apps", "Magpie.AppImage"))
	mkdir(t, filepath.Join(r, "apps", "data"))
	t.Setenv("APPIMAGE", img)
	t.Setenv("APPDIR", filepath.Join(r, "mount"))
	if got, want := Resolve(exe), filepath.Join(r, "apps", "data"); got != want {
		t.Errorf("AppImage: %q, want %q", got, want)
	}
	// inherited from another AppImage (a terminal): magpie isn't in its mount
	other := touch(t, filepath.Join(r, "bin", "magpie"))
	if got := Resolve(other); got != "" {
		t.Errorf("inherited APPIMAGE: %q, want installed", got)
	}
}

func TestFolders(t *testing.T) {
	t.Setenv("APPIMAGE", "")
	r := root(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(r, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(r, "cache"))
	t.Cleanup(func() { UseExecutable("") })

	UseExecutable("")
	if Portable() != "" || Config() != filepath.Join(r, "cfg", "magpie") || Cache() != filepath.Join(r, "cache", "magpie") {
		t.Errorf("installed: portable %q, config %q, cache %q", Portable(), Config(), Cache())
	}
	if d, _ := SystemCache(); filepath.Base(d) != "magpie" {
		t.Errorf("installed system cache %q", d)
	}

	exe := touch(t, filepath.Join(r, "port", "magpie"))
	data := filepath.Join(r, "port", "data")
	mkdir(t, data)
	UseExecutable(exe)
	if Portable() != data || Config() != data || Cache() != filepath.Join(data, "cache") {
		t.Errorf("portable: portable %q, config %q, cache %q", Portable(), Config(), Cache())
	}
	if d, _ := SystemCache(); d != filepath.Join(data, "cache") {
		t.Errorf("portable system cache %q", d)
	}
}

// #508: portable, WebView2's profile goes into the data folder, not
// %APPDATA%\magpie.exe; installed, it is left at WebView2's default.
func TestWebView(t *testing.T) {
	t.Setenv("APPIMAGE", "")
	r := root(t)
	t.Cleanup(func() { UseExecutable("") })

	UseExecutable("")
	if got := WebView(); got != "" {
		t.Errorf("installed: WebView() = %q, want the default", got)
	}
	exe := touch(t, filepath.Join(r, "port", "magpie.exe"))
	touch(t, filepath.Join(r, "port", ".portable"))
	UseExecutable(exe)
	if got, want := WebView(), filepath.Join(r, "port", "data", "webview2"); got != want {
		t.Errorf("portable: WebView() = %q, want %q", got, want)
	}
}

// #989: a folder named data in /Applications, where every app keeps its
// own, isn't magpie's portable data while the installed folder holds
// magpie's files; it is when nothing is installed, or with .portable.
func TestResolveSharedFolder(t *testing.T) {
	t.Setenv("APPIMAGE", "")
	r := root(t)
	apps := filepath.Join(r, "Applications")
	was := sharedFolder
	sharedFolder = func(dir string) bool { return dir == apps }
	t.Cleanup(func() { sharedFolder = was })
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(r, "cfg"))

	exe := touch(t, filepath.Join(apps, "Magpie.app", "Contents", "MacOS", "magpie"))
	data := filepath.Join(apps, "data")
	mkdir(t, filepath.Join(data, "nodes"))
	touch(t, filepath.Join(data, "install-id"))

	// another app's data folder, nothing installed: not taken
	if got := Resolve(exe); got != "" {
		t.Errorf("no magpie files in data: %q, want installed", got)
	}
	touch(t, filepath.Join(data, "settings.json"))

	// nothing installed: one who keeps magpie portable there
	if got := Resolve(exe); got != data {
		t.Errorf("nothing installed: %q, want %q", got, data)
	}
	if got := Passed(exe); got != "" {
		t.Errorf("nothing installed: Passed %q", got)
	}
	// installed magpie has its files: the data folder isn't taken
	touch(t, filepath.Join(r, "cfg", "magpie", "providers.json"))
	if got := Resolve(exe); got != "" {
		t.Errorf("installed has files: %q, want installed", got)
	}
	if got := Passed(exe); got != data {
		t.Errorf("Passed %q, want %q", got, data)
	}
	t.Cleanup(func() { UseExecutable("") })
	UseExecutable(exe)
	if Config() != filepath.Join(r, "cfg", "magpie") {
		t.Errorf("Config %q, want the installed folder", Config())
	}
	// asked for with .portable: portable
	touch(t, filepath.Join(apps, ".portable"))
	if got := Resolve(exe); got != data {
		t.Errorf(".portable: %q, want %q", got, data)
	}

	// any other folder: a data folder beside magpie is its own, as before
	other := touch(t, filepath.Join(r, "usb", "Magpie.app", "Contents", "MacOS", "magpie"))
	mkdir(t, filepath.Join(r, "usb", "data"))
	if got, want := Resolve(other), filepath.Join(r, "usb", "data"); got != want {
		t.Errorf("own folder: %q, want %q", got, want)
	}
}

// tangle778 on X: magpie.exe run from Downloads beside a data folder the
// user's archiver empties wrote its providers there and lost them. A data
// folder in Downloads holding none of magpie's files isn't its portable
// data; one that does is, and .portable always makes it portable.
func TestResolveDownloads(t *testing.T) {
	t.Setenv("APPIMAGE", "")
	r := root(t)
	h := filepath.Join(r, "home")
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", "")
	dl := filepath.Join(h, "Downloads")
	exe := touch(t, filepath.Join(dl, "magpie.exe"))
	data := filepath.Join(dl, "data")
	touch(t, filepath.Join(data, "report.json"))

	if got := Resolve(exe); got != "" {
		t.Errorf("user's data folder: %q, want installed", got)
	}
	if got := Passed(exe); got != "" {
		t.Errorf("user's data folder: Passed %q, want none", got)
	}
	touch(t, filepath.Join(data, "providers.json"))
	if got := Resolve(exe); got != data {
		t.Errorf("portable run before: %q, want %q", got, data)
	}
	touch(t, filepath.Join(h, ".config", "magpie", "logins.json"))
	if got := Resolve(exe); got != "" {
		t.Errorf("installed has files: %q, want installed", got)
	}
	if got := Passed(exe); got != data {
		t.Errorf("Passed %q, want %q", got, data)
	}
	touch(t, filepath.Join(dl, ".portable"))
	if got := Resolve(exe); got != data {
		t.Errorf(".portable: %q, want %q", got, data)
	}
}

func TestSharedFolders(t *testing.T) {
	h, _ := Home()
	for _, d := range []string{h, filepath.Join(h, "Downloads"), filepath.Join(h, "Downloads") + string(filepath.Separator), filepath.Join(h, "Desktop"), filepath.Join(h, "Documents")} {
		if !sharedFolder(d) {
			t.Errorf("%s not shared", d)
		}
	}
	for _, d := range []string{filepath.Join(h, "Downloads", "magpie"), filepath.Join(h, "tools")} {
		if sharedFolder(d) {
			t.Errorf("%s shared", d)
		}
	}
}

func TestSharedFolderIsApplications(t *testing.T) {
	if runtime.GOOS != "darwin" {
		if sharedFolder("/Applications") {
			t.Error("shared off the Mac")
		}
		return
	}
	h, _ := Home()
	for _, d := range []string{"/Applications", "/Applications/", filepath.Join(h, "Applications")} {
		if !sharedFolder(d) {
			t.Errorf("%s not shared", d)
		}
	}
	for _, d := range []string{"/Volumes/USB", "/Applications/Utilities"} {
		if sharedFolder(d) {
			t.Errorf("%s shared", d)
		}
	}
}

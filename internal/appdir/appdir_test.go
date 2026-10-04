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

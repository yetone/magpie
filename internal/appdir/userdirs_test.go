package appdir

import (
	"os"
	"path/filepath"
	"testing"
)

// userDirsZH is user-dirs.dirs as xdg-user-dirs-update writes it on a
// Chinese desktop (Ubuntu 24.04, zh_CN.UTF-8): Downloads is ~/下载, not
// ~/Downloads (1Shin on Discord: an export joined $HOME/Downloads).
const userDirsZH = `# This file is written by xdg-user-dirs-update
# If you want to change or add directories, just edit the line you're
# interested in. All local changes will be retained on the next run.
# Format is XDG_xxx_DIR="$HOME/yyy", where yyy is a shell-escaped
# homedir-relative path, or XDG_xxx_DIR="/yyy", where /yyy is an
# absolute path. No other format is supported.
# 
XDG_DESKTOP_DIR="$HOME/桌面"
XDG_DOWNLOAD_DIR="$HOME/下载"
XDG_TEMPLATES_DIR="$HOME/模板"
XDG_PUBLICSHARE_DIR="$HOME/公共的"
XDG_DOCUMENTS_DIR="$HOME/文档"
XDG_MUSIC_DIR="$HOME/音乐"
XDG_PICTURES_DIR="$HOME/图片"
XDG_VIDEOS_DIR="$HOME/视频"
`

func userDirsHome(t *testing.T, dirs string) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	if dirs != "" {
		os.MkdirAll(filepath.Join(h, ".config"), 0o700)
		if err := os.WriteFile(filepath.Join(h, ".config", "user-dirs.dirs"), []byte(dirs), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func fakeKnownFolder(t *testing.T, f func(kind string) (string, string)) {
	t.Helper()
	was := knownFolder
	knownFolder = f
	t.Cleanup(func() { knownFolder = was })
}

func TestUserFolderXDG(t *testing.T) {
	h := userDirsHome(t, userDirsZH)
	for kind, want := range map[string]string{
		DownloadsFolder: filepath.Join(h, "下载"),
		DesktopFolder:   filepath.Join(h, "桌面"),
		DocumentsFolder: filepath.Join(h, "文档"),
	} {
		if got := userFolderOn("linux", h, kind); got != want {
			t.Errorf("linux %s: %q, want %q", kind, got, want)
		}
		if got := userFolderOn("freebsd", h, kind); got != want {
			t.Errorf("freebsd %s: %q, want %q", kind, got, want)
		}
	}
	// A Mac's Downloads can't be moved: a user-dirs.dirs there is no one's.
	if got, want := userFolderOn("darwin", h, DownloadsFolder), filepath.Join(h, "Downloads"); got != want {
		t.Errorf("darwin: %q, want %q", got, want)
	}
}

func TestUserFolderXDGLines(t *testing.T) {
	for _, c := range []struct {
		name, dirs, want string
	}{
		{"absolute", `XDG_DOWNLOAD_DIR="/mnt/data/dl"`, "/mnt/data/dl"},
		{"escaped", `XDG_DOWNLOAD_DIR="$HOME/my \"dl\""`, "~/my \"dl\""},
		{"spaces round =", `  XDG_DOWNLOAD_DIR = "$HOME/dl"`, "~/dl"},
		{"turned off", `XDG_DOWNLOAD_DIR="$HOME/"`, "~"},
		{"home itself", `XDG_DOWNLOAD_DIR="$HOME"`, "~"},
		{"the last wins", "XDG_DOWNLOAD_DIR=\"$HOME/a\"\nXDG_DOWNLOAD_DIR=\"$HOME/b\"", "~/b"},
		{"commented", `# XDG_DOWNLOAD_DIR="$HOME/a"`, "~/Downloads"},
		{"unquoted", `XDG_DOWNLOAD_DIR=$HOME/a`, "~/Downloads"},
		{"relative", `XDG_DOWNLOAD_DIR="a/b"`, "~/Downloads"},
		{"another variable", `XDG_DOWNLOAD_DIR="$XDG_DATA_HOME/a"`, "~/Downloads"},
		{"another folder only", `XDG_MUSIC_DIR="$HOME/a"`, "~/Downloads"},
		{"empty file", "\n", "~/Downloads"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := userDirsHome(t, c.dirs)
			want := c.want
			if want[0] == '/' {
				want = filepath.FromSlash(want)
			} else if want == "~" {
				want = h
			} else if len(want) > 1 && want[:2] == "~/" {
				want = filepath.Join(h, filepath.FromSlash(want[2:]))
			}
			if got := userFolderOn("linux", h, DownloadsFolder); got != want {
				t.Errorf("%q, want %q", got, want)
			}
		})
	}
}

// With no user-dirs.dirs, or XDG_CONFIG_HOME unset (home/.config is read),
// or relative (ignored, as the spec asks).
func TestUserFolderXDGConfigHome(t *testing.T) {
	h := userDirsHome(t, "")
	if got, want := userFolderOn("linux", h, DownloadsFolder), filepath.Join(h, "Downloads"); got != want {
		t.Errorf("no file: %q, want %q", got, want)
	}
	userDirsHome2 := func(cfg string) {
		os.MkdirAll(filepath.Join(h, ".config"), 0o700)
		os.WriteFile(filepath.Join(h, ".config", "user-dirs.dirs"), []byte(`XDG_DOWNLOAD_DIR="$HOME/dl"`), 0o600)
		t.Setenv("XDG_CONFIG_HOME", cfg)
	}
	for _, cfg := range []string{"", "rel/cfg"} {
		userDirsHome2(cfg)
		if got, want := userFolderOn("linux", h, DownloadsFolder), filepath.Join(h, "dl"); got != want {
			t.Errorf("XDG_CONFIG_HOME %q: %q, want %q", cfg, got, want)
		}
	}
	other := t.TempDir()
	os.WriteFile(filepath.Join(other, "user-dirs.dirs"), []byte(`XDG_DOWNLOAD_DIR="$HOME/other"`), 0o600)
	t.Setenv("XDG_CONFIG_HOME", other)
	if got, want := userFolderOn("linux", h, DownloadsFolder), filepath.Join(h, "other"); got != want {
		t.Errorf("XDG_CONFIG_HOME set: %q, want %q", got, want)
	}
}

// Windows' Downloads is the known folder (moved to D:\ or OneDrive), when
// magpie's home is the profile Windows keeps it for.
func TestUserFolderWindows(t *testing.T) {
	h := userDirsHome(t, userDirsZH) // not read on Windows
	moved := filepath.Join(t.TempDir(), "OneDrive", "Downloads")
	fakeKnownFolder(t, func(kind string) (string, string) {
		if kind != DownloadsFolder {
			return "", ""
		}
		return moved, h
	})
	if got := userFolderOn("windows", h, DownloadsFolder); got != moved {
		t.Errorf("moved: %q, want %q", got, moved)
	}
	if got, want := userFolderOn("windows", h, DesktopFolder), filepath.Join(h, "Desktop"); got != want {
		t.Errorf("lookup failed: %q, want %q", got, want)
	}
	// Another home's known folder: magpie's own home keeps home\Downloads.
	other := t.TempDir()
	if got, want := userFolderOn("windows", other, DownloadsFolder), filepath.Join(other, "Downloads"); got != want {
		t.Errorf("another home: %q, want %q", got, want)
	}
	fakeKnownFolder(t, func(string) (string, string) { return moved, "" })
	if got, want := userFolderOn("windows", h, DownloadsFolder), filepath.Join(h, "Downloads"); got != want {
		t.Errorf("no profile: %q, want %q", got, want)
	}
}

// The Downloads the system keeps, moved off home, is a moved folder, and
// home's is still shared.
func TestSharedFolderMovedDownloads(t *testing.T) {
	h := userDirsHome(t, userDirsZH)
	moved := filepath.Join(h, "下载")
	fakeKnownFolder(t, func(kind string) (string, string) {
		if kind == DownloadsFolder {
			return moved, h
		}
		return "", ""
	})
	if UserFolder(DownloadsFolder) == filepath.Join(h, "Downloads") {
		t.Skip("this system's Downloads can't be moved")
	}
	if !movedFolder(moved) {
		t.Errorf("%s not a moved folder", moved)
	}
	if sharedFolder(moved) {
		t.Errorf("%s shared as home's own", moved)
	}
	if !sharedFolder(filepath.Join(h, "Downloads")) {
		t.Error("home's Downloads not shared")
	}
	if movedFolder(filepath.Join(moved, "magpie")) || sharedFolder(filepath.Join(moved, "magpie")) {
		t.Error("a folder inside shared")
	}
}

// jwaitq on Discord: updated from v0.1.2630 to v0.1.2670, magpie showed
// the setup of long ago. A data folder beside magpie in a moved Downloads
// (D:\Downloads, OneDrive, ~/下载) holding magpie's files is where every
// release before v0.1.2648 kept them, an installed folder with files of
// its own or not; one holding none of them (another app's, or emptied by
// an archiver) isn't taken.
func TestResolveMovedDownloadsKeepsData(t *testing.T) {
	t.Setenv("APPIMAGE", "")
	r, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := filepath.Join(r, "home")
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", "")
	moved := filepath.Join(r, "D", "Downloads")
	was := userFolder
	userFolder = func(kind string) string {
		if kind == DownloadsFolder {
			return moved
		}
		return filepath.Join(h, kind)
	}
	t.Cleanup(func() { userFolder = was })

	exe := filepath.Join(moved, "magpie.exe")
	data := filepath.Join(moved, "data")
	for _, p := range []string{exe, filepath.Join(data, "report.json")} {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := Resolve(exe); got != "" {
		t.Errorf("no magpie files in data: %q, want installed", got)
	}
	os.WriteFile(filepath.Join(data, "providers.json"), []byte("{}"), 0o600)
	if got := Resolve(exe); got != data {
		t.Errorf("nothing installed: %q, want %q", got, data)
	}
	// the setup of long ago, in the installed folder
	os.MkdirAll(filepath.Join(h, ".config", "magpie"), 0o700)
	os.WriteFile(filepath.Join(h, ".config", "magpie", "settings.json"), []byte("{}"), 0o600)
	if got := Resolve(exe); got != data {
		t.Errorf("installed has old files: %q, want the data folder %q", got, data)
	}
	if got := Passed(exe); got != "" {
		t.Errorf("Passed %q, want none", got)
	}
}

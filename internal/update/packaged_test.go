package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pacman's local database as the AUR's magpie-bin 0.1.2342-1 leaves it
// (its PKGBUILD installs /usr/bin/magpie), beside a package that doesn't
// own magpie.
func pacmanRoot(t *testing.T) string {
	root := t.TempDir()
	local := filepath.Join(root, "var/lib/pacman/local")
	writeFile(t, filepath.Join(local, "ALPM_DB_VERSION"), "9\n")
	writeFile(t, filepath.Join(local, "bash-5.3.3-2/files"), "%FILES%\nusr/\nusr/bin/\nusr/bin/bash\nusr/bin/sh\n\n%BACKUP%\netc/bash.bashrc\t0b5ab6a3\n")
	writeFile(t, filepath.Join(local, "bash-5.3.3-2/desc"), "%NAME%\nbash\n\n%VERSION%\n5.3.3-2\n")
	writeFile(t, filepath.Join(local, "magpie-bin-0.1.2342-1/files"), "%FILES%\nusr/\nusr/bin/\nusr/bin/magpie\nusr/share/\nusr/share/applications/\nusr/share/applications/magpie.desktop\nusr/share/icons/hicolor/256x256/apps/magpie.png\nusr/share/licenses/magpie-bin/LICENSE\n\n")
	writeFile(t, filepath.Join(local, "magpie-bin-0.1.2342-1/desc"), "%NAME%\nmagpie-bin\n\n%VERSION%\n0.1.2342-1\n\n%BASE%\nmagpie-bin\n")
	return root
}

func TestPackagedByPacman(t *testing.T) {
	root := pacmanRoot(t)
	p := packagedIn(root, "linux", "/usr/bin/magpie")
	if p == nil || p.Manager != "pacman" || p.Package != "magpie-bin" {
		t.Fatalf("/usr/bin/magpie from magpie-bin: %+v", p)
	}
	if c := p.Command(); c != "yay -S magpie-bin" {
		t.Fatalf("command %q", c)
	}
	if s := p.Say(); !strings.Contains(s, "pacman") || !strings.Contains(s, "yay -S magpie-bin") {
		t.Fatalf("said %q", s)
	}
	// /bin is /usr/bin on Arch: the same file
	if p := packagedIn(root, "linux", "/bin/magpie"); p == nil || p.Package != "magpie-bin" {
		t.Fatalf("/bin/magpie: %+v", p)
	}
	// a copy in /usr/local, ~/.local/bin or /opt that no package lists is
	// the user's own, and updates itself
	for _, exe := range []string{"/usr/local/bin/magpie", "/home/u/.local/bin/magpie", "/opt/magpie/magpie", "/usr/lib/magpie/magpie"} {
		if p := packagedIn(root, "linux", exe); p != nil {
			t.Fatalf("%s: %+v", exe, p)
		}
	}
	// not Linux: pacman's files aren't read
	if p := packagedIn(root, "darwin", "/usr/bin/magpie"); p != nil {
		t.Fatalf("darwin: %+v", p)
	}
}

// A package named otherwise is found too, its name from the folder when
// its desc can't be read.
func TestPackagedByPacmanOtherName(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "var/lib/pacman/local/ai-tools-2.0-3/files"), "%FILES%\nopt/ai/bin/magpie\n")
	p := packagedIn(root, "linux", "/opt/ai/bin/magpie")
	if p == nil || p.Package != "ai-tools" {
		t.Fatalf("%+v", p)
	}
}

// dpkg lists absolute paths, and on a merged /usr may list /bin for the
// /usr/bin the binary resolves to.
func TestPackagedByDpkg(t *testing.T) {
	root := t.TempDir()
	info := filepath.Join(root, "var/lib/dpkg/info")
	writeFile(t, filepath.Join(info, "coreutils.list"), "/.\n/bin\n/bin/ls\n")
	writeFile(t, filepath.Join(info, "magpie:amd64.list"), "/.\n/usr\n/usr/bin\n/usr/bin/magpie\n")
	p := packagedIn(root, "linux", "/usr/bin/magpie")
	if p == nil || p.Manager != "dpkg" || p.Package != "magpie" || p.Name() != "apt" || p.Command() != "sudo apt install --only-upgrade magpie" {
		t.Fatalf("%+v", p)
	}
	if p := packagedIn(root, "linux", "/usr/bin/ls"); p == nil || p.Package != "coreutils" {
		t.Fatalf("merged /usr: %+v", p)
	}
	if p := packagedIn(root, "linux", "/usr/sbin/magpie"); p != nil {
		t.Fatalf("not listed: %+v", p)
	}
}

func TestPackagedByRpm(t *testing.T) {
	root := t.TempDir()
	was := rpmOwner
	t.Cleanup(func() { rpmOwner = was })
	asked := ""
	rpmOwner = func(path string) string { asked = path; return "magpie" }
	// no rpm database: rpm isn't asked
	if p := packagedIn(root, "linux", "/usr/bin/magpie"); p != nil || asked != "" {
		t.Fatalf("no rpm here: %+v, asked %q", p, asked)
	}
	if err := os.MkdirAll(filepath.Join(root, "var/lib/rpm"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := packagedIn(root, "linux", "/usr/bin/magpie")
	if p == nil || p.Manager != "rpm" || asked != "/usr/bin/magpie" || p.Command() != "sudo dnf upgrade magpie" {
		t.Fatalf("%+v, asked %q", p, asked)
	}
	rpmOwner = func(string) string { return "" }
	if p := packagedIn(root, "linux", "/usr/bin/magpie"); p != nil {
		t.Fatalf("not owned: %+v", p)
	}
}

func TestPackagedByHomebrewAndNix(t *testing.T) {
	root := t.TempDir()
	for exe, want := range map[string]string{
		"/opt/homebrew/Cellar/magpie/0.1.126/bin/magpie":              "homebrew magpie brew upgrade magpie",
		"/home/linuxbrew/.linuxbrew/Cellar/magpie/0.1.126/bin/magpie": "homebrew magpie brew upgrade magpie",
		"/nix/store/abc123-magpie-0.1.2342/bin/magpie":                "nix  ",
	} {
		goos := "linux"
		if strings.HasPrefix(exe, "/opt/homebrew") {
			goos = "darwin"
		}
		p := packagedIn(root, goos, exe)
		if p == nil || p.Manager+" "+p.Package+" "+p.Command() != want {
			t.Fatalf("%s: %+v", exe, p)
		}
	}
	if p := packagedIn(root, "darwin", "/Applications/magpie.app/Contents/MacOS/magpie"); p != nil {
		t.Fatalf("the app: %+v", p)
	}
	// the flake's app on a Mac runs from the store, which Nix updates
	if p := packagedIn(root, "darwin", "/nix/store/abc123-magpie-0.1.4674/Applications/magpie.app/Contents/MacOS/magpie"); p == nil || p.Manager != "nix" || p.Command() != "" {
		t.Fatalf("the flake's app on a Mac: %+v", p)
	}
}

// Scoop's app folder as `scoop install magpie-app` from the magpie bucket
// leaves it: the version folder holds the binary with the
// scoop-install.json and scoop-manifest.json Scoop writes (seen on
// xiaomi-gpu, Scoop 0.6.0), install.json and manifest.json before 0.6,
// and current is a junction to it.
func TestPackagedByScoop(t *testing.T) {
	root := filepath.Join(t.TempDir(), "scoop")
	for i, v := range []string{"0.1.4478", "current"} {
		dir := filepath.Join(root, "apps", "magpie-app", v)
		pre := []string{"scoop-", ""}[i]
		writeFile(t, filepath.Join(dir, "magpie.exe"), "MZ")
		writeFile(t, filepath.Join(dir, pre+"install.json"), `{"bucket":"magpie","architecture":"64bit"}`)
		writeFile(t, filepath.Join(dir, pre+"manifest.json"), `{"version":"0.1.4478"}`)
		p := packagedIn("/", "windows", filepath.Join(dir, "magpie.exe"))
		if p == nil || p.Manager != "scoop" || p.Package != "magpie-app" {
			t.Fatalf("%s: got %+v, want scoop's magpie-app", v, p)
		}
		if c := p.Command(); c != "scoop update magpie-app" {
			t.Fatalf("command %q", c)
		}
		if s := p.Say(); !strings.Contains(s, "Scoop") || !strings.HasSuffix(s, "scoop update magpie-app") {
			t.Fatalf("say %q", s)
		}
	}
	// a magpie.exe kept by hand in a folder named apps, without Scoop's
	// files, is magpie's to update
	own := filepath.Join(t.TempDir(), "apps", "magpie", "bin")
	writeFile(t, filepath.Join(own, "magpie.exe"), "MZ")
	writeFile(t, filepath.Join(own, "manifest.json"), `{}`)
	writeFile(t, filepath.Join(own, "scoop-install.json"), `{}`)
	if p := packagedIn("/", "windows", filepath.Join(own, "magpie.exe")); p != nil {
		t.Fatalf("own folder: got %+v", p)
	}
	// only on Windows
	dir := filepath.Join(root, "apps", "magpie-app", "current")
	if p := packagedIn("/", "linux", filepath.Join(dir, "magpie.exe")); p != nil {
		t.Fatalf("linux: got %+v", p)
	}
}

// winget's portable package folder, as `winget install yetone.Magpie`
// leaves it per user and machine-wide.
func TestPackagedByWinget(t *testing.T) {
	for _, exe := range []string{
		`C:\Users\yeton\AppData\Local\Microsoft\WinGet\Packages\yetone.Magpie_Microsoft.Winget.Source_8wekyb3d8bbwe\magpie.exe`,
		`C:\Program Files\WinGet\Packages\yetone.Magpie_Microsoft.Winget.Source_8wekyb3d8bbwe\magpie.exe`,
		// winget install --manifest, as seen on xiaomi-gpu (winget 1.29)
		`C:\Users\yeton\AppData\Local\Microsoft\WinGet\Packages\yetone.Magpie__DefaultSource\magpie.exe`,
	} {
		p := packagedIn("/", "windows", exe)
		if p == nil || p.Manager != "winget" || p.Package != "yetone.Magpie" {
			t.Fatalf("%s: got %+v", exe, p)
		}
		if c := p.Command(); c != "winget upgrade --id yetone.Magpie" {
			t.Fatalf("command %q", c)
		}
	}
	if p := packagedIn("/", "windows", `C:\Users\yeton\Downloads\magpie-windows-amd64.exe`); p != nil {
		t.Fatalf("downloaded: got %+v", p)
	}
	p := packagedIn("/", "windows", `C:\Users\yeton\AppData\Local\Microsoft\WinGet\Packages\odd\magpie.exe`)
	if p == nil || p.Manager != "winget" || p.Command() != "winget upgrade magpie" {
		t.Fatalf("unknown id: got %+v", p)
	}
}

// A path whose links can't be resolved is kept as given, so a Scoop
// install's current junction (which EvalSymlinks fails on) is still
// judged by PackagedBy, instead of Executable failing and the check being
// skipped.
func TestResolvedKeepsUnresolvablePath(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone", "magpie.exe")
	if got := resolved(missing); got != missing {
		t.Fatalf("resolved(%q) = %q", missing, got)
	}
	real := filepath.Join(dir, "magpie")
	writeFile(t, real, "x")
	want, _ := filepath.EvalSymlinks(real)
	if got := resolved(real); got != want {
		t.Fatalf("resolved(%q) = %q, want %q", real, got, want)
	}
}

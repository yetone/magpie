package update

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/proc"
)

// Packaged is the package manager that installed a magpie binary, which
// then has to be the one to update it. magpie replacing a file a package
// owns (pkexec over /usr/bin/magpie from the AUR's magpie-bin, shuhai on
// Discord) leaves the manager's database naming an older version and a
// file that no longer matches it, and the manager's next upgrade, which
// can be a version older than the one magpie put in, writes over it again.
type Packaged struct {
	Manager string `json:"manager"`           // pacman, dpkg, rpm, homebrew, nix, scoop or winget
	Package string `json:"package,omitempty"` // the package's name, when known
}

// Name is the manager as its users call it.
func (p *Packaged) Name() string {
	switch p.Manager {
	case "dpkg":
		return "apt"
	case "homebrew":
		return "Homebrew"
	case "nix":
		return "Nix"
	case "scoop":
		return "Scoop"
	}
	return p.Manager
}

// Command is what updates the package, "" when there is no one command
// (Nix: a profile or a system configuration). magpie is in no distribution's
// own repository, so a pacman package is the AUR's, which pacman alone
// doesn't upgrade: its AUR helper does.
func (p *Packaged) Command() string {
	pkg := p.Package
	if pkg == "" {
		pkg = "magpie"
	}
	switch p.Manager {
	case "pacman":
		return "yay -S " + pkg
	case "dpkg":
		return "sudo apt install --only-upgrade " + pkg
	case "rpm":
		return "sudo dnf upgrade " + pkg
	case "homebrew":
		return "brew upgrade " + pkg
	case "scoop":
		return "scoop update " + pkg
	case "winget":
		if p.Package == "" {
			return "winget upgrade magpie"
		}
		return "winget upgrade --id " + p.Package
	}
	return ""
}

// Say is the sentence a terminal is told: who updates this magpie, and how.
func (p *Packaged) Say() string {
	s := "this magpie was installed with " + p.Name()
	if p.Package != "" {
		s += " (package " + p.Package + ")"
	}
	s += ", which updates it, not magpie"
	if c := p.Command(); c != "" {
		s += ": " + c
		if p.Manager == "pacman" {
			s += " (or paru, or the AUR helper it came from)"
		}
	}
	return s
}

// PackagedBy says which package manager installed exe (the running
// binary, symlinks resolved), nil when none did.
func PackagedBy(exe string) *Packaged {
	return packagedIn("/", runtime.GOOS, exe)
}

// rpmOwner is the rpm package owning path, "" when none; a test stands in
// for it. rpm's database is SQLite (or Berkeley DB), read through rpm.
var rpmOwner = func(path string) string {
	out, err := proc.Command("rpm", "-qf", "--queryformat", "%{NAME}", path).Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	if s == "" || strings.Contains(s, " ") { // "file … is not owned by any package"
		return ""
	}
	return s
}

// packagedIn is PackagedBy with the system's files under root.
func packagedIn(root, goos, exe string) *Packaged {
	p := filepath.ToSlash(exe)
	if _, rest, ok := strings.Cut(p, "/Cellar/"); ok {
		name, _, _ := strings.Cut(rest, "/")
		return &Packaged{Manager: "homebrew", Package: name}
	}
	// Nix's store on a Mac too: the flake's magpie.app runs from there,
	// read-only, and a profile or a system configuration updates it
	if strings.HasPrefix(p, "/nix/store/") {
		return &Packaged{Manager: "nix"}
	}
	if goos == "windows" {
		return packagedOnWindows(exe)
	}
	if goos != "linux" {
		return nil
	}
	if !systemPath(p) {
		return nil
	}
	if name, ok := pacmanOwner(root, p); ok {
		return &Packaged{Manager: "pacman", Package: name}
	}
	if name, ok := dpkgOwner(root, p); ok {
		return &Packaged{Manager: "dpkg", Package: name}
	}
	if isDir(filepath.Join(root, "var/lib/rpm")) {
		if name := rpmOwner(p); name != "" {
			return &Packaged{Manager: "rpm", Package: name}
		}
	}
	return nil
}

// packagedOnWindows says whether Scoop or winget installed exe. Scoop keeps
// an app in <root>\apps\<name>\<version> (or its current junction, which
// Go doesn't resolve), with the scoop-install.json and scoop-manifest.json
// it writes beside it (install.json and manifest.json before Scoop 0.6);
// its root is $SCOOP, so that folder's name isn't relied on.
// winget keeps a portable package in WinGet\Packages\<id>_<source> under
// %LOCALAPPDATA%\Microsoft (or Program Files, machine-wide), and the
// command on PATH is a link to it in WinGet\Links, which Executable
// resolves. Either one's next update would otherwise put back the version
// it holds, or fail on the file magpie changed.
func packagedOnWindows(exe string) *Packaged {
	dir := filepath.Dir(exe)
	app := filepath.Dir(dir)
	if strings.EqualFold(filepath.Base(filepath.Dir(app)), "apps") && (scoopFiles(dir, "scoop-") || scoopFiles(dir, "")) {
		return &Packaged{Manager: "scoop", Package: filepath.Base(app)}
	}
	p := strings.ReplaceAll(exe, `\`, "/")
	if i := strings.Index(strings.ToLower(p), "/winget/packages/"); i >= 0 {
		folder, _, _ := strings.Cut(p[i+len("/winget/packages/"):], "/")
		// <id>_<source>: _Microsoft.Winget.Source_8wekyb3d8bbwe from the
		// community repository, __DefaultSource from a local manifest
		id := ""
		for _, src := range []string{"_microsoft.winget.source", "__defaultsource"} {
			if j := strings.Index(strings.ToLower(folder), src); j > 0 {
				id = folder[:j]
				break
			}
		}
		return &Packaged{Manager: "winget", Package: id}
	}
	return nil
}

// scoopFiles is whether dir has the two files Scoop writes into an app's
// version folder, their names starting with prefix.
func scoopFiles(dir, prefix string) bool {
	return isFile(filepath.Join(dir, prefix+"install.json")) && isFile(filepath.Join(dir, prefix+"manifest.json"))
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// systemPath is whether a package may own p: /usr (but not /usr/local,
// which packages leave to the machine's own), /bin, /sbin, /opt.
func systemPath(p string) bool {
	if strings.HasPrefix(p, "/usr/local/") {
		return false
	}
	for _, d := range []string{"/usr/", "/bin/", "/sbin/", "/opt/"} {
		if strings.HasPrefix(p, d) {
			return true
		}
	}
	return false
}

// aliases are the names p has in a package's file list: on a merged /usr
// /bin is /usr/bin, and a package may list either.
func aliases(p string) []string {
	out := []string{p}
	for _, d := range []string{"/bin/", "/sbin/", "/lib/"} {
		if strings.HasPrefix(p, "/usr"+d) {
			out = append(out, strings.TrimPrefix(p, "/usr"))
		} else if strings.HasPrefix(p, d) {
			out = append(out, "/usr"+p)
		}
	}
	return out
}

// magpieFirst orders names with those starting "magpie" first: the package
// owning magpie is all but always one of them (magpie-bin, magpie-cli-bin,
// magpie-git), so the rest are read only when none is.
func magpieFirst(names []string) []string {
	slices.SortStableFunc(names, func(a, b string) int {
		ma, mb := strings.HasPrefix(a, "magpie"), strings.HasPrefix(b, "magpie")
		switch {
		case ma && !mb:
			return -1
		case mb && !ma:
			return 1
		}
		return 0
	})
	return names
}

// pacmanOwner reads pacman's local database, a folder per installed
// package (name-version-release) holding the list of its files, each
// without its leading slash.
func pacmanOwner(root, p string) (string, bool) {
	dir := filepath.Join(root, "var/lib/pacman/local")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	want := aliases(p)
	for i := range want {
		want[i] = strings.TrimPrefix(want[i], "/")
	}
	for _, n := range magpieFirst(names) {
		if listed(filepath.Join(dir, n, "files"), want) {
			return pacmanName(filepath.Join(dir, n), n), true
		}
	}
	return "", false
}

// pacmanName is the package's name, from its desc (%NAME%), else its
// folder's name without its version and release.
func pacmanName(dir, folder string) string {
	if b, err := os.ReadFile(filepath.Join(dir, "desc")); err == nil {
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			if strings.TrimSpace(l) == "%NAME%" && i+1 < len(lines) {
				if n := strings.TrimSpace(lines[i+1]); n != "" {
					return n
				}
			}
		}
	}
	parts := strings.Split(folder, "-")
	if len(parts) > 2 {
		return strings.Join(parts[:len(parts)-2], "-")
	}
	return folder
}

// dpkgOwner reads dpkg's file lists, one name.list (or name:arch.list) per
// installed package, each line an absolute path.
func dpkgOwner(root, p string) (string, bool) {
	dir := filepath.Join(root, "var/lib/dpkg/info")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".list") {
			names = append(names, e.Name())
		}
	}
	want := aliases(p)
	for _, n := range magpieFirst(names) {
		if listed(filepath.Join(dir, n), want) {
			name, _, _ := strings.Cut(strings.TrimSuffix(n, ".list"), ":")
			return name, true
		}
	}
	return "", false
}

// listed is whether the file list at path has a line equal to one of want.
func listed(path string, want []string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if slices.Contains(want, strings.TrimSpace(sc.Text())) {
			return true
		}
	}
	return false
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

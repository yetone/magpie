package ccswitchlink

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/proc"
)

const mime = "x-scheme-handler/" + Scheme

// magpieEntry is the desktop entry magpie v0.1.3981 to v0.1.4454 wrote for
// ccswitch:// and made the default when it had none.
const magpieEntry = "magpie-ccswitch.desktop"

// executable is this program's path; tests stand in for it.
var executable = os.Executable

// start starts CC Switch; tests stand in for it.
var start = func(argv []string) error { return proc.StartReaped(proc.Command(argv[0], argv[1:]...)) }

// forward runs the Exec of CC Switch's desktop entry with the link. Then,
// when the desktop's default for ccswitch:// is magpie's entry, that entry
// goes and CC Switch's is made the default.
func forward(link string) error {
	id, exec := entry()
	if id == "" {
		return ErrNotInstalled
	}
	argv := expand(exec, link)
	if len(argv) == 0 {
		return ErrNotInstalled
	}
	if err := start(argv); err != nil {
		return err
	}
	out, _ := proc.Command("xdg-mime", "query", "default", mime).Output()
	if strings.TrimSpace(string(out)) != magpieEntry {
		return nil
	}
	dir := filepath.Join(dataHome(), "applications")
	if ours(filepath.Join(dir, magpieEntry)) {
		_ = os.Remove(filepath.Join(dir, magpieEntry))
		_ = proc.Command("update-desktop-database", dir).Run()
	}
	return proc.Command("xdg-mime", "default", id, mime).Run()
}

// dataHome is $XDG_DATA_HOME, or ~/.local/share without it.
func dataHome() string {
	if d := appdir.Getenv("XDG_DATA_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// dataDirs are the folders desktop entries are looked up in, the user's
// first.
func dataDirs() []string {
	dirs := []string{dataHome()}
	sys := appdir.Getenv("XDG_DATA_DIRS")
	if sys == "" {
		sys = "/usr/local/share:/usr/share"
	}
	for _, d := range filepath.SplitList(sys) {
		if d != "" && !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// entry is the id and Exec of the desktop entry that handles ccswitch://
// other than magpie's: one whose Exec takes a URL (cc-switch-handler.desktop)
// before one that doesn't ("CC Switch.desktop" of the package, whose Exec
// is then given the link as its last argument). An entry earlier in the
// data folders hides one of the same id later.
func entry() (id, exec string) {
	seen := map[string]bool{}
	var plain, plainExec string
	for _, d := range dataDirs() {
		files, _ := filepath.Glob(filepath.Join(d, "applications", "*.desktop"))
		for _, f := range files {
			name := filepath.Base(f)
			if seen[name] {
				continue
			}
			seen[name] = true
			if strings.HasPrefix(name, "magpie") {
				continue
			}
			x, ok := handles(f)
			if !ok {
				continue
			}
			if strings.Contains(x, "%u") || strings.Contains(x, "%U") {
				return name, x
			}
			if plain == "" {
				plain, plainExec = name, x
			}
		}
	}
	return plain, plainExec
}

// handles is the Exec of a desktop entry that lists ccswitch:// in its
// MimeType and isn't hidden.
func handles(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var exec string
	claims, in := false, false
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			in = l == "[Desktop Entry]"
			continue
		}
		if !in {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Exec":
			exec = strings.TrimSpace(v)
		case "MimeType":
			claims = slices.Contains(strings.Split(v, ";"), mime)
		case "Hidden":
			if strings.TrimSpace(v) == "true" {
				return "", false
			}
		}
	}
	return exec, claims && exec != ""
}

// expand is an Exec line as arguments, with the link for %u, %U, %f or
// %F, or as the last argument when it has none of them. Other field codes
// go, and %% is a %.
func expand(exec, link string) []string {
	var argv []string
	put := false
	for _, w := range words(exec) {
		switch w {
		case "%u", "%U", "%f", "%F":
			argv = append(argv, link)
			put = true
			continue
		case "%i", "%c", "%k":
			continue
		}
		var b strings.Builder
		for i := 0; i < len(w); i++ {
			if w[i] == '%' && i+1 < len(w) {
				i++
				if w[i] == '%' {
					b.WriteByte('%')
				}
				continue
			}
			b.WriteByte(w[i])
		}
		argv = append(argv, b.String())
	}
	if len(argv) > 0 && !put {
		argv = append(argv, link)
	}
	return argv
}

// words splits an Exec line as the Desktop Entry spec quotes it: double
// quotes around an argument, a backslash before ", `, $ or \ inside them.
func words(s string) []string {
	var out []string
	var b strings.Builder
	quoted, have := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quoted && c == '\\' && i+1 < len(s):
			i++
			b.WriteByte(s[i])
		case c == '"':
			quoted = !quoted
			have = true
		case !quoted && (c == ' ' || c == '\t'):
			if have {
				out = append(out, b.String())
				b.Reset()
				have = false
			}
		default:
			b.WriteByte(c)
			have = true
		}
	}
	if have {
		out = append(out, b.String())
	}
	return out
}

// ours reports whether a desktop entry runs this executable, or one that
// is gone: magpie's own, not another magpie's.
func ours(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	self, _ := executable()
	for _, l := range strings.Split(string(b), "\n") {
		if x, ok := strings.CutPrefix(l, "Exec="); ok {
			run := strings.TrimSuffix(x, " %u")
			if run == self {
				return true
			}
			_, err := os.Stat(run)
			return os.IsNotExist(err)
		}
	}
	return false
}

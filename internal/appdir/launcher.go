package appdir

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Launcher is the path a record the system keeps — the autostart entry,
// the desktop entry that opens magpie:// links, the service, a link on the
// PATH — should start this magpie by, when the running file is not that
// path; "" when it is.
//
// A package's wrapper names itself in MAGPIE_EXECUTABLE: a Nix or Guix
// wrapper sets up GTK's environment and runs the real binary, so
// os.Executable is a file that can't start magpie by itself (#1747,
// HoronLee's file chooser aborting with "No GSettings schemas"). A path in
// a Nix or Guix store changes with every upgrade and is collected after,
// so it is mapped to the profile's link that leads to the same file: the
// one on the PATH, a profile's bin, or on a Mac its Applications.
func Launcher() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return launcher(exe, os.Getenv("MAGPIE_EXECUTABLE"), launcherCandidates(), stores)
}

// stores are the package stores whose paths change with every upgrade.
var stores = []string{"/nix/store/", "/gnu/store/"}

// launcher is Launcher for the running file exe, the wrapper named, the
// links that may lead to it, and the stores.
func launcher(exe, named string, candidates, stores []string) string {
	inStore := func(p string) bool {
		p = filepath.ToSlash(p)
		for _, s := range stores {
			if strings.HasPrefix(p, s) {
				return true
			}
		}
		return false
	}
	target := ""
	switch {
	case named != "" && filepath.IsAbs(named) && isRegular(named):
		target = filepath.Clean(named)
	case inStore(resolvedPath(exe)):
		target = exe
	default:
		return ""
	}
	want := resolvedPath(target)
	if !inStore(want) {
		return target
	}
	for _, c := range candidates {
		if c == "" || !filepath.IsAbs(c) || inStore(c) {
			continue
		}
		if r := resolvedPath(c); r == want && isRegular(r) {
			return c
		}
	}
	return target
}

// launcherCandidates are the links a profile keeps to magpie, the most
// likely first: the PATH's, then each profile's (a user's nix profile, the
// per-user and system ones of NixOS and nix-darwin, Nix's default, Guix's).
// On a Mac the app's program in a profile's Applications comes before them,
// so a login item starts the app.
func launcherCandidates() []string {
	home, _ := os.UserHomeDir()
	state := Getenv("XDG_STATE_HOME")
	if state == "" && home != "" {
		state = filepath.Join(home, ".local", "state")
	}
	var roots []string
	if home != "" {
		roots = append(roots, filepath.Join(home, ".nix-profile"))
	}
	if state != "" {
		roots = append(roots, filepath.Join(state, "nix", "profile"))
	}
	if u := os.Getenv("USER"); u != "" {
		roots = append(roots, filepath.Join("/etc/profiles/per-user", u))
	}
	roots = append(roots, "/run/current-system/sw", "/nix/var/nix/profiles/default")
	if home != "" {
		roots = append(roots, filepath.Join(home, ".guix-profile"))
	}
	roots = append(roots, "/run/current-system/profile")

	var out []string
	if runtime.GOOS == "darwin" {
		const prog = "magpie.app/Contents/MacOS/magpie"
		for _, r := range roots {
			out = append(out, filepath.Join(r, "Applications", prog))
		}
		out = append(out, filepath.Join("/Applications/Nix Apps", prog))
		if home != "" {
			out = append(out, filepath.Join(home, "Applications", "Home Manager Apps", prog))
		}
	}
	for _, d := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.IsAbs(d) {
			out = append(out, filepath.Join(d, "magpie"))
		}
	}
	for _, r := range roots {
		out = append(out, filepath.Join(r, "bin", "magpie"))
	}
	return out
}

func resolvedPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func isRegular(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

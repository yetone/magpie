package appdir

import (
	"os"
	"path/filepath"
	"testing"
)

// The Nix flake's Linux package runs libexec/magpie/magpie through the
// wrapper bin/magpie, which sets MAGPIE_EXECUTABLE to itself, and a profile
// links bin/magpie. Entries the system keeps name the profile's link: the
// wrapper's store path is gone after an upgrade and a collection, and the
// real binary can't start the GUI without the wrapper's environment.
func TestLauncherNamesTheProfilesLink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(root, "store") + "/"
	pkg := filepath.Join(root, "store", "abc-magpie-0.1.4674")
	real := filepath.Join(pkg, "libexec", "magpie", "magpie")
	wrapper := filepath.Join(pkg, "bin", "magpie")
	app := filepath.Join(pkg, "Applications", "magpie.app", "Contents", "MacOS", "magpie")
	for _, f := range []string{real, wrapper, app} {
		write(t, f)
	}
	// a profile is a store path of links, and the profile's name a link to it
	env := filepath.Join(root, "store", "def-profile")
	link(t, wrapper, filepath.Join(env, "bin", "magpie"))
	link(t, filepath.Join(pkg, "Applications"), filepath.Join(env, "Applications"))
	profile := filepath.Join(root, "home", ".nix-profile")
	link(t, env, profile)
	// another magpie on the PATH, a copy install.sh put there
	other := filepath.Join(root, "home", ".local", "bin", "magpie")
	write(t, other)
	stores := []string{store}

	byProfile := filepath.Join(profile, "bin", "magpie")
	candidates := []string{other, filepath.Join(env, "bin", "magpie"), byProfile}
	if got := launcher(real, wrapper, candidates, stores); got != byProfile {
		t.Fatalf("the wrapper with a profile: %q, want %q", got, byProfile)
	}
	// nix run: no profile has it, so the wrapper itself
	if got := launcher(real, wrapper, []string{other}, stores); got != wrapper {
		t.Fatalf("the wrapper with no profile: %q, want %q", got, wrapper)
	}
	// no wrapper named: the running file, when it is in a store (the Mac's
	// app, whose bin/magpie is a link to its program)
	appLink := filepath.Join(profile, "Applications", "magpie.app", "Contents", "MacOS", "magpie")
	if got := launcher(app, "", []string{appLink, byProfile}, stores); got != appLink {
		t.Fatalf("the Mac's app: %q, want %q", got, appLink)
	}
	// a name that is no file, or relative, is not taken
	for _, named := range []string{filepath.Join(root, "gone"), "bin/magpie"} {
		if got := launcher(other, named, candidates, stores); got != "" {
			t.Fatalf("MAGPIE_EXECUTABLE=%s: %q", named, got)
		}
	}
	// a wrapper out of any store stays put already
	stable := filepath.Join(root, "opt", "magpie")
	write(t, stable)
	if got := launcher(real, stable, candidates, stores); got != stable {
		t.Fatalf("a stable wrapper: %q", got)
	}
	// and a magpie out of any store, with nothing named, is its own launcher
	if got := launcher(other, "", candidates, stores); got != "" {
		t.Fatalf("a plain install: %q", got)
	}
}

func write(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("magpie"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func link(t *testing.T, to, at string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(to, at); err != nil {
		t.Skip("no symlinks here:", err)
	}
}

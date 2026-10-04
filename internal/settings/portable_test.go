package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/testenv"
)

// A data folder beside magpie takes every file of its own (#508): the
// settings land in it, whatever HOME and XDG_CONFIG_HOME say, and the
// files of an install from before the rename aren't copied in.
func TestPortableSettings(t *testing.T) {
	r, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	testenv.SetHome(t, filepath.Join(r, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(r, "home", ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(r, "home", ".cache"))
	t.Setenv("APPIMAGE", "")
	t.Cleanup(func() { appdir.UseExecutable("") })

	exe := filepath.Join(r, "Magpie", "magpie.exe")
	data := filepath.Join(r, "Magpie", "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(r, "home", ".config", "dial")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "settings.json"), []byte(`{"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	appdir.UseExecutable(exe)
	if got, want := Path(), filepath.Join(data, "settings.json"); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
	if Dir() != data || Portable() != data {
		t.Fatalf("Dir() = %q, Portable() = %q, want %q", Dir(), Portable(), data)
	}
	Migrate()
	s := Load()
	s.Theme = "light"
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "settings.json")); err != nil {
		t.Fatalf("settings not saved in the data folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r, "home", ".config", "magpie")); !os.IsNotExist(err) {
		t.Fatalf("the profile's ~/.config/magpie was touched: %v", err)
	}

	// without the data folder it's the profile again
	appdir.UseExecutable("")
	if got, want := Path(), filepath.Join(r, "home", ".config", "magpie", "settings.json"); got != want {
		t.Fatalf("installed Path() = %q, want %q", got, want)
	}
}

package desktopdir

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// ran makes dir a userData Desktop has run in, at t.
func ran(tb testing.TB, dir string, t time.Time) {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatal(err)
	}
	p := filepath.Join(dir, "Local State")
	if err := os.WriteFile(p, []byte(`{}`), 0o644); err != nil {
		tb.Fatal(err)
	}
	if err := os.Chtimes(p, t, t); err != nil {
		tb.Fatal(err)
	}
}

// Desktop from the setup.exe that installs its MSIX package (Kilig on
// Discord, Windows 10 LTSC 21H2) keeps its data in the package's
// LocalCache, nothing in %APPDATA%\Claude: those are its folders, whatever
// the package's publisher id.
func TestFindMSIX(t *testing.T) {
	for _, pub := range []string{"pzs8sxrjxfjjc", "q1w2e3r4t5y6z"} {
		home := t.TempDir()
		local, roaming := filepath.Join(home, "AppData", "Local"), filepath.Join(home, "AppData", "Roaming")
		pkg := filepath.Join(local, "Packages", "Claude_"+pub, "LocalCache")
		ran(t, filepath.Join(pkg, "Roaming", "Claude"), time.Now())
		os.MkdirAll(filepath.Join(pkg, "Local", "Claude", "logs"), 0o755)
		os.MkdirAll(filepath.Join(local, "Packages", "Microsoft.WindowsStore_8wekyb3d8bbwe"), 0o755)
		os.MkdirAll(roaming, 0o755)

		got := Find("windows", home, env(map[string]string{"LOCALAPPDATA": local, "APPDATA": roaming}))
		want := Dirs{Data: filepath.Join(pkg, "Roaming", "Claude"), Mode: filepath.Join(pkg, "Local", "Claude"),
			ThreeP: filepath.Join(pkg, "Local", "Claude-3p"), Package: pkg}
		if got != want {
			t.Errorf("%s:\n got %+v\nwant %+v", pub, got, want)
		}
		if all := got.All(); !reflect.DeepEqual(all, []string{want.Data, want.Mode, want.ThreeP}) {
			t.Errorf("%s: all %v", pub, all)
		}
		// without the variables: the profile's own AppData
		if got := Find("windows", home, env(nil)); got != want {
			t.Errorf("%s, no variables: %+v", pub, got)
		}
	}
}

// The usual install (Squirrel: %APPDATA%\Claude, %LOCALAPPDATA%\Claude-3p)
// keeps its folders, and so does a machine with no Desktop at all. An
// %APPDATA%\Claude holding only claude_desktop_config.json (another tool's
// MCP servers) is not a Desktop that runs there; when both have run, the
// one that ran last is Desktop's.
func TestFindWindowsClassic(t *testing.T) {
	home := t.TempDir()
	local, roaming := filepath.Join(home, "Local"), filepath.Join(home, "Roaming")
	e := env(map[string]string{"LOCALAPPDATA": local, "APPDATA": roaming})
	classic := Dirs{Data: filepath.Join(roaming, "Claude"), Mode: filepath.Join(local, "Claude"), ThreeP: filepath.Join(local, "Claude-3p")}
	if got := Find("windows", home, e); got != classic {
		t.Errorf("nothing installed: %+v", got)
	}
	ran(t, filepath.Join(roaming, "Claude"), time.Now().Add(-time.Hour))
	if got := Find("windows", home, e); got != classic {
		t.Errorf("usual install: %+v", got)
	}

	pkg := filepath.Join(local, "Packages", "Claude_pzs8sxrjxfjjc", "LocalCache")
	os.MkdirAll(pkg, 0o755)
	if got := Find("windows", home, e); got != classic {
		t.Errorf("package never run: %+v", got)
	}
	ran(t, filepath.Join(pkg, "Roaming", "Claude"), time.Now().Add(-2*time.Hour))
	if got := Find("windows", home, e); got != classic {
		t.Errorf("package ran before the usual one: %+v", got)
	}
	ran(t, filepath.Join(pkg, "Roaming", "Claude"), time.Now())
	if got := Find("windows", home, e); got.Package != pkg || got.Data != filepath.Join(pkg, "Roaming", "Claude") {
		t.Errorf("package ran last: %+v", got)
	}

	// another tool's MCP file alone in %APPDATA%\Claude
	home2 := t.TempDir()
	local2, roaming2 := filepath.Join(home2, "Local"), filepath.Join(home2, "Roaming")
	os.MkdirAll(filepath.Join(roaming2, "Claude"), 0o755)
	os.WriteFile(filepath.Join(roaming2, "Claude", "claude_desktop_config.json"), []byte(`{"mcpServers":{}}`), 0o644)
	pkg2 := filepath.Join(local2, "Packages", "Claude_pzs8sxrjxfjjc", "LocalCache")
	ran(t, filepath.Join(pkg2, "Roaming", "Claude"), time.Now())
	if got := Find("windows", home2, env(map[string]string{"LOCALAPPDATA": local2, "APPDATA": roaming2})); got.Package != pkg2 {
		t.Errorf("only an MCP file in %%APPDATA%%\\Claude: %+v", got)
	}
}

// macOS and Linux have one Claude folder for both, and no package.
func TestFindOthers(t *testing.T) {
	home := t.TempDir()
	as := filepath.Join(home, "Library", "Application Support")
	if got := Find("darwin", home, env(nil)); got != (Dirs{Data: filepath.Join(as, "Claude"), Mode: filepath.Join(as, "Claude"), ThreeP: filepath.Join(as, "Claude-3p")}) {
		t.Errorf("darwin: %+v", got)
	} else if len(got.All()) != 2 {
		t.Errorf("darwin all: %v", got.All())
	}
	for _, x := range []string{"", "relative/dir"} {
		c := filepath.Join(home, ".config")
		if got := Find("linux", home, env(map[string]string{"XDG_CONFIG_HOME": x})); got != (Dirs{Data: filepath.Join(c, "Claude"), Mode: filepath.Join(c, "Claude"), ThreeP: filepath.Join(c, "Claude-3p")}) {
			t.Errorf("linux %q: %+v", x, got)
		}
	}
}

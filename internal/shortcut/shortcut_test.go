package shortcut

import "testing"

func TestTarget(t *testing.T) {
	for in, want := range map[string]string{
		`C:\Tools\magpie-windows-amd64.exe`:          `C:\Tools\magpie-windows-amd64.exe`,
		`C:\Tools\magpie.exe.old`:                    `C:\Tools\magpie.exe`,
		`C:\Tools\magpie.exe.old-3`:                  `C:\Tools\magpie.exe`,
		`C:\Tools\magpie.exe.new`:                    `C:\Tools\magpie.exe`,
		`C:\Tools\MAGPIE.EXE.OLD`:                    `C:\Tools\MAGPIE.EXE`,
		`C:\old\magpie.exe`:                          `C:\old\magpie.exe`,
		`C:\Tools\magpie.exe.older`:                  `C:\Tools\magpie.exe.older`,
		`/usr/local/bin/magpie`:                      `/usr/local/bin/magpie`,
		`C:\Users\me\Downloads\magpie (1).exe.old-2`: `C:\Users\me\Downloads\magpie (1).exe`,
	} {
		if got := Target(in); got != want {
			t.Errorf("Target(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStale(t *testing.T) {
	exe := "/tools/magpie.exe"
	for _, c := range []struct {
		target, dir string
		stale       bool
	}{
		{"", "", true},                            // no shortcut yet
		{"/tools/magpie.exe", "/tools", false},    // already this one
		{"/TOOLS/Magpie.EXE", "/Tools/", false},   // the same, in another case
		{"/downloads/magpie.exe", "/tools", true}, // the exe was moved
		{"/tools/magpie.exe", "/downloads", true}, // starts in another folder
		{"/tools/magpie.exe", "", true},           // in no folder
	} {
		if got := Stale(c.target, c.dir, exe); got != c.stale {
			t.Errorf("Stale(%q, %q) = %v, want %v", c.target, c.dir, got, c.stale)
		}
	}
}

// Installed apps runs the exe it names, quoted, with the arguments the
// CLI opens the window on (the path has spaces under a user's name).
func TestUninstallCommand(t *testing.T) {
	got := UninstallCommand(`C:\Users\Ann Lee\AppData\Local\Programs\magpie\magpie.exe`)
	want := `"C:\Users\Ann Lee\AppData\Local\Programs\magpie\magpie.exe" uninstall --window`
	if got != want {
		t.Fatalf("UninstallCommand = %s, want %s", got, want)
	}
}

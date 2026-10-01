package update

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// app makes a stand-in .app holding the text v.
func app(t *testing.T, path, v string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "Contents", "v"), []byte(v), 0o644); err != nil {
		t.Fatal(err)
	}
}

func version(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(path, "Contents", "v"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A folder magpie may not write to: Install fails with a permission error,
// leaves the app as it was, and keeps what it staged.
func TestInstallNeedsAdmin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a read-only folder and the administrator's password are a Mac's and Linux's")
	}
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	root := t.TempDir()
	apps := filepath.Join(root, "Applications")
	bundle := filepath.Join(apps, "magpie.app")
	app(t, bundle, "old")
	staged := filepath.Join(root, "cache", "app", "magpie.app")
	app(t, staged, "new")
	os.Chmod(apps, 0o555)
	defer os.Chmod(apps, 0o755)

	if stageDir(apps) == filepath.Join(apps, ".magpie-update") {
		t.Error("stageDir picked a folder it can't write to")
	}
	err := Install(staged, bundle)
	if !NeedsAdmin(err) {
		t.Fatalf("Install = %v, want a permission error", err)
	}
	if version(t, bundle) != "old" || version(t, staged) != "new" {
		t.Error("a failed Install changed something")
	}
}

// The script InstallAsAdmin runs as root swaps the apps, awkward names and
// all; run here as the user in a folder they own.
func TestSwapScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the swap runs as root under /bin/sh, on a Mac or Linux")
	}
	dir := filepath.Join(t.TempDir(), `it's "a" \ $(dir) `)
	bundle, staged, old := filepath.Join(dir, "magpie.app"), filepath.Join(dir, "s", "magpie.app"), filepath.Join(dir, "s", "old.app")
	app(t, bundle, "old")
	app(t, staged, "new")
	if b, err := exec.Command("/bin/sh", "-c", swapScript(staged, bundle, old)).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	if version(t, bundle) != "new" {
		t.Error("the new app isn't in place")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the old app was left behind")
	}

	// a failed second move puts the app back
	os.RemoveAll(staged)
	if exec.Command("/bin/sh", "-c", swapScript(staged, bundle, old)).Run() == nil {
		t.Error("the swap succeeded without a staged app")
	}
	if version(t, bundle) != "new" {
		t.Error("the app wasn't put back")
	}
}

// On a Mac the script goes to osascript as an AppleScript string, and a
// dismissed prompt is ErrCanceled.
func TestAsAdmin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("osascript")
	}
	defer func(r func(string, ...string) ([]byte, error)) { run = r }(run)
	var got []string
	run = func(name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return []byte("execution error: User canceled. (-128)"), errors.New("exit status 1")
	}
	script := swapScript(`/a "b"/s.app`, `/x\y.app`, "/o.app")
	if err := asAdmin(script); !errors.Is(err, ErrCanceled) {
		t.Errorf("asAdmin = %v, want ErrCanceled", err)
	}
	if len(got) != 3 || got[0] != "osascript" || !strings.Contains(got[2], `"`+appleString(script)+`"`) || !strings.HasSuffix(got[2], "with administrator privileges") {
		t.Errorf("ran %q", got)
	}
	// AppleScript unescapes it back to the script
	out, err := exec.Command("osascript", "-e", `"`+appleString(script)+`"`).Output()
	if err != nil || strings.TrimSuffix(string(out), "\n") != script {
		t.Errorf("AppleScript read %q (%v), want %q", out, err, script)
	}
}

func TestStuck(t *testing.T) {
	if Stuck("/private/var/folders/xy/T/AppTranslocation/1234/d/magpie.app") != "translocated" {
		t.Error("a translocated app isn't stuck")
	}
	if Stuck(t.TempDir()) != "" {
		t.Error("a writable folder is stuck")
	}
}

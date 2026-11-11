package testenv

import (
	"os"
	"runtime"
	"testing"
)

func TestWindowsDrive(t *testing.T) {
	for dir, want := range map[string]bool{
		"/mnt/c":                  true,
		"/mnt/c/Windows/System32": true,
		"/mnt/D/Tools":            true,
		"/mnt/wsl":                false,
		"/mnt/wslg/runtime-dir":   false,
		"/mnt/":                   false,
		"/mnt":                    false,
		"/usr/bin":                false,
		"/home/u/mnt/c":           false,
		"/mnt/c2/bin":             false,
	} {
		if got := windowsDrive(dir); got != want {
			t.Errorf("windowsDrive(%q) = %v, want %v", dir, got, want)
		}
	}
}

// In WSL a test binary's PATH has no Windows drive folder: each is a 9P
// stat at every look, and they hold the machine's Windows programs.
func TestIsolateDropsWindowsPath(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("WSL is Linux")
	}
	t.Setenv("WSL_DISTRO_NAME", "magpie-test")
	t.Setenv("PATH", "/usr/bin:/mnt/c/Windows/System32:/mnt/wslg/x:/bin:/mnt/d")
	if err := dropWindowsPath(); err != nil {
		t.Fatal(err)
	}
	if got, want := os.Getenv("PATH"), "/usr/bin:/mnt/wslg/x:/bin"; got != want {
		t.Errorf("PATH = %q, want %q", got, want)
	}
}

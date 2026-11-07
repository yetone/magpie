package proc

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// On a real Windows with WSL (MAGPIE_WSL_REGISTRY_LIVE set): every distro
// wsl.exe -l -q lists (which reads and starts nothing) is one the registry
// has, so the gate never reads none while wsl.exe has some. The registry
// may have more: a distro being installed or uninstalled (its State), which
// wsl.exe leaves out, keeps wsl.exe asked.
func TestWSLRegistryLive(t *testing.T) {
	if os.Getenv("MAGPIE_WSL_REGISTRY_LIVE") == "" {
		t.Skip("MAGPIE_WSL_REGISTRY_LIVE not set")
	}
	names, ok := registeredDistros()
	if !ok {
		t.Fatal("the registry couldn't be read")
	}
	t.Logf("registered: %q, package %q", names, wslPackageVersion())
	// the real wsl.exe, not testenv's stand-in, and not through Command
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "wsl.exe"), "-l", "-q")
	cmd.Env = append(os.Environ(), "WSL_UTF8=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("wsl.exe -l -q: %v", err)
	}
	var listed []string
	for _, l := range strings.Split(decodeWSL(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			listed = append(listed, l)
		}
	}
	if len(listed) == 0 {
		t.Fatal("wsl.exe lists no distro: nothing to compare")
	}
	for _, n := range listed {
		if !slices.Contains(names, n) {
			t.Errorf("wsl.exe lists %q, the registry hasn't it (%q)", n, names)
		}
	}
	t.Logf("wsl.exe lists %q", listed)
	if wslPackageVersion() == "" {
		t.Fatal("no WSL package version read")
	}
}

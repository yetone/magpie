package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/testenv"
	"gopkg.in/yaml.v3"
)

// A WSL omp under NAT reaches the gateway at Windows' address, where a
// request without a named key is turned away (whqtian on Discord: it
// connected only once auth: none was swapped for the key by hand). Its
// models.yml carries the key the gateway is shared on the network with;
// one on loopback stays keyless, and so does one while nothing is shared.
func TestOmpKeyBeyondLoopback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	wsl := "http://172.23.80.1:3425"
	if e := ompProviderAt(wsl, "18.4.8"); e.Auth != "none" || e.APIKey != "" {
		t.Fatalf("not shared, yet auth %q key %q", e.Auth, e.APIKey)
	}
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	key := access.LANSecret()
	if !strings.HasPrefix(key, access.Prefix) {
		t.Fatalf("LAN key %q", key)
	}
	e := ompProviderAt(wsl, "18.4.8")
	if e.APIKey != key || e.Auth != "" {
		t.Fatalf("WSL omp under NAT: auth %q key %q, want the LAN key", e.Auth, e.APIKey)
	}
	b, _ := yaml.Marshal(e)
	if strings.Contains(string(b), "auth:") || !strings.Contains(string(b), "apiKey: "+key) {
		t.Errorf("models.yml entry:\n%s", b)
	}
	for _, gw := range []string{"http://127.0.0.1:3425", "http://localhost:3425"} {
		if e := ompProviderAt(gw, "18.4.8"); e.Auth != "none" || e.APIKey != "" {
			t.Errorf("%s: auth %q key %q", gw, e.Auth, e.APIKey)
		}
	}
	if err := access.ConfigureLAN(false, false); err != nil {
		t.Fatal(err)
	}
	if e := ompProviderAt(wsl, "18.4.8"); e.Auth != "none" || e.APIKey != "" {
		t.Errorf("sharing off, yet auth %q key %q", e.Auth, e.APIKey)
	}
}

// The probe finds an omp bun installed though ~/.bun/bin is put on PATH
// only by ~/.bashrc, which sh -l doesn't read, and asks it its version
// with bun on PATH for its #!/usr/bin/env bun (whqtian on Discord: a WSL
// omp's version stayed unknown, so max stayed xhigh).
func TestWSLProbeFindsBunOmp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the probe runs in a Linux distro")
	}
	to, err := exec.LookPath("timeout")
	if err != nil {
		t.Skip("no timeout(1)")
	}
	home := t.TempDir()
	bun := filepath.Join(home, ".bun", "bin")
	sys := filepath.Join(home, "sys") // PATH: only timeout, nothing of omp's
	for _, d := range []string{bun, sys, filepath.Join(home, ".omp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	testenv.Program(t, filepath.Join(bun, "bun"), "#!/bin/sh\necho omp/16.5.1\n")
	// a program too: the probe runs it under its own timeout 10, which a
	// newly written file's first-run check on macOS outlasts under load
	testenv.Program(t, filepath.Join(bun, "omp"), "#!/usr/bin/env bun\n")
	if err := os.Symlink(to, filepath.Join(sys, "timeout")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", wslProbeScript)
	cmd.Env = []string{"HOME=" + home, "PATH=" + sys + ":/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	d := parseProbe("U", string(out))
	if d == nil || !d.Has["bin:omp"] || d.Versions["omp"] != "16.5.1" {
		t.Fatalf("probe said:\n%s\nread as %+v", out, d)
	}
	if !ompTakesMax(d.place("omp@wsl:U").version) {
		t.Error("its models wouldn't offer max")
	}
}

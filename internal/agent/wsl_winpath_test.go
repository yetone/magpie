package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/testenv"
)

// WSL puts Windows' PATH on the distro's, so `command -v pi` there finds
// Windows npm's pi under /mnt/c (#406). A command on one of Windows' drives
// — /mnt/<letter>/, or wherever /proc/mounts says a drive is — finds no
// agent, for every kind; one in the distro's own folders still does.
func TestWSLWindowsCommandIsNotTheDistros(t *testing.T) {
	d := parseProbe("Ubuntu-24.04", "home:/home/me\n"+
		"bin:pi /mnt/c/Users/me/AppData/Roaming/npm/pi\n"+
		"bin:codex /mnt/d/tools/npm/codex\n"+
		"bin:claude /win/c/Users/me/.local/bin/claude\n"+
		"win:/win/c\n"+
		"bin:omo /mnt/data/omo/bin/omo\n")
	if d == nil {
		t.Fatal("no distro")
	}
	for _, k := range []string{"bin:pi", "bin:codex", "bin:claude"} {
		if d.Has[k] {
			t.Errorf("%s taken from Windows: %v", k, d.Has)
		}
	}
	if !d.Has["bin:omo"] {
		t.Errorf("a distro's own /mnt/data command lost: %v", d.Has)
	}
	for _, k := range wslKinds {
		if k.id != "omo" && k.found(*d) {
			t.Errorf("%s found", k.id)
		}
	}

	// Codex installed in the distro (#251's case) is found as before, with
	// a space in its path kept
	d = parseProbe("U", "home:/home/me\nbin:codex /home/me/.nvm/versions/node/v22/bin/codex\nbin:pi /opt/my tools/pi\nwin:/mnt/c\n")
	if d == nil || !d.Has["bin:codex"] || !d.Has["bin:pi"] || !wslFound(*d) {
		t.Fatalf("%+v", d)
	}
	if !onWindowsDrive("/mnt/C/x", nil) || onWindowsDrive("/mnt/c", nil) || onWindowsDrive("/mnt/cd/x", nil) || onWindowsDrive("/usr/bin/pi", nil) {
		t.Fatal("onWindowsDrive")
	}
}

// The probe itself says where each command is: a pi on PATH comes out as
// bin:pi <its path>.
func TestWSLProbeScriptSaysWhere(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	// the probe is a distro's: a Windows shell of its own (Git Bash's)
	// reports the folder it is given as a drive, and a program on a drive
	// is not the distro's, which is not what this check is about
	if runtime.GOOS == "windows" {
		t.Skip("a Windows shell reports the temp folder as a drive")
	}
	home, bin := t.TempDir(), t.TempDir()
	testenv.Program(t, filepath.Join(bin, "pi"), "#!/bin/sh\n")
	cmd := exec.Command(sh, "-c", wslProbeScript)
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "bin:pi "+filepath.Join(bin, "pi")+"\n") {
		t.Fatalf("%s", out)
	}
	if d := parseProbe("U", string(out)); d == nil || !d.Has["bin:pi"] {
		t.Fatalf("%+v", d)
	}
}

// What an older probe found by a command alone in a distro now stopped
// isn't listed on its word: the command may have been Windows'. A distro it
// found by its folder stays, without the command.
func TestWSLOldProbeBinsForgotten(t *testing.T) {
	codexHome(t, "", "")
	seen := map[string]*distro{
		"Ubuntu-24.04": {Name: "Ubuntu-24.04", Home: "/home/me", Has: map[string]bool{"bin:pi": true}},
		"Debian":       {Name: "Debian", Home: "/home/me", Has: map[string]bool{"dir:.codex": true, "bin:codex": true}},
		"New":          {Name: "New", Home: "/home/me", Has: map[string]bool{"bin:pi": true}, Probe: wslProbeVersion},
	}
	b, _ := json.Marshal(seen)
	os.MkdirAll(filepath.Dir(wslStatePath()), 0o755)
	os.WriteFile(wslStatePath(), b, 0o600)
	fakeWSL(t, "Ubuntu-24.04\r\nDebian\r\nNew\r\n", "", nil, nil)

	ds := wslDistros()
	if len(ds) != 2 || ds[0].Name != "Debian" || ds[0].Has["bin:codex"] || ds[1].Name != "New" {
		t.Fatalf("%+v", ds)
	}
	for _, a := range wslAgentsOf(ds) {
		if a.ID == "pi@wsl:Ubuntu-24.04" {
			t.Fatal("Pi · WSL Ubuntu-24.04 listed")
		}
	}
	var kept map[string]*distro
	b, _ = os.ReadFile(wslStatePath())
	json.Unmarshal(b, &kept)
	if len(kept) != 2 || kept["Ubuntu-24.04"] != nil || kept["Debian"] == nil {
		t.Fatalf("kept %s", b)
	}
}

// A running distro with a stale bin:pi from before is probed again, and
// its Windows pi drops it from the list and from wsl.json.
func TestWSLRunningStaleWindowsPiReprobed(t *testing.T) {
	codexHome(t, "", "")
	b, _ := json.Marshal(map[string]*distro{"Ubuntu-24.04": {Name: "Ubuntu-24.04", Home: "/home/me",
		Has: map[string]bool{"bin:pi": true}, Probe: wslProbeVersion}})
	os.MkdirAll(filepath.Dir(wslStatePath()), 0o755)
	os.WriteFile(wslStatePath(), b, 0o600)
	asked, _ := fakeWSL(t, "Ubuntu-24.04\r\n", "Ubuntu-24.04\r\n", map[string]string{
		"Ubuntu-24.04": "home:/home/me\nbin:pi /mnt/c/Users/me/AppData/Roaming/npm/pi\nwin:/mnt/c\n",
	}, nil)
	if ds := wslDistros(); len(ds) != 0 {
		t.Fatalf("%+v", ds)
	}
	if strings.Join(*asked, ",") != "Ubuntu-24.04" {
		t.Fatalf("asked %v", *asked)
	}
	if b, _ := os.ReadFile(wslStatePath()); strings.Contains(string(b), "Ubuntu") {
		t.Fatalf("kept %s", b)
	}
}

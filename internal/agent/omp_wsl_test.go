package agent

import (
	"strings"
	"testing"
)

// A WSL omp's version is asked in the distro by its probe, so its models
// offer max from 16.4.0 as the omp on this machine's do (whqtian on
// Discord: a WSL omp still had max made xhigh, its version not known).
// One on Windows' drives isn't asked, and one that says no version is
// taken for an older omp as before.
func TestWSLOmpVersionProbed(t *testing.T) {
	if !strings.Contains(wslProbeScript(), `echo "ver:omp $(PATH="${p%/*}:$PATH" timeout 10 "$p" --version`) || !strings.Contains(wslProbeScript(), `/mnt/*) ;;`) {
		t.Fatalf("the probe doesn't ask omp its version: %s", wslProbeScript())
	}
	if strings.Contains(wslProbeScript(), `"ver:codex`) {
		t.Error("the probe asks a kind that doesn't need it")
	}
	d := parseProbe("U", "home:/home/me\nbin:omp /home/me/.bun/bin/omp\nver:omp omp/16.5.1\n")
	if d == nil || d.Versions["omp"] != "16.5.1" {
		t.Fatalf("version not read: %+v", d)
	}
	if v := d.place("omp@wsl:U").version; v != "16.5.1" || !ompTakesMax(v) {
		t.Errorf("omp@wsl:U's place has version %q", v)
	}
	if v := d.place("pi@wsl:U").version; v != "" {
		t.Errorf("pi got omp's version %q", v)
	}
	if d := parseProbe("U", "home:/home/me\nver:omp sh: omp: not found\n"); d == nil || d.Versions["omp"] != "" || ompTakesMax(d.place("omp@wsl:U").version) {
		t.Errorf("no version said, yet %+v", d)
	}
}

package agent

import (
	"path/filepath"
	"strings"
	"testing"
)

// A distro with no agent magpie sets up, only the data of the Antigravity
// app connected to it from Windows (its language server runs there and
// keeps ~/.gemini/antigravity in the distro; @guyng35955 on X), is kept,
// so internal/sessions reads its conversations; one with neither isn't.
func TestWSLAntigravityHome(t *testing.T) {
	syncHome(t)
	for _, dir := range []string{".gemini/antigravity", ".gemini/antigravity-insiders"} {
		if want := `[ -d "$HOME/` + dir + `" ] && echo dir:` + dir + `; `; !strings.Contains(wslProbeScript(), want) {
			t.Fatalf("the probe doesn't look for %s:\n%s", dir, wslProbeScript())
		}
	}
	root := t.TempDir()
	t.Cleanup(FakeWSL(
		map[string]string{"Ubuntu": "home:/home/me\ndir:.gemini/antigravity\n", "Debian": "home:/home/me\n"},
		map[string]string{"Ubuntu": root, "Debian": t.TempDir()}))

	hs := wslHomes()
	if len(hs) != 1 || hs[0].Distro != "Ubuntu" || hs[0].Home != filepath.Join(root, "home", "me") || !hs[0].Running {
		t.Fatalf("homes %+v, want Ubuntu's alone", hs)
	}
	if len(wslAgents()) != 0 {
		t.Fatalf("the Antigravity app's data made an agent row: %v", wslAgents())
	}
}

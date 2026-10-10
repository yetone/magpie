package agent

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A distro with no agent magpie sets up, only the Copilot CLI signed in
// (#723, jia2), is kept: provider reads the CLI's sign-in in its home
// while it runs, and never once it is stopped.
func TestWSLCopilotHome(t *testing.T) {
	syncHome(t)
	root := t.TempDir()
	if !strings.Contains(wslProbeScript, `[ -d "$HOME/.copilot" ] && echo dir:.copilot; `) {
		t.Fatalf("the probe doesn't look for the Copilot CLI's folder:\n%s", wslProbeScript)
	}
	t.Cleanup(FakeWSL(
		map[string]string{"Ubuntu": "home:/home/jia\ndir:.copilot\n", "Debian": "home:/home/me\n"},
		map[string]string{"Ubuntu": root, "Debian": t.TempDir()}))

	want := filepath.Join(root, "home", "jia")
	if got := provider.CopilotWSLHomes(); !slices.Equal(got, []string{want}) {
		t.Fatalf("homes %q, want %q", got, want)
	}
	if len(wslAgents()) != 0 {
		t.Fatalf("the Copilot CLI's folder made an agent row: %v", wslAgents())
	}
	StopFakeWSL("Ubuntu")
	if got := provider.CopilotWSLHomes(); len(got) != 0 {
		t.Fatalf("a stopped distro is read: %q", got)
	}
}

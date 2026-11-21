package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// myduso on Discord: Claude's subscription kept to Claude Code, magpie
// only switching its sign-in. A request naming the Claude accounts runs
// no Claude Code through the bridge and is told why, with how to undo it;
// unticked, it is served again.
func TestClaudeKeptToClaudeCodeServesNothing(t *testing.T) {
	claudeMadeFirst(t, false)
	s := New()
	t.Cleanup(s.subscription.abortAll)
	calls := filepath.Join(t.TempDir(), "calls")
	claudeRefuses(t, calls, "tok-none") // no account refused

	if err := provider.SetSwitchOnly("claude", true); err != nil {
		t.Fatal(err)
	}
	rec := askClaudeModel(s, "hi")
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "kept to Claude Code's own sign-in") {
		t.Fatalf("kept to Claude Code: %d %s", rec.Code, rec.Body.String())
	}
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Fatalf("Claude Code run through the bridge: %q", b)
	}

	if err := provider.SetSwitchOnly("claude", false); err != nil {
		t.Fatal(err)
	}
	if rec := askClaudeModel(s, "again"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "healthy account") {
		t.Fatalf("unticked: %d %s", rec.Code, rec.Body.String())
	}
}

// Antigravity CLI kept to agy's own sign-in (#1908) is told as agy's,
// with the tick to undo it named as its editor names it.
func TestAgyKeptToAgySaysAgy(t *testing.T) {
	p := provider.PluginForTest("agy-cli", "agy-cli")
	p.Name, p.SwitchOnly = "Antigravity CLI", true
	msg := switchedOff(p, "agy-cli/gemini-3-pro")
	if !strings.Contains(msg, "kept to agy's own sign-in") || !strings.Contains(msg, `"Only switch agy's sign-in"`) || strings.Contains(msg, "Claude") {
		t.Fatalf("switchedOff = %q", msg)
	}
	p.SwitchOnly = false
	if p.KeptToAgent() {
		t.Fatal("kept to agy with Only switch off")
	}
}

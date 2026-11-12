package cli

import (
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// `magpie claude --help` names the form that gives a tier a model of its
// own, which only the field list hinted at (KoiLx0 on X: "magpie claude
// opus xxxx" was tried as if it did not exist). A tier takes a model, a
// flag is refused, and so is a model magpie does not serve.
func TestAgentHelpNamesTheTierForm(t *testing.T) {
	groupsHome(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	a, err := agent.Find("claude")
	if err != nil {
		t.Fatal(err)
	}
	h := agentHelp(a)
	if !strings.Contains(h, "magpie claude opus <model>") || !strings.Contains(h, "opus, sonnet, haiku, fable") {
		t.Fatalf("help lacks the tier form:\n%s", h)
	}
	if err := run([]string{"claude", "a/m"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"claude", "haiku", "a/only-a"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"claude", "--haiku", "a/only-a"}); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("a flag: %v", err)
	}
	if err := run([]string{"claude", "haiku", "nope/zzz"}); err == nil || !strings.Contains(err.Error(), "not a model magpie serves") {
		t.Fatalf("an unknown model: %v", err)
	}
}

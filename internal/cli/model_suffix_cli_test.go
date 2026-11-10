package cli

import (
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// magpie model suffix off has the agents' lists name models alone, on
// names them with their providers again (#335), own all but the names the
// user gave (#92); anything else is refused.
func TestModelSuffixCmd(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil // no agent's files are rewritten here
	if err := modelCmd([]string{"suffix"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"suffix", "off"}); err != nil || !settings.Load().PlainNames {
		t.Fatal(err, settings.Load().PlainNames)
	}
	if err := modelCmd([]string{"suffix"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"suffix", "on"}); err != nil || settings.Load().PlainNames {
		t.Fatal(err, settings.Load().PlainNames)
	}
	// own: a name the user gave just as they wrote it, the others with
	// their provider's (#92)
	if err := modelCmd([]string{"suffix", "own"}); err != nil || provider.SuffixMode() != provider.SuffixOwn {
		t.Fatal(err, provider.SuffixMode())
	}
	if err := modelCmd([]string{"suffix"}); err != nil {
		t.Fatal(err)
	}
	if err := modelCmd([]string{"suffix", "on"}); err != nil || provider.SuffixMode() != provider.SuffixOn {
		t.Fatal(err, provider.SuffixMode())
	}
	if err := modelCmd([]string{"suffix", "maybe"}); err == nil {
		t.Fatal("took maybe")
	}
}

// magpie model suffix pi off gives Pi its own Provider in model names,
// over every agent's (KillTry27355 on X); every gives it back. Every
// agent's is left as it was, and a word it doesn't know is refused.
func TestModelSuffixAgentCmd(t *testing.T) {
	groupsHome(t)
	catalog.Changed = nil
	for _, args := range [][]string{{"suffix", "pi"}, {"suffix", "pi", "off"}, {"suffix", "pi"}} {
		if err := modelCmd(args); err != nil {
			t.Fatal(args, err)
		}
	}
	if provider.SuffixModeFor("pi") != provider.SuffixOff || provider.SuffixMode() != provider.SuffixOn || settings.Load().PlainNames {
		t.Fatalf("pi %q, every %q", provider.SuffixModeFor("pi"), provider.SuffixMode())
	}
	if err := modelCmd([]string{"suffix", "pi", "every"}); err != nil || provider.AgentSuffix("pi") != "" {
		t.Fatal(err, provider.AgentSuffix("pi"))
	}
	if err := modelCmd([]string{"suffix", "pi", "maybe"}); err == nil {
		t.Fatal("took maybe")
	}
}

package provider

import (
	"encoding/json"
	"os"
	"testing"
)

// Found groups keep one usage name, even when an agent wrote the old id or
// another vendor spelling into a session. Ordinary models remain distinct.
func TestUsageFoundGroupNames(t *testing.T) {
	autoRefHome(t)
	alias := UsageAlias()
	if alias == nil {
		t.Fatal("found group has no usage name mapper")
	}
	for _, ref := range []string{"group/auto-claude-opus-5-5", "auto/claude-opus-5-5", "auto/claude-opus-5.5"} {
		if got := alias(ref); got != "auto/claude-opus-5.5" {
			t.Errorf("%s counted as %q", ref, got)
		}
		if got := alias(ref + ":high[1m]"); got != "auto/claude-opus-5.5:high[1m]" {
			t.Errorf("effort/window lost: %q", got)
		}
	}
	for _, ref := range []string{"claude-opus-5-5", "a/claude-opus-5-5", "group/mine", "group/auto-gone", "auto/gone"} {
		if got := alias(ref); got != ref {
			t.Errorf("unrelated model %q counted as %q", ref, got)
		}
	}
	key := UsageNamesKey()
	if _, err := SetUsageAlias("claude-opus-5.5", "opus"); err != nil {
		t.Fatal(err)
	}
	alias = UsageAlias()
	if alias("group/auto-claude-opus-5-5") != "opus" || alias("auto/claude-opus-5.5") != "opus" {
		t.Fatal("the two ids did not follow the same user alias")
	}
	if UsageNamesKey() == key {
		t.Fatal("a user's alias did not invalidate usage names")
	}
}

func TestUsageFoundGroupNamesKeepAutoProvider(t *testing.T) {
	autoRefHome(t)
	// A provider saved before auto was reserved still owns this namespace.
	f := load()
	f.Providers = append(f.Providers, Provider{ID: "auto", Name: "Legacy auto", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"claude-opus-5.5"}})
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := UsageAlias()
	for _, ref := range []string{"auto/claude-opus-5.5", "group/auto-claude-opus-5-5"} {
		if alias != nil && alias(ref) != ref {
			t.Fatalf("legacy provider's %s was renamed", ref)
		}
	}
}

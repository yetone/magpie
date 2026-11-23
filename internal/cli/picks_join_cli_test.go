package cli

import (
	"os"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// eiyuuzaki2333 on X, the CLI's way: models picked with provider models or
// provider set models= are ticked for an agent shown only its picks that
// takes a provider's in, and for no other.
func TestProviderPicksJoinCmd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", os.Getenv("HOME"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "")
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "http://127.0.0.1:1/v1", Models: []string{"a1"}}); err != nil {
		t.Fatal(err)
	}
	s := settings.Load()
	s.PickedModels = map[string][]string{"codex": {"relay/a1"}, "claude": {"relay/a1"}}
	if err := settings.Save(s); err != nil {
		t.Fatal(err)
	}
	if err := visibleCmd([]string{"codex", "--provider-picks"}); err != nil {
		t.Fatal(err)
	}
	if !provider.PicksJoin("codex") {
		t.Fatal("--provider-picks isn't kept")
	}
	if err := providerCmd([]string{"provider", "models", "relay", "+a2"}); err != nil {
		t.Fatal(err)
	}
	if err := providerCmd([]string{"provider", "set", "relay", "models=a1,a2,a3"}); err != nil {
		t.Fatal(err)
	}
	got := settings.Load().PickedModels
	if !slices.Equal(got["codex"], []string{"relay/a1", "relay/a2", "relay/a3"}) {
		t.Fatalf("codex: %v", got["codex"])
	}
	if !slices.Equal(got["claude"], []string{"relay/a1"}) {
		t.Fatalf("an agent without the switch was changed: %v", got["claude"])
	}
	if err := visibleCmd([]string{"codex", "--no-provider-picks"}); err != nil {
		t.Fatal(err)
	}
	if err := providerCmd([]string{"provider", "models", "relay", "+a4"}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(settings.Load().PickedModels["codex"], "relay/a4") {
		t.Fatal("switched off, a pick joined")
	}
}

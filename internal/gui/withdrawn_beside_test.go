package gui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

// The withdrawn Antigravity plugin, on beside agy-cli signed in with its
// one Google account (the owner's screenshot, 11-21), is named beside it,
// so the page offers to turn it off and not to use agy-cli; without
// agy-cli's account it is the plain withdrawn line; turned off, no line.
func TestWithdrawnBesideAgyCLI(t *testing.T) {
	cfg := pluginsSandbox(t)
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	list := func(off bool) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"plugins": []map[string]any{
			{"spec": "@magpie-community/opencode-antigravity-auth@latest", "off": off},
			{"spec": "@magpie-community/opencode-agy-cli-auth@latest"},
		}})
		if err := os.WriteFile(filepath.Join(cfg, "plugins.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	auths := func(m map[string]any) {
		t.Helper()
		b, _ := json.Marshal(m)
		if err := os.MkdirAll(filepath.Dir(plugin.AuthPath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(plugin.AuthPath(), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := map[string]any{"type": "oauth", "refresh": "1//a|proj-a", "accountId": "a@x.com"}

	list(false)
	auths(map[string]any{"antigravity": old})
	got := withdrawnLines()
	if len(got) != 1 || got[0].Beside || got[0].Spec != "@magpie-community/opencode-antigravity-auth@latest" {
		t.Fatalf("without agy-cli's account: %+v, want the plain line with its spec", got)
	}

	auths(map[string]any{"antigravity": old, "agy-cli": map[string]any{"type": "success", "key": "agy:0123", "metadata": map[string]any{"email": "A@x.com"}}})
	got = withdrawnLines()
	if len(got) != 1 || !got[0].Beside {
		t.Fatalf("agy-cli with the same account: %+v, want it named beside", got)
	}

	list(true)
	if got = withdrawnLines(); len(got) != 0 {
		t.Fatalf("turned off: %+v, want no line", got)
	}
}

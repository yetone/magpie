package agent

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/edit"
)

func TestLANSharingDoesNotOwnUserGatewayKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	const secret = "fixture-user-owned"
	if _, err := access.Update("add-key", access.Change{Name: "Other client", Secret: secret}); err != nil {
		t.Fatal(err)
	}
	if err := access.ConfigureLAN(true, false); err != nil {
		t.Fatal(err)
	}
	if keyAt("http://172.23.80.1:3425") == secret {
		t.Error("an agent beyond loopback borrowed the user's gateway key")
	}
	if ourKey(secret) {
		t.Error("the user's gateway key became magpie's agent credential")
	}
	path := filepath.Join(home, ".claude", "settings.json")
	const endpoint = "https://fixture.example/v1"
	if err := edit.SetJSON(path,
		edit.KV{Path: "env.ANTHROPIC_AUTH_TOKEN", Value: secret},
		edit.KV{Path: "env.ANTHROPIC_BASE_URL", Value: endpoint}); err != nil {
		t.Fatal(err)
	}
	if err := claude(home).Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"ANTHROPIC_AUTH_TOKEN": secret, "ANTHROPIC_BASE_URL": endpoint} {
		if got, _ := edit.GetJSON(path, "env."+key); got != want {
			t.Errorf("model default removed the user's %s: got %q, want %q", key, got, want)
		}
	}
}

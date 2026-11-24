package cli

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/provider"
)

// magpie provider pool (plugins#349) sets a subscription's caps, Stop at
// the caps and Check usage near the cap from one command, and shows them;
// a bad value or an account the subscription hasn't changes nothing.
func TestPoolCmd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("DSH_HOME", "")
	jwt := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
		"id_token":      jwt(map[string]any{"email": "me@example.com"}),
		"access_token":  jwt(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
		"refresh_token": "r", "account_id": "acct-1"}})
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), auth, 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)
	// --accounts saves the pool's order and syncs the agents' files behind
	// it (agent.SyncCatalogSoon), which run settles and a test calling
	// providerCmd has to: that sync wrote stash.json into the config folder
	// as it was being removed (#5243)
	t.Cleanup(agent.SyncCatalogSettled)

	if err := providerCmd([]string{"provider", "pool", "codex"}); err != nil {
		t.Fatal(err)
	}
	if p, _ := provider.Find("codex"); p.CapStop || p.CapCareful {
		t.Fatalf("on by default: stop %v careful %v", p.CapStop, p.CapCareful)
	}
	if err := providerCmd([]string{"provider", "pool", "codex", "--cap", "85%", "--stop=on", "--careful", "on"}); err != nil {
		t.Fatal(err)
	}
	p, _ := provider.Find("codex")
	if c := provider.AccountCapOf("codex", "me@example.com"); c != 85 || !p.CapStop || !p.CapCareful {
		t.Fatalf("cap %d stop %v careful %v", c, p.CapStop, p.CapCareful)
	}
	for _, bad := range [][]string{
		{"provider", "pool", "codex", "--stop", "maybe"},
		{"provider", "pool", "codex", "--cap", "150"},
		{"provider", "pool", "codex", "--accounts", "nobody@example.com", "--careful", "off"},
		{"provider", "pool", "codex", "--careful"},
		{"provider", "pool", "codex", "--fast", "on"},
		{"provider", "pool"},
	} {
		if err := providerCmd(bad); err == nil {
			t.Fatalf("%v taken", bad)
		}
	}
	p, _ = provider.Find("codex")
	if c := provider.AccountCapOf("codex", "me@example.com"); c != 85 || !p.CapStop || !p.CapCareful {
		t.Fatalf("a refused command changed it: cap %d stop %v careful %v", c, p.CapStop, p.CapCareful)
	}
	if err := providerCmd([]string{"provider", "pool", "codex", "--accounts", "Me@Example.com", "--cap", "off", "--stop", "off"}); err != nil {
		t.Fatal(err)
	}
	p, _ = provider.Find("codex")
	if c := provider.AccountCapOf("codex", "me@example.com"); c != 0 || p.CapStop || !p.CapCareful {
		t.Fatalf("cap %d stop %v careful %v", c, p.CapStop, p.CapCareful)
	}
}

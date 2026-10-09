package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// magpie provider account-cap caps a subscription account at a share of
// each usage window (70, 70%), off lifts it; a share out of 1–99, an
// account the subscription hasn't, or a provider with keys is an error.
func TestAccountCapCmd(t *testing.T) {
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

	if err := providerCmd([]string{"provider", "account-cap", "codex", "Me@Example.com", "70%"}); err != nil {
		t.Fatal(err)
	}
	if c := provider.AccountCapOf("codex", "me@example.com"); c != 70 {
		t.Fatalf("cap %d", c)
	}
	// shown, for one account or all
	for _, args := range [][]string{{"provider", "account-cap", "codex"}, {"provider", "account-cap", "codex", "me@example.com"}} {
		if err := providerCmd(args); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []string{"0.5", "150", "lots"} {
		if err := providerCmd([]string{"provider", "account-cap", "codex", "me@example.com", bad}); err == nil {
			t.Fatalf("%s taken", bad)
		}
	}
	if c := provider.AccountCapOf("codex", "me@example.com"); c != 70 {
		t.Fatalf("a refused share changed the cap to %d", c)
	}
	if err := providerCmd([]string{"provider", "account-cap", "codex", "nobody@example.com", "50"}); err == nil {
		t.Fatal("an account it hasn't was capped")
	}
	if err := providerCmd([]string{"provider", "account-cap", "codex", "me@example.com", "off"}); err != nil {
		t.Fatal(err)
	}
	if c := provider.AccountCapOf("codex", "me@example.com"); c != 0 {
		t.Fatalf("off left %d", c)
	}
	// a window's own (willz on Discord): the five hours at 50%, the week
	// with none, then back to the account's
	if err := providerCmd([]string{"provider", "account-cap", "codex", "me@example.com", "--window", "5 Hours", "50%"}); err != nil {
		t.Fatal(err)
	}
	if err := providerCmd([]string{"provider", "account-cap", "codex", "me@example.com", "--window=Weekly", "none"}); err != nil {
		t.Fatal(err)
	}
	c := provider.AccountCapsOf("codex", "me@example.com")
	if five, _ := c.Of("5 hours"); five != 50 || c.Windows["weekly"] != 100 || c.All != 0 {
		t.Fatalf("window caps %+v", c)
	}
	if err := providerCmd([]string{"provider", "account-cap", "codex", "me@example.com", "--window", "5 hours"}); err != nil {
		t.Fatal(err) // shown
	}
	if err := providerCmd([]string{"provider", "account-cap", "codex", "me@example.com", "--window", "5 hours", "lots"}); err == nil {
		t.Fatal("a window's share of lots taken")
	}
	if err := providerCmd([]string{"provider", "account-cap", "codex", "--window", "5 hours"}); err == nil {
		t.Fatal("a window with no account taken")
	}
	for _, w := range []string{"5 hours", "weekly"} {
		if err := providerCmd([]string{"provider", "account-cap", "codex", "me@example.com", "--window", w, "default"}); err != nil {
			t.Fatal(err)
		}
	}
	if c := provider.AccountCapsOf("codex", "me@example.com"); c.Windows != nil {
		t.Fatalf("default left %+v", c)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k1", Chat: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := providerCmd([]string{"provider", "account-cap", "relay", "k1", "50"}); err == nil {
		t.Fatal("a key provider was capped")
	}
}

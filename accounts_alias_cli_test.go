package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #1515: `magpie accounts alias` names an account for the user, as
// magpie keeps it (providers.json): the agent's own sign-in is left as it
// is, and `magpie accounts` and `magpie quota` show the name first.
func TestAccountsAlias(t *testing.T) {
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
	authPath := filepath.Join(home, ".codex", "auth.json")
	os.MkdirAll(filepath.Dir(authPath), 0o755)
	if err := os.WriteFile(authPath, auth, 0o600); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	t.Cleanup(provider.ForgetAccounts)

	if err := accountsCmd([]string{"accounts", "alias", "codex", "Me@Example.com", "工作", "号"}); err != nil {
		t.Fatal(err)
	}
	if got := provider.AccountNameOf("codex", "me@example.com"); got != "工作 号" {
		t.Fatalf("name %q", got)
	}
	if b, _ := os.ReadFile(authPath); !bytes.Equal(b, auth) {
		t.Fatal("naming the account wrote Codex's auth.json")
	}
	// said back with no name given
	if err := accountsCmd([]string{"accounts", "alias", "codex", "me@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := accountsCmd([]string{"accounts", "alias", "codex", "nobody@example.com", "x"}); err == nil {
		t.Fatal("an account it hasn't was named")
	}
	r := accountRow{User: "me@example.com", Alias: provider.AccountNameOf("codex", "me@example.com")}
	if r.Alias != "工作 号" {
		t.Fatalf("row %+v", r)
	}
	if got := quotaTitle(provider.Quota{Provider: "codex", Plan: "Plus", User: "me@example.com", Alias: "工作 号"}); got != "codex · Plus · 工作 号 (me@example.com)" {
		t.Fatalf("quota title %q", got)
	}
	if err := accountsCmd([]string{"accounts", "alias", "codex", "me@example.com", "--clear"}); err != nil {
		t.Fatal(err)
	}
	if got := provider.AccountNameOf("codex", "me@example.com"); got != "" {
		t.Fatalf("cleared, still %q", got)
	}
}

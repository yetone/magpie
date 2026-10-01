package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// magpie accounts lists, switches and forgets a plugin's accounts by its
// provider's id, as a built-in subscription's.
func TestAccountsOfAPlugin(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("no bun on PATH")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	t.Setenv("MAGPIE_BUN", bun)
	t.Cleanup(plugin.Settle)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	abs, _ := filepath.Abs("internal/plugin/testdata/fake/index.js")
	if _, err := plugin.Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	if _, err := plugin.Providers(ctx); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"sk-one-key-1111", "sk-two-key-2222"} {
		if _, err := provider.PluginAPIKey(ctx, "fakeco", 0, nil, key); err != nil {
			t.Fatal(err)
		}
	}
	list := func() []accountRow {
		t.Helper()
		out, err := said(t, func() error { return accountsCmd([]string{"accounts", "fakeco", "--json"}) })
		if err != nil {
			t.Fatal(err)
		}
		var rows []accountRow
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return rows
	}
	rows := list()
	if len(rows) != 2 || !rows[0].Active || rows[1].Active || rows[0].User != "API key …1111" || rows[1].User != "API key …2222" {
		t.Fatalf("magpie accounts fakeco = %+v", rows)
	}
	second := rows[1].User
	if _, err := said(t, func() error { return accountsCmd([]string{"accounts", "switch", "fakeco", second}) }); err != nil {
		t.Fatal(err)
	}
	if rows := list(); len(rows) != 2 || rows[0].User != second || !rows[0].Active {
		t.Fatalf("after switching to %s: %+v", second, rows)
	}
	// the one in use isn't forgotten, as a built-in's isn't; the other is
	if _, err := said(t, func() error { return accountsCmd([]string{"accounts", "forget", "fakeco", second}) }); err == nil {
		t.Fatal("the account in use was forgotten")
	}
	first := rows[0].User
	if _, err := said(t, func() error { return accountsCmd([]string{"accounts", "forget", "fakeco", first}) }); err != nil {
		t.Fatal(err)
	}
	if rows := list(); len(rows) != 1 || rows[0].User != second || !rows[0].Active {
		t.Fatalf("after forgetting %s: %+v", first, rows)
	}
	if _, err := said(t, func() error { return accountsCmd([]string{"accounts", "switch", "nosuch", "a"}) }); err == nil || !strings.Contains(err.Error(), "can be added and switched") {
		t.Fatalf("an unknown one: %v", err)
	}
}

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
)

// magpie provider zed, once Zed is moved onto its plugin, reads as the
// built-in did: the account from Zed's own sign-in, and no plugin:// URL
// the user can't reach.
func TestShowMovedProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("MAGPIE_BUN", "")
	dir := filepath.Join(home, ".config", "magpie")
	for name, body := range map[string]string{
		"plugins.json":     `{"plugins":[{"spec":"opencode-zed-auth"}]}`,
		"plugin-auth.json": `{"zed":{"type":"oauth","access":"a","refresh":"r","expires":0,"accountId":"me@zed"}}`,
		"migrations.json":  `{"zed":{"state":"plugin","package":"opencode-zed-auth","at":"2026-10-01T00:00:00Z"}}`,
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plugin.UseCached([]plugin.Provider{{ID: "zed", Spec: "opencode-zed-auth", Name: "Zed",
		Models: []plugin.Model{{ID: "claude-sonnet-4-5", NPM: "@ai-sdk/anthropic"}, {ID: "gpt-5", NPM: "@ai-sdk/openai"}}}})
	t.Cleanup(func() { plugin.UseCached(nil) })
	p, err := provider.Find("zed")
	if err != nil {
		t.Fatal(err)
	}
	out, err := said(t, func() error { return showProvider(*p) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "plugin://") || strings.Contains(out, "plugin's") || !strings.Contains(out, "from zed's own sign-in") {
		t.Fatalf("magpie provider zed:\n%s", out)
	}
}

package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

func TestClaudePassthroughSettingControlsAuthToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0o755)

	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: "https://api.fake.com/v1", Key: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}

	cl := claude(home)
	settingsPath := filepath.Join(claudeDir, "settings.json")

	// 1. By default, passthrough is off: ANTHROPIC_AUTH_TOKEN is written.
	if err := cl.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}

	gwURL, _ := edit.GetJSON(settingsPath, "env.ANTHROPIC_BASE_URL")
	authToken, _ := edit.GetJSON(settingsPath, "env.ANTHROPIC_AUTH_TOKEN")
	if gwURL != gateway.URL() {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want %q", gwURL, gateway.URL())
	}
	if authToken != gateway.Token {
		t.Errorf("default ANTHROPIC_AUTH_TOKEN = %q, want %q", authToken, gateway.Token)
	}
	if checkMsg := cl.Check(); checkMsg != "" {
		t.Errorf("Check() reported unexpected drift: %s", checkMsg)
	}

	// 2. Toggle passthrough on via field: ANTHROPIC_AUTH_TOKEN is omitted/removed.
	passthroughField := cl.Field("passthrough")
	if passthroughField.Key != "passthrough" {
		t.Fatalf("expected passthrough field on host Claude Code")
	}
	if err := passthroughField.Set("on"); err != nil {
		t.Fatal(err)
	}
	if !settings.Load().ClaudePassthrough {
		t.Errorf("settings.Load().ClaudePassthrough = false, want true")
	}

	authToken, _ = edit.GetJSON(settingsPath, "env.ANTHROPIC_AUTH_TOKEN")
	if authToken != "" {
		t.Errorf("ANTHROPIC_AUTH_TOKEN with passthrough on = %q, want empty", authToken)
	}
	if checkMsg := cl.Check(); checkMsg != "" {
		t.Errorf("Check() with passthrough on reported unexpected drift: %s", checkMsg)
	}

	// 3. Toggle passthrough off: ANTHROPIC_AUTH_TOKEN is restored.
	if err := passthroughField.Set("off"); err != nil {
		t.Fatal(err)
	}
	if settings.Load().ClaudePassthrough {
		t.Errorf("settings.Load().ClaudePassthrough = true, want false")
	}
	authToken, _ = edit.GetJSON(settingsPath, "env.ANTHROPIC_AUTH_TOKEN")
	if authToken != gateway.Token {
		t.Errorf("ANTHROPIC_AUTH_TOKEN with passthrough off = %q, want %q", authToken, gateway.Token)
	}
	if checkMsg := cl.Check(); checkMsg != "" {
		t.Errorf("Check() with passthrough off reported unexpected drift: %s", checkMsg)
	}
}

func TestClaudePassthroughWSLIgnoresHostPassthrough(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	// Turn passthrough on globally
	if err := settings.Save(settings.Settings{ClaudePassthrough: true}); err != nil {
		t.Fatal(err)
	}
	defer settings.Save(settings.Settings{})

	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: "https://api.fake.com/v1", Key: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}

	// A WSL place has non-empty id
	wslHome := t.TempDir()
	wslDir := filepath.Join(wslHome, ".claude")
	os.MkdirAll(wslDir, 0o755)

	wslPlace := place{id: "Debian", home: wslHome}
	clWSL := claudeIn(wslPlace)

	// WSL Claude Code does not have the passthrough field
	if f := clWSL.Field("passthrough"); f != nil {
		t.Errorf("WSL Claude Code should not expose passthrough field")
	}

	if err := clWSL.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(wslDir, "settings.json")
	authToken, _ := edit.GetJSON(settingsPath, "env.ANTHROPIC_AUTH_TOKEN")

	// WSL distro must always retain ANTHROPIC_AUTH_TOKEN
	if authToken != gateway.Token {
		t.Errorf("WSL Claude Code ANTHROPIC_AUTH_TOKEN = %q, want %q", authToken, gateway.Token)
	}
	if checkMsg := clWSL.Check(); checkMsg != "" {
		t.Errorf("Check() reported unexpected drift: %s", checkMsg)
	}
}

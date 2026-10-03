package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

func TestClaudePassthroughAuthTokenOmittedWhenSignedIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: "https://api.fake.com/v1", Key: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}

	creds := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  "sk-ant-oat01-test",
			"refreshToken": "sk-ant-ort01-test",
			"expiresAt":    time.Now().Add(time.Hour).Unix(),
		},
	}
	credsBytes, _ := json.Marshal(creds)
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), credsBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	cl := claude(home)

	if err := cl.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	gwURL, _ := edit.GetJSON(settingsPath, "env.ANTHROPIC_BASE_URL")
	authToken, _ := edit.GetJSON(settingsPath, "env.ANTHROPIC_AUTH_TOKEN")
	model, _ := edit.GetJSON(settingsPath, "env.ANTHROPIC_MODEL")

	if gwURL != gateway.URL() {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want %q", gwURL, gateway.URL())
	}
	if authToken != "" {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %q, want empty (omitted for OAuth passthrough)", authToken)
	}
	if model != "fake/m1" {
		t.Errorf("ANTHROPIC_MODEL = %q, want fake/m1", model)
	}

	if checkMsg := cl.Check(); checkMsg != "" {
		t.Errorf("Check() reported unexpected drift: %s", checkMsg)
	}
}

func TestClaudePassthroughAuthTokenSetWhenSignedOut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Chat: "https://api.fake.com/v1", Key: "k", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}

	cl := claude(home)

	if err := cl.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(claudeDir, "settings.json")
	gwURL, _ := edit.GetJSON(settingsPath, "env.ANTHROPIC_BASE_URL")
	authToken, _ := edit.GetJSON(settingsPath, "env.ANTHROPIC_AUTH_TOKEN")

	if gwURL != gateway.URL() {
		t.Errorf("ANTHROPIC_BASE_URL = %q, want %q", gwURL, gateway.URL())
	}
	if authToken != gateway.Token {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %q, want %q", authToken, gateway.Token)
	}

	if checkMsg := cl.Check(); checkMsg != "" {
		t.Errorf("Check() reported unexpected drift: %s", checkMsg)
	}
}

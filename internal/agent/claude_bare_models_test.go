package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// Claude Desktop's Code tab on the user's own subscription sends the model
// variables of settings.json to Anthropic as they stand (iryan_sun on X:
// ANTHROPIC_SMALL_FAST_MODEL and the haiku tier were still
// "claude/claude-haiku-4-5[1m]", and WebFetch failed with "There's an issue
// with the selected model"). A Claude model of Claude Code's own
// subscription goes into every model variable as Anthropic's bare id; a
// model of another vendor, or a relay's, has no such id and stays.
func TestClaudeModelVariablesAreAnthropicsOwnIDs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exp := time.Now().Add(time.Hour).UnixMilli()
	write(filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-x","refreshToken":"sk-ant-ort01-x","expiresAt":`+strconv.FormatInt(exp, 10)+`,"subscriptionType":"max","scopes":["user:inference","user:profile"]}}`)
	write(filepath.Join(home, ".claude.json"), `{"oauthAccount":{"emailAddress":"own@example.com"},"additionalModelOptionsCache":[{"value":"claude-sonnet-4-6","label":"S"},{"value":"claude-haiku-4-5","label":"H"},{"value":"claude-opus-4-7","label":"O"}]}`)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	provider.ForgetAccounts()
	if p, err := provider.Find("claude"); err != nil || p.Account == nil {
		t.Fatalf("no Claude Code account in the sandbox: %v", err)
	}
	path := filepath.Join(home, ".claude", "settings.json")
	write(path, `{}`)
	a := claude(home)
	main := "claude/claude-sonnet-4-6"
	if err := a.Field("model").Set(main); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("haiku").Set("claude/claude-haiku-4-5[1m]"); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("opus").Set("deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	get := func(k string) string { v, _ := edit.GetJSON(path, "env."+k); return v }
	for k, want := range map[string]string{
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-4-6",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "claude-haiku-4-5",
		"ANTHROPIC_SMALL_FAST_MODEL":     "claude-haiku-4-5",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "deepseek/pro", // no id Anthropic knows
	} {
		if got := strings.TrimSuffix(get(k), "[1m]"); got != want {
			t.Errorf("%s is %q, want %q", k, got, want)
		}
	}
	if got := get("ANTHROPIC_DEFAULT_HAIKU_MODEL"); strings.HasSuffix(got, "[1m]") {
		t.Errorf("haiku has no 1M window to mark: %q", got)
	}
	// magpie reads them back as the subscription's models
	if got := a.Field("haiku").Get(); got != "claude/claude-haiku-4-5" {
		t.Errorf("haiku shown as %q", got)
	}
	if got := claudeMain(path); got != main {
		t.Errorf("main read as %q", got)
	}
	// a settings.json an older magpie wrote is rewritten by the next sync
	for k, v := range map[string]string{
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "claude/claude-haiku-4-5[1m]",
		"ANTHROPIC_SMALL_FAST_MODEL":    "claude/claude-haiku-4-5[1m]",
		"CLAUDE_CODE_SUBAGENT_MODEL":    "claude/claude-sonnet-4-6",
	} {
		if err := edit.SetJSON(path, edit.KV{Path: "env." + k, Value: v}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"} {
		if v := get(k); strings.Contains(v, "/") || strings.HasSuffix(v, "[1m]") {
			t.Errorf("after Sync %s is still %q", k, v)
		}
	}
	// the user's own bare model on a tier magpie doesn't hold stays theirs
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if got := get("ANTHROPIC_SMALL_FAST_MODEL"); got != "claude-haiku-4-5" {
		t.Errorf("a second Sync moved it to %q", got)
	}
}

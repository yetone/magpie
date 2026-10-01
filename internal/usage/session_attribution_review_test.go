package usage

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/sessions"
)

func sessionHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for k, v := range map[string]string{"HOME": home, "USERPROFILE": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_CACHE_HOME": filepath.Join(home, ".cache"), "CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"), "CODEX_HOME": filepath.Join(home, ".codex"), "OPENAI_BASE_URL": ""} {
		t.Setenv(k, v)
	}
	return home
}

func sessionAuth(t *testing.T, dir, account, user, email string) {
	t.Helper()
	claims, _ := json.Marshal(map[string]any{"email": email, "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": account, "chatgpt_user_id": user}})
	jwt := "h." + base64.RawURLEncoding.EncodeToString(claims) + ".sig"
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"account_id": account, "id_token": jwt, "access_token": jwt, "refresh_token": "fake"}})
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), auth, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNamedProviderHistoryDoesNotFollowCurrentConfig(t *testing.T) {
	sessionHome(t)
	sessionAuth(t, sessions.CodexDir(), "a", "u", "me@example.com")
	c := sessions.Call{Agent: "codex", Upstream: "custom", AccountID: "a", UserID: "u"}
	for _, cfg := range []string{"[model_providers.custom]\nbase_url = 'https://relay.example/v1'", "[model_providers.custom]\nrequires_openai_auth = true", "[model_providers.custom]\nbase_url = 'https://api.openai.com/v1'", ""} {
		if err := os.WriteFile(filepath.Join(sessions.CodexDir(), "config.toml"), []byte(cfg), 0600); err != nil {
			t.Fatal(err)
		}
		if got := newSessionResolver([]sessions.Call{c}).resolve(c); got != ("me@example.com") {
			t.Fatalf("current config changed historical attribution: %+v", got)
		}
	}
}

func TestSeparateCodexHomesAndDisabledProvider(t *testing.T) {
	home := sessionHome(t)
	sessionAuth(t, filepath.Join(home, ".codex"), "gateway-account", "gateway-user", "gateway@example.com")
	cliDir := filepath.Join(home, "other-codex")
	sessionAuth(t, cliDir, "cli-account", "cli-user", "cli@example.com")
	t.Setenv("CODEX_HOME", cliDir)
	c := sessions.Call{Time: time.Now(), Agent: "codex", Upstream: "openai", AccountID: "cli-account", UserID: "cli-user"}
	r := newSessionResolver([]sessions.Call{c})
	if got := r.resolve(c); got != ("cli@example.com") {
		t.Fatalf("foreign CODEX_HOME merged into gateway account: %+v", got)
	}
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	c.AccountID, c.UserID = "gateway-account", "gateway-user"
	if got := newSessionResolver([]sessions.Call{c}).resolve(c); got != "gateway@example.com" {
		t.Fatalf("same account not recognized: %+v", got)
	}
	if err := provider.SetOff("codex", true); err != nil {
		t.Fatal(err)
	}
	if got := newSessionResolver([]sessions.Call{c}).resolve(c); got != "gateway@example.com" {
		t.Fatalf("disabled provider still used: %+v", got)
	}
}

func TestCodexConfigurationDoesNotChangeRecordedAccount(t *testing.T) {
	sessionHome(t)
	sessionAuth(t, sessions.CodexDir(), "a", "u", "me@example.com")
	c := sessions.Call{Agent: "codex", Upstream: "openai", Model: "relay/glm-5", AccountID: "a", UserID: "u"}
	for _, tc := range []struct{ name, config, env string }{
		{"top level", "openai_base_url = 'http://127.0.0.1:3425/codex'", ""},
		{"environment", "", "https://relay.example/v1"},
		{"provider table", "[model_providers.openai]\nbase_url = 'https://relay.example/v1'", ""},
		{"malformed", "[invalid", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPENAI_BASE_URL", tc.env)
			if err := os.WriteFile(filepath.Join(sessions.CodexDir(), "config.toml"), []byte(tc.config), 0600); err != nil {
				t.Fatal(err)
			}
			if got := newSessionResolver([]sessions.Call{c}).resolve(c); got != "me@example.com" {
				t.Fatalf("gateway route became subscription: %+v", got)
			}
		})
	}
}

func TestSharedWorkspaceDoesNotIdentifyItsMember(t *testing.T) {
	r := &sessionResolver{identities: []provider.SessionIdentity{{Agent: "codex", AccountID: "team-ws", UserID: "u-me", User: "me@example.com"}}}
	for _, user := range []string{"", "colleague"} {
		c := sessions.Call{Agent: "codex", Upstream: "openai", AccountID: "team-ws", UserID: user}
		if got := r.resolve(c); got != ("") {
			t.Fatalf("workspace inferred its member: %+v", got)
		}
	}
}

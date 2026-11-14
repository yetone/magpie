package agent

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// #1907 (foobra): two ChatGPT accounts, both on in magpie, for Codex
// Desktop and the CLI alone, not for the gateway. Codex isn't connected, so
// it goes straight to OpenAI whether switch-only is on or off, and magpie
// only switches its sign-in; a base URL the user wrote by hand is theirs,
// and stays either way (#856).
func TestCodexSwitchOnlyKeepsCodexOffTheGateway(t *testing.T) {
	claims := func(m map[string]any) string {
		b, _ := json.Marshal(m)
		return "h." + base64.RawURLEncoding.EncodeToString(b) + ".s"
	}
	auth := func(email, acct string) map[string]any {
		return map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{
			"id_token":      claims(map[string]any{"email": email}),
			"access_token":  claims(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}),
			"refresh_token": "r-" + acct, "account_id": acct}}
	}
	me, _ := json.Marshal(auth("me@example.com", "acct-1"))
	home, read := codexHome(t, string(me), "model = \"gpt-5.5\"\n")
	b, _ := json.Marshal([]map[string]any{{"agent": "codex", "user": "spare@example.com", "on": true,
		"seen": time.Now(), "auth": auth("spare@example.com", "acct-2")}})
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), b, 0o600)

	cx := codex(home)
	for _, only := range []bool{false, true, false} {
		if err := provider.SetSwitchOnly("codex", only); err != nil {
			t.Fatal(err)
		}
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		if cfg := read(); strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "gpt-5.5"`) {
			t.Fatalf("switch-only %v:\n%s", only, cfg)
		}
		if cx.FailingOver() || cx.FailoverSaid() != "" || CodexOwnOnly() {
			t.Fatalf("switch-only %v: failing over %v, said %q, own only %v", only, cx.FailingOver(), cx.FailoverSaid(), CodexOwnOnly())
		}
	}

	// the user's own base URL: theirs, kept, switch-only on or off
	base := codexGatewayURL()
	cfg := filepath.Join(home, ".codex", "config.toml")
	os.WriteFile(cfg, []byte("model = \"gpt-5.5\"\nopenai_base_url = \""+base+"\"\n"), 0o644)
	for _, only := range []bool{true, false} {
		if err := provider.SetSwitchOnly("codex", only); err != nil {
			t.Fatal(err)
		}
		if err := cx.Sync(); err != nil {
			t.Fatal(err)
		}
		if got := read(); !strings.Contains(got, `openai_base_url = "`+base+`"`) {
			t.Fatalf("switch-only %v: the user's base URL taken away:\n%s", only, got)
		}
	}
}

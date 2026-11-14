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

// A Codex signed in to ChatGPT that reaches the gateway from another
// machine (MAGPIE_ADDR pinned beyond loopback; a WSL distro under NAT is
// the same to it) is never pointed at the gateway's Codex path for account
// failover either: from there the gateway takes a named key alone, so the
// turn failover handed magpie came back a 401 — the shape #2145 took out
// of the joined case. No Codex not connected is pointed there now
// (TestCodexNotConnectedStaysOffMagpie); one written before goes.
func TestCodexFailoverBeyondLoopbackNotWritten(t *testing.T) {
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
	t.Setenv("MAGPIE_ADDR", "172.23.80.1:3425")
	cx := codex(home)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "openai_base_url") || strings.Contains(cfg, "backend-api/codex") {
		t.Fatalf("failover written beyond loopback:\n%s", cfg)
	}
	if s := stashLoad(); s["codex.failover"] == "1" {
		t.Fatalf("failover marked beyond loopback: %v", s)
	}

	// one a magpie before this wrote there is taken off again by Sync
	home, read = codexHome(t, string(me), "model = \"gpt-5.5\"\nopenai_base_url = \"http://172.23.80.1:3425/backend-api/codex\"\n")
	stash(map[string]string{"codex.failover": "1"})
	if err := codex(home).Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "openai_base_url") {
		t.Fatalf("failover written before stays beyond loopback:\n%s", cfg)
	}
}

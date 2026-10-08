package gui

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agentenv"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// Codex's own multi_agent_v2 on in the config magpie routes is what the
// Settings row's notice is for (#141): with Codex connected and CodexAgentsV1
// on, the answer says so and leaves the key alone; off has nothing of the
// setting's to say. magpie writes nothing here — the user turns the key off.
func TestCodexAgentsV1SettingNotice(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	t.Setenv("CODEX_HOME", filepath.Join(h, ".codex"))
	os.MkdirAll(filepath.Join(h, ".codex"), 0o755)
	for _, v := range agentenv.Vars {
		t.Setenv(v, "")
	}
	cfg := filepath.Join(h, ".codex", "config.toml")
	if err := os.WriteFile(cfg, []byte("model = \"gpt-5.5\"\n\n[features]\nmulti_agent_v2 = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	// process-wide and must go back even when the test fails
	was := settings.Load().CodexAgentsV1
	t.Cleanup(func() {
		s := settings.Load()
		s.CodexAgentsV1 = was
		if err := settings.Save(s); err != nil {
			t.Errorf("restoring settings.CodexAgentsV1: %v", err)
		}
	})
	srv := Handler(nil, nil)
	call := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// connect Codex so the config magpie routes is the one with the key
	if code, out := call("POST", "/api/agents/connect/codex", "{}"); code != 200 {
		t.Fatalf("connect: %d %v", code, out)
	}
	if b, _ := os.ReadFile(cfg); !strings.Contains(string(b), "multi_agent_v2 = true") {
		t.Fatalf("the connect lost the key:\n%s", b)
	}

	code, out := call("POST", "/api/settings/codex-agents-v1", `{"on":true}`)
	if code != 200 || out["codexAgentsV1"] != true || !settings.Load().CodexAgentsV1 {
		t.Fatalf("on: %d %v", code, out["codexAgentsV1"])
	}
	if n, _ := out["notice"].(string); !strings.Contains(n, "multi_agent_v2") {
		t.Fatalf("no notice that Codex's own V2 wins: %q", n)
	}
	if b, _ := os.ReadFile(cfg); !strings.Contains(string(b), "multi_agent_v2 = true") || strings.Contains(string(b), "multi_agent_v2 = false") {
		t.Fatalf("magpie wrote the key:\n%s", b)
	}

	code, out = call("POST", "/api/settings/codex-agents-v1", `{"on":false}`)
	if code != 200 || settings.Load().CodexAgentsV1 {
		t.Fatalf("off: %d %v", code, settings.Load().CodexAgentsV1)
	}
	if n, _ := out["notice"].(string); strings.Contains(n, "multi_agent_v2") {
		t.Fatalf("off notice about the setting: %q", n)
	}
}

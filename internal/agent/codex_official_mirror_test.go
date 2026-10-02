package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// CC Switch's unified-session mirror of Codex's built-in OpenAI provider.
// Four fields, no base URL. A relay on the same id is not this.
const codexOfficialMirror = "model = \"gpt-5.5\"\nmodel_provider = \"custom\"\n\n" +
	"[model_providers.custom]\nname = \"OpenAI\"\nrequires_openai_auth = true\nsupports_websockets = true\nwire_api = \"responses\"\n"

func TestCodexCCSwitchOfficialMirrorIsBuiltIn(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x"}}`, codexOfficialMirror)
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"models":[
		{"slug":"gpt-5.5","display_name":"GPT-5.5","priority":1}]}`), 0o644)
	cx := codex(home)
	groups := map[string]string{}
	for _, o := range cx.Fields[0].Options(nil) {
		if o.Value == "gpt-5.5" {
			groups[o.Group] = o.Value
		}
	}
	if groups["OpenAI"] != "gpt-5.5" || groups["custom"] != "" {
		t.Fatalf("mirror grouped as %v, want OpenAI", groups)
	}

	logins := func(on bool) {
		t.Helper()
		b := []byte(`[{"agent":"codex","user":"spare@example.com","on":` + boolJSON(on) + `,"seen":"` + time.Now().UTC().Format(time.RFC3339) + `","auth":{"tokens":{"access_token":"y"}}}]`)
		os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
		if err := os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logins(false)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider") || strings.Contains(cfg, "model_providers") || strings.Contains(cfg, "openai_base_url") {
		t.Fatalf("one account, mirror should be gone and traffic stay on OpenAI:\n%s", cfg)
	}

	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte(codexOfficialMirror), 0o644); err != nil {
		t.Fatal(err)
	}
	logins(true)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if strings.Contains(cfg, "model_provider") || strings.Contains(cfg, "model_providers.custom") ||
		!strings.Contains(cfg, `openai_base_url = "http://127.0.0.1:`) || !strings.Contains(cfg, `model = "gpt-5.5"`) {
		t.Fatalf("second account on, mirror should route through magpie:\n%s", cfg)
	}
}

func TestCodexCCSwitchRelayIsNotTheOfficialMirror(t *testing.T) {
	const relay = "model = \"gpt-5.5\"\nmodel_provider = \"custom\"\n\n" +
		"[model_providers.custom]\nname = \"custom\"\nbase_url = \"https://relay.example/v1\"\nwire_api = \"responses\"\nrequires_openai_auth = true\n"
	home, read := codexHome(t, `{"tokens":{"access_token":"x"}}`, relay)
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"models":[
		{"slug":"gpt-5.5","display_name":"GPT-5.5","priority":1}]}`), 0o644)
	cx := codex(home)
	var group string
	for _, o := range cx.Fields[0].Options(nil) {
		if o.Value == "gpt-5.5" {
			group = o.Group
		}
	}
	if group != "custom" {
		t.Fatalf("relay grouped as %q", group)
	}
	os.MkdirAll(filepath.Dir(provider.Path()), 0o755)
	os.WriteFile(filepath.Join(filepath.Dir(provider.Path()), "logins.json"),
		[]byte(`[{"agent":"codex","user":"spare@example.com","on":true,"auth":{"tokens":{"access_token":"y"}}}]`), 0o600)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if !strings.Contains(cfg, `base_url = "https://relay.example/v1"`) || strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model_provider = "custom"`) {
		t.Fatalf("relay was rewritten:\n%s", cfg)
	}
}

func boolJSON(on bool) string {
	if on {
		return "true"
	}
	return "false"
}

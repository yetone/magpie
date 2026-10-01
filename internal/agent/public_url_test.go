package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
)

// Codex stays on the local gateway even when its advertised URL changes.
func TestCodexPublicURL(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "model = \"gpt-5.5\"\n")
	t.Setenv("MAGPIE_PUBLIC_URL", "https://magpie.example.com/magpie///")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `openai_base_url = "`+gateway.URL()+gateway.CodexPath+`"`) ||
		!strings.Contains(cfg, `base_url = "`+gateway.URL()+`/v1"`) {
		t.Fatalf("local URL not written:\n%s", cfg)
	}
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	if got := codex(home).Check(); got != "" {
		t.Fatalf("wiring changed after removing public URL: %s", got)
	}
	if err := codex(home).Fields[0].Set("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "openai_base_url") {
		t.Fatalf("local URL not removed:\n%s", cfg)
	}
}

// Gemini keeps its local wiring and restores the original URL across changes
// to the advertised URL, including a second application of magpie.
func TestGeminiPublicURL(t *testing.T) {
	home, _ := codexHome(t, "", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "https://magpie.example.com/")
	path := filepath.Join(home, ".gemini", ".env")
	if err := edit.SetEnvFile(path, edit.KV{Path: "GOOGLE_GEMINI_BASE_URL", Value: "https://gemini.example.com"}); err != nil {
		t.Fatal(err)
	}
	gm := gemini(home)
	if err := gm.Field("model").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := edit.GetEnvFile(path, "GOOGLE_GEMINI_BASE_URL"); got != gateway.URL() {
		t.Fatalf("base URL: %q", got)
	}
	for _, public := range []string{"https://another.example.com", ""} {
		t.Setenv("MAGPIE_PUBLIC_URL", public)
		if got := gemini(home).Check(); got != "" {
			t.Fatalf("wiring changed with public URL %q: %s", public, got)
		}
		if err := gemini(home).Field("model").Set("fake/m1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := gemini(home).Field("model").Set("gemini-2.5-pro"); err != nil {
		t.Fatal(err)
	}
	if got, _ := edit.GetEnvFile(path, "GOOGLE_GEMINI_BASE_URL"); got != "https://gemini.example.com" {
		t.Fatalf("restored base URL: %q", got)
	}
}

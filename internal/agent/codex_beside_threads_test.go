package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// yikelizi9438 on Discord (Windows, the Codex app): "This chat uses
// custom, but your current provider is openai. Switch to custom to
// continue", and "Model unavailable" in the composer. Before #4347
// (v0.1.2652) a magpie model picked beside the ChatGPT sign-in took the
// user's model_provider (CC Switch's "custom") down, keeping it in the
// stash for the Disconnect: Codex went on OpenAI by the base URL, and
// every thread started on custom was refused. Nothing moved them, as
// bringOver moves a provider's threads only onto magpie as Codex's
// provider. Now, beside the sign-in with the user's provider in the
// stash and gone from the top, its threads move to OpenAI, where Codex
// resumes them through magpie, and back to it at the Disconnect, which
// puts the provider back; OpenAI's own threads stay on OpenAI.
func TestCodexBesideBringsTakenProvidersThreads(t *testing.T) {
	const (
		theirs = "019a0000-0000-7000-8000-0000000000d1"
		own    = "019a0000-0000-7000-8000-0000000000d2"
	)
	home, read := codexHome(t, chatGPTAuth, "")
	dir := filepath.Join(home, ".codex")
	cfgPath := filepath.Join(dir, "config.toml")
	// config.toml as the beside wiring before #4347 left it
	cfg := "model = \"fake/m1\"\nopenai_base_url = \"" + here(home).codexURL() + "\"\n\n" +
		"[model_providers.custom]\nname = \"custom\"\nbase_url = \"https://relay.example/v1\"\nwire_api = \"responses\"\n"
	if err := edit.WriteAtomic(cfgPath, []byte(cfg)); err != nil {
		t.Fatal(err)
	}
	stash(map[string]string{"codex.provider": "custom", "codex.model": "gpt-5.5", "codex.beside": "1"})
	theirsPath := codexThreadOn(t, dir, theirs, "custom")
	codexThreadOn(t, dir, own, "openai")
	cx := codex(home)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if p, _ := edit.GetTOMLTop(cfgPath, "model_provider"); p != "" {
		t.Fatalf("model_provider %q after a sync, want Codex left on OpenAI beside its sign-in:\n%s", p, read())
	}
	for id, want := range map[string]string{theirs: "openai", own: "openai"} {
		if p := codexRowOn(t, dir, id); p != want {
			t.Errorf("synced: thread %s on %q, want %q", id, p, want)
		}
	}
	if err := cx.Unwire(); err != nil {
		t.Fatal(err)
	}
	if p, _ := edit.GetTOMLTop(cfgPath, "model_provider"); p != "custom" {
		t.Fatalf("model_provider %q after the Disconnect, want custom back:\n%s", p, read())
	}
	for id, want := range map[string]string{theirs: "custom", own: "openai"} {
		if p := codexRowOn(t, dir, id); p != want {
			t.Errorf("disconnected: thread %s on %q, want %q", id, p, want)
		}
	}
	if b, _ := os.ReadFile(theirsPath); !strings.Contains(string(b), `"model_provider":"custom"`) {
		t.Fatalf("rollout not back on custom:\n%s", b)
	}
}

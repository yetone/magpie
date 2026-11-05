package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// harris on Discord (the Codex app, after CC Switch): "This chat uses
// openai, but your current provider is custom. Switch to openai to
// continue". CC Switch left its "custom" at the top of config.toml, which
// magpie keeps since #4347, and Codex 0.166 resumes a thread only on the
// provider it is on: every thread started on OpenAI before was refused,
// and switching to OpenAI would refuse custom's instead. A sync moves
// OpenAI's threads to the provider Codex is on, and gives them back once
// Codex is on OpenAI again; custom's own threads stay on custom.
func TestCodexOwnProviderTakesOpenAIThreads(t *testing.T) {
	const (
		openai = "019a0000-0000-7000-8000-0000000000e1"
		theirs = "019a0000-0000-7000-8000-0000000000e2"
	)
	cfg := "model_provider = \"custom\"\nmodel = \"gpt-5.4\"\n\n" +
		"[model_providers.custom]\nname = \"custom\"\nbase_url = \"https://relay.example/v1\"\nwire_api = \"responses\"\nrequires_openai_auth = true\n"
	home, read := codexHome(t, chatGPTAuth, cfg)
	dir := filepath.Join(home, ".codex")
	cfgPath := filepath.Join(dir, "config.toml")
	openaiPath := codexThreadOn(t, dir, openai, "openai")
	codexThreadOn(t, dir, theirs, "custom")
	cx := codex(home)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if p, _ := edit.GetTOMLTop(cfgPath, "model_provider"); p != "custom" {
		t.Fatalf("model_provider %q after a sync, want the user's custom kept:\n%s", p, read())
	}
	for id, want := range map[string]string{openai: "custom", theirs: "custom"} {
		if p := codexRowOn(t, dir, id); p != want {
			t.Errorf("synced: thread %s on %q, want %q", id, p, want)
		}
	}
	if b, _ := os.ReadFile(openaiPath); !strings.Contains(string(b), `"model_provider":"custom"`) {
		t.Fatalf("rollout not on custom:\n%s", b)
	}
	// CC Switch's official card: Codex is on OpenAI again
	if err := edit.DelTOMLTop(cfgPath, "model_provider"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{openai: "openai", theirs: "custom"} {
		if p := codexRowOn(t, dir, id); p != want {
			t.Errorf("back on OpenAI: thread %s on %q, want %q", id, p, want)
		}
	}
	if _, ok := stashLoad()[here(home).key("codex.lent")]; ok {
		t.Error("record left after the threads went back")
	}
}

// None moves while a profile names OpenAI, which the user picks to go on
// with those threads there, nor once the user disconnected Codex.
func TestCodexOwnProviderLeavesOpenAIThreads(t *testing.T) {
	const id = "019a0000-0000-7000-8000-0000000000e3"
	table := "[model_providers.custom]\nname = \"custom\"\nbase_url = \"https://relay.example/v1\"\nwire_api = \"responses\"\n"
	for name, c := range map[string]struct {
		cfg   string
		stash map[string]string
	}{
		"profile":      {cfg: "model_provider = \"custom\"\n\n" + table + "\n[profiles.official]\nmodel_provider = \"openai\"\n"},
		"disconnected": {cfg: "model_provider = \"custom\"\n\n" + table, stash: map[string]string{"codex" + disconnectedKey: "1"}},
	} {
		home, _ := codexHome(t, chatGPTAuth, c.cfg)
		dir := filepath.Join(home, ".codex")
		codexThreadOn(t, dir, id, "openai")
		if c.stash != nil {
			stash(c.stash)
		}
		if err := codex(home).Sync(); err != nil {
			t.Fatal(err)
		}
		if p := codexRowOn(t, dir, id); p != "openai" {
			t.Errorf("%s: thread on %q, want it left on openai", name, p)
		}
	}
}

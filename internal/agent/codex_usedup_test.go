package agent

import (
	"strings"
	"testing"
)

// A ChatGPT account that runs out of its allowance after a magpie model was
// picked beside its sign-in leaves the Codex app sending nothing, a magpie
// model's turn included (#540): Sync makes magpie Codex's provider then, as
// a pick does for an account already out, and puts magpie back beside the
// sign-in once the allowance is back. A provider the user asked for (api)
// or one for a Codex not signed in to ChatGPT stays.
func TestCodexRunsOutAfterPick(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") || !strings.Contains(cfg, "openai_base_url") {
		t.Fatalf("beside the sign-in:\n%s", cfg)
	}
	codexUsedUp = func() bool { return true }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) || !strings.Contains(cfg, "model_catalog_json") ||
		!strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "fake/m1"`) {
		t.Fatalf("used up:\n%s", cfg)
	}
	if err := cx.Sync(); err != nil { // still out: stays
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("used up, synced again:\n%s", cfg)
	}
	codexUsedUp = func() bool { return false }
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "model_provider =") || strings.Contains(cfg, "model_catalog_json") ||
		!strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "fake/m1"`) {
		t.Fatalf("allowance back:\n%s", cfg)
	}

	// magpie as the provider because the user asked for it stays so
	if err := cx.Field("login").Set("api"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("api:\n%s", cfg)
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("api, allowance there, synced:\n%s", cfg)
	}
}

// Kept on ChatGPT (Always ChatGPT), a ChatGPT account that runs out leaves
// magpie beside the sign-in: Codex isn't moved to magpie as its provider,
// by a pick or by Sync, and one moved before the user chose it is put back
// beside it at once.
func TestCodexKeptOnChatGPT(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	codexUsedUp = func() bool { return true }
	t.Cleanup(func() { codexUsedUp = func() bool { return false } })
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("ChatGPT, used up: magpie its provider:\n%s", cfg)
	}

	beside := func(when string) {
		t.Helper()
		if cfg := read(); strings.Contains(cfg, "model_provider =") || strings.Contains(cfg, "model_catalog_json") ||
			!strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "fake/m1"`) {
			t.Fatalf("%s:\n%s", when, cfg)
		}
	}
	if err := cx.Field("login").Set("chatgpt"); err != nil {
		t.Fatal(err)
	}
	beside("kept on ChatGPT, used up")
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	beside("kept on ChatGPT, used up, synced")
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	beside("kept on ChatGPT, used up, picked")
	if got := cx.Field("login").Get(); got != "chatgpt" {
		t.Fatalf("login %q", got)
	}

	// back to ChatGPT, it is moved again while the account is out
	if err := cx.Field("login").Set(""); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `model_provider = "magpie"`) {
		t.Fatalf("ChatGPT again, used up:\n%s", cfg)
	}
}

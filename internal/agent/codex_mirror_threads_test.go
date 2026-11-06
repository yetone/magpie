package agent

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// Chris on Discord (the Codex app on a Mac): "Model provider mismatch.
// This chat uses OpenAI, but your current provider is OpenAI. Switch to
// OpenAI to continue." The Codex app names a provider by its table's
// name, and CC Switch's OpenAI mirror ("custom": name OpenAI, the
// ChatGPT sign-in, no base_url) is named OpenAI as the built-in is. Codex
// 0.166 and 0.167 resume a thread only on the provider id it is on now,
// and magpie left the mirror out of every move: its threads stayed on it
// while Codex was on the built-in OpenAI beside magpie, and OpenAI's
// stayed on OpenAI while Codex was on the mirror. Both are the ChatGPT
// account's threads, so they follow whichever of the two Codex is on.
const ccSwitchMirrorTable = "[model_providers.custom]\nname = \"OpenAI\"\nrequires_openai_auth = true\nsupports_websockets = true\nwire_api = \"responses\"\n"

// Codex on OpenAI beside its sign-in with a magpie model, the mirror a
// table left from CC Switch: its threads move to OpenAI; a profile's
// mirror keeps its own.
func TestCodexMirrorThreadsFollowOpenAI(t *testing.T) {
	const (
		mirror  = "019a0000-0000-7000-8000-0000000000a1"
		profile = "019a0000-0000-7000-8000-0000000000a2"
	)
	cfg := "model = \"gpt-5.4\"\n\n" + ccSwitchMirrorTable +
		"\n[model_providers.cc-switch-3]\nname = \"OpenAI\"\nrequires_openai_auth = true\nwire_api = \"responses\"\n" +
		"\n[profiles.p]\nmodel_provider = \"cc-switch-3\"\n"
	home, read := codexHome(t, chatGPTAuth, cfg)
	dir := filepath.Join(home, ".codex")
	codexThreadOn(t, dir, mirror, "custom")
	codexThreadOn(t, dir, profile, "cc-switch-3")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if p, _ := edit.GetTOMLTop(filepath.Join(dir, "config.toml"), "model_provider"); p != "" {
		t.Fatalf("model_provider %q, want Codex on OpenAI beside its sign-in:\n%s", p, read())
	}
	for id, want := range map[string]string{mirror: "openai", profile: "cc-switch-3"} {
		if p := codexRowOn(t, dir, id); p != want {
			t.Errorf("thread %s on %q, want %q", id, p, want)
		}
	}
}

// The mirror at the top when a magpie model is picked: magpie takes it
// down and Codex goes on OpenAI, so its threads go to OpenAI, and back to
// the mirror at the Disconnect, which puts it back on top.
func TestCodexMirrorAtTopJoinsWithItsThreads(t *testing.T) {
	const (
		theirs = "019a0000-0000-7000-8000-0000000000a3"
		own    = "019a0000-0000-7000-8000-0000000000a4"
	)
	home, read := codexHome(t, chatGPTAuth, "model_provider = \"custom\"\nmodel = \"gpt-5.4\"\n\n"+ccSwitchMirrorTable)
	dir := filepath.Join(home, ".codex")
	cfgPath := filepath.Join(dir, "config.toml")
	codexThreadOn(t, dir, theirs, "custom")
	codexThreadOn(t, dir, own, "openai")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if p, _ := edit.GetTOMLTop(cfgPath, "model_provider"); p != "" {
		t.Fatalf("model_provider %q, want the mirror taken down:\n%s", p, read())
	}
	for id, want := range map[string]string{theirs: "openai", own: "openai"} {
		if p := codexRowOn(t, dir, id); p != want {
			t.Errorf("beside: thread %s on %q, want %q", id, p, want)
		}
	}
	if err := cx.Unwire(); err != nil {
		t.Fatal(err)
	}
	if p, _ := edit.GetTOMLTop(cfgPath, "model_provider"); p != "custom" {
		t.Fatalf("model_provider %q after the Disconnect, want the mirror back:\n%s", p, read())
	}
	if p := codexRowOn(t, dir, theirs); p != "custom" {
		t.Errorf("after the Disconnect: the mirror's thread on %q, want custom", p)
	}
}

// Codex on the mirror at the top (CC Switch's official card of that
// kind): OpenAI's threads move to it, and back once Codex is on OpenAI.
func TestCodexMirrorAtTopTakesOpenAIThreads(t *testing.T) {
	const id = "019a0000-0000-7000-8000-0000000000a5"
	home, read := codexHome(t, chatGPTAuth, "model_provider = \"custom\"\nmodel = \"gpt-5.4\"\n\n"+ccSwitchMirrorTable)
	dir := filepath.Join(home, ".codex")
	cfgPath := filepath.Join(dir, "config.toml")
	codexThreadOn(t, dir, id, "openai")
	cx := codex(home)
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if p, _ := edit.GetTOMLTop(cfgPath, "model_provider"); p != "custom" {
		t.Fatalf("model_provider %q, want the mirror kept:\n%s", p, read())
	}
	if p := codexRowOn(t, dir, id); p != "custom" {
		t.Fatalf("thread on %q, want custom, the provider Codex is on", p)
	}
	if err := edit.DelTOMLTop(cfgPath, "model_provider"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if p := codexRowOn(t, dir, id); p != "openai" {
		t.Fatalf("back on OpenAI: thread on %q, want openai", p)
	}
}

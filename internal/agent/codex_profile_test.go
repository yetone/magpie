package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Codex wired by its profile alone (hpnjstar on X): codex -p magpie goes
// through magpie, and config.toml, which plain codex reads, is not touched
// by connecting, switching models or disconnecting.
func TestCodexProfileOnlyLeavesConfigTomlAlone(t *testing.T) {
	const own = "model = \"gpt-5.5\"\nmodel_provider = \"acme\"\nopenai_base_url = \"https://relay.example/v1\"\n\n[model_providers.acme]\nname = \"acme\"\nbase_url = \"https://acme.example/v1\"\n"
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, own)
	dir := filepath.Join(home, ".codex")
	cx := codex(home)
	if err := cx.Field("scope").Set("profile"); err != nil {
		t.Fatal(err)
	}
	cx = codex(home)
	if cx.Path != filepath.Join(dir, "magpie.config.toml") {
		t.Fatalf("path %s", cx.Path)
	}
	if err := cx.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	prof, _ := os.ReadFile(cx.Path)
	for _, want := range []string{`model_provider = "magpie"`, `model = "fake/m1"`, `model_catalog_json = "magpie-models.json"`, "[model_providers.magpie]"} {
		if !strings.Contains(string(prof), want) {
			t.Errorf("profile lacks %q:\n%s", want, prof)
		}
	}
	if read() != own {
		t.Fatalf("config.toml changed:\n%s", read())
	}
	if !cx.Wired() {
		t.Fatalf("not connected:\n%s", prof)
	}
	if d := cx.Drift(); d != nil {
		t.Fatalf("drift %+v", d)
	}
	if err := cx.Apply("effort", "high"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Sync(); err != nil {
		t.Fatal(err)
	}
	if read() != own {
		t.Fatalf("config.toml changed after effort and sync:\n%s", read())
	}
	// a way around the scope while connected would leave both wirings
	if err := cx.Field("scope").Set(""); err == nil {
		t.Fatal("scope changed while connected")
	}
	if err := cx.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cx.Path); !os.IsNotExist(err) {
		t.Fatalf("profile left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "magpie-models.json")); !os.IsNotExist(err) {
		t.Fatalf("catalog left: %v", err)
	}
	if read() != own || codex(home).Wired() {
		t.Fatalf("after disconnect:\n%s", read())
	}
}

// A model of Codex's own is not a profile model: plain codex runs those.
func TestCodexProfileOnlyRefusesAnOwnModel(t *testing.T) {
	home, _ := codexHome(t, "", "")
	cx := codex(home)
	if err := cx.Field("scope").Set("profile"); err != nil {
		t.Fatal(err)
	}
	if err := codex(home).Field("model").Set("gpt-5.5"); err == nil {
		t.Fatal("an own model was written to the profile")
	}
}

// The default wiring still writes config.toml, and "applies to" refuses to
// move under it.
func TestCodexScopeWaitsForDisconnect(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "")
	cx := codex(home)
	if err := cx.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(), "openai_base_url") {
		t.Fatalf("default wiring changed:\n%s", read())
	}
	if err := cx.Field("scope").Set("profile"); err == nil {
		t.Fatal("scope changed while connected")
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "magpie.config.toml")); err == nil {
		t.Fatal("a profile was written by the default wiring")
	}
}

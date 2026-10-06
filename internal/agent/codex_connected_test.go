package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// Codex signed in with ChatGPT, connected on one of magpie's models beside
// its sign-in, stays connected when Codex writes one of its own models in
// (its /model, or a Codex app still running on its old pick): its own
// models are in its list beside magpie's, as when it joined (#940: Codex
// read as not connected then, its config still on magpie's gateway).
// Disconnect leaves it on that model. Made magpie's provider (the Codex
// app blocking its account), it is not beside the sign-in.
func TestCodexOwnPickAfterMagpieModelStaysConnected(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "model = \"gpt-5.5\"\n")
	cx := codex(home)
	if err := cx.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if !cx.Wired() {
		t.Fatalf("not connected on fake/m1:\n%s", read())
	}
	if err := edit.SetTOMLTop(filepath.Join(home, ".codex", "config.toml"), edit.KV{Path: "model", Value: "gpt-6.1-sol"}); err != nil {
		t.Fatal(err)
	}
	if !cx.Wired() {
		t.Fatalf("Codex's own pick disconnected it:\n%s", read())
	}
	// told as a change from outside, the row still connected
	if d := cx.Drift(); d == nil || d.Kind != "replaced" || d.Now != "gpt-6.1-sol" {
		t.Fatalf("drift: %+v", d)
	}
	if err := cx.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); cx.Wired() || strings.Contains(cfg, "openai_base_url") || !strings.Contains(cfg, `model = "gpt-6.1-sol"`) {
		t.Fatalf("disconnected:\n%s", cfg)
	}

	// out of its allowance, magpie is its provider: an own model written
	// back is a change from outside, not a pick beside magpie's
	codexUsedUp = func() bool { return true }
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".codex", "config.toml")
	if err := edit.SetTOMLTop(path, edit.KV{Path: "model", Value: "gpt-6.1-sol"}); err != nil {
		t.Fatal(err)
	}
	if err := edit.DelTOMLTop(path, "model_provider"); err != nil {
		t.Fatal(err)
	}
	if cx.Wired() {
		t.Fatalf("still beside the sign-in after magpie became its provider:\n%s", read())
	}
}

// Connected while Codex can't join (the Codex app blocking its ChatGPT
// account, or a provider of its own in config.toml), Codex stays on the
// model it was on as magpie serves it on the ChatGPT account, not the first
// of magpie's other models (#940: it went to Grok 4.7, "magpie doesn't
// serve the model it was on").
func TestCodexConnectKeepsItsOwnModelThroughMagpie(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "model = \"gpt-b\"\n")
	os.WriteFile(filepath.Join(home, ".codex", "models_cache.json"), []byte(`{"models":[
		{"slug":"gpt-a","display_name":"A","priority":1},
		{"slug":"gpt-b","display_name":"B","priority":2}]}`), 0o644)
	codexUsedUp = func() bool { return true }
	cx := codex(home)
	c, err := cx.ConnectHow()
	if err != nil {
		t.Fatal(err)
	}
	if c.How != "same" || c.Value != "codex/gpt-b" || !strings.Contains(read(), `model = "codex/gpt-b"`) {
		t.Fatalf("connected %+v:\n%s", c, read())
	}
}

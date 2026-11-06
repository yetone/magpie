package agent

import (
	"strings"
	"testing"
)

// Codex writes its memories with models of its own, gpt-6-luna for a
// thread's summary and gpt-6.1-terra to consolidate them (gpt-5.6-luna
// and gpt-5.6-terra before Codex 0.167.0), whatever its
// model (Yc on Discord). The memories square sets both [memories] keys to
// one pick, Default removes them, a magpie model needs Codex on magpie,
// and stepping out of magpie drops one of magpie's while keeping the
// user's other [memories] settings.
func TestCodexMemoriesModel(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`,
		"model = \"gpt-5.5\"\n\n[memories]\nmax_raw_memories_for_consolidation = 64\n")
	cx := codex(home)
	mem := cx.Field("memories")
	if mem == nil {
		t.Fatal("no memories field")
	}
	if !mem.Quiet {
		t.Error("the memories field is a picker of its own, not a square")
	}
	if got := mem.Get(); got != "" {
		t.Fatalf("unset memories model = %q", got)
	}
	if err := mem.Set("fake/m1"); err == nil {
		t.Error("a magpie model was taken before Codex runs through magpie")
	}
	for _, o := range mem.Options(nil) {
		if isMagpie(o.Value) {
			t.Errorf("magpie model offered before Codex runs through magpie: %s", o.Value)
		}
	}
	if err := mem.Set("gpt-5.4-mini"); err != nil {
		t.Fatal(err)
	}
	cfg := read()
	if !strings.Contains(cfg, `extract_model = "gpt-5.4-mini"`) || !strings.Contains(cfg, `consolidation_model = "gpt-5.4-mini"`) ||
		!strings.Contains(cfg, "max_raw_memories_for_consolidation = 64") {
		t.Fatalf("own model:\n%s", cfg)
	}
	if got := mem.Get(); got != "gpt-5.4-mini" {
		t.Fatalf("memories = %q", got)
	}

	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); !strings.Contains(cfg, `extract_model = "fake/m1"`) || !strings.Contains(cfg, `consolidation_model = "fake/m1"`) {
		t.Fatalf("magpie model:\n%s", cfg)
	}
	// one of Codex's own again: Codex can't find magpie's model any more
	if err := cx.Fields[0].Set("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "extract_model") || strings.Contains(cfg, "consolidation_model") ||
		!strings.Contains(cfg, "max_raw_memories_for_consolidation = 64") {
		t.Fatalf("stepping out of magpie:\n%s", cfg)
	}

	// Default: the keys go, and a table left empty with them
	if err := mem.Set("gpt-5.4-mini"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Set(""); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "extract_model") || strings.Contains(cfg, "consolidation_model") ||
		!strings.Contains(cfg, "max_raw_memories_for_consolidation = 64") {
		t.Fatalf("default:\n%s", cfg)
	}
	home, read = codexHome(t, "", "model = \"gpt-5.5\"\n")
	mem = codex(home).Field("memories")
	if err := mem.Set("gpt-5.4-mini"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Set(""); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "[memories]") {
		t.Fatalf("empty table kept:\n%s", cfg)
	}
}

// A magpie memories model goes when Codex is disconnected from magpie too.
func TestCodexMemoriesUnwire(t *testing.T) {
	home, read := codexHome(t, `{"tokens":{"access_token":"x","id_token":"x.e30.x"}}`, "model = \"gpt-5.5\"\n")
	cx := codex(home)
	if err := cx.Fields[0].Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Field("memories").Set("fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := cx.Unwire(); err != nil {
		t.Fatal(err)
	}
	if cfg := read(); strings.Contains(cfg, "fake/m1") {
		t.Fatalf("magpie model kept after disconnecting:\n%s", cfg)
	}
}

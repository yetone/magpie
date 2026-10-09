package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
)

// Sorghum on Discord: each of magpie's models in Kimi Code has the name
// magpie shows for it (display_name) and, where magpie knows it, its most
// output (max_output_size, the new Kimi Code's max_completion_tokens); a
// value the user set in the table themselves stays, while one magpie wrote
// follows magpie's catalog. kimi-cli, which has no max_output_size, gets the
// name only.
func TestKimiNamesAndOutput(t *testing.T) {
	type model struct {
		Model          string `toml:"model"`
		MaxContextSize int    `toml:"max_context_size"`
		MaxOutputSize  *int   `toml:"max_output_size"`
		DisplayName    string `toml:"display_name"`
	}
	for _, legacy := range []bool{false, true} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
		t.Setenv("KIMI_SHARE_DIR", "")
		t.Setenv("KIMI_CODE_HOME", "")
		dir := filepath.Join(home, ".kimi-code")
		if legacy {
			dir = filepath.Join(home, ".kimi")
		}
		os.MkdirAll(dir, 0o755)
		if err := provider.Save(provider.Provider{ID: "relay", Name: "Relay", Chat: "https://example.test/v1", Key: "k",
			Models: []string{"big", "wide", "bare"}}); err != nil {
			t.Fatal(err)
		}
		live := func(bigOut int) {
			t.Helper()
			if err := catalog.SaveLive("relay", "https://example.test/v1", []catalog.Model{
				{ID: "big", Name: "Big One", Context: 200000, Output: bigOut},
				// models.dev lists some outputs above the window: the window caps it
				{ID: "wide", Name: "Wide", Context: 128000, Output: 384000},
				{ID: "bare"},
			}); err != nil {
				t.Fatal(err)
			}
		}
		live(32000)
		a := kimi(home)
		if a.Path != filepath.Join(dir, "config.toml") {
			t.Fatalf("path: %s", a.Path)
		}
		read := func() map[string]model {
			t.Helper()
			b, err := os.ReadFile(a.Path)
			if err != nil {
				t.Fatal(err)
			}
			var c struct{ Models map[string]model }
			if err := toml.Unmarshal(b, &c); err != nil {
				t.Fatalf("%v\n%s", err, b)
			}
			return c.Models
		}
		labels := map[string]string{}
		for _, m := range magpieModels("kimi") {
			labels[m.ID] = m.Name
		}
		out := func(m model) int {
			if m.MaxOutputSize == nil {
				return 0
			}
			return *m.MaxOutputSize
		}
		want := func(id string, name string, output int) {
			t.Helper()
			m := read()["magpie/"+id]
			if legacy {
				output = 0
			}
			if m.DisplayName != name || out(m) != output {
				b, _ := os.ReadFile(a.Path)
				t.Fatalf("legacy=%v %s: display_name %q max_output_size %d, want %q %d\n%s", legacy, id, m.DisplayName, out(m), name, output, b)
			}
		}
		f := a.Field("model")
		if err := f.Set("magpie/relay/big"); err != nil {
			t.Fatal(err)
		}
		if labels["relay/big"] == "" || labels["relay/big"] == "relay/big" {
			t.Fatalf("no name for relay/big: %v", labels)
		}
		want("relay/big", labels["relay/big"], 32000)
		want("relay/wide", labels["relay/wide"], 128000)
		// a model magpie knows no output for has no max_output_size
		want("relay/bare", labels["relay/bare"], 0)

		// the user names big themselves and caps wide, by hand in config.toml
		edit.SetTOMLKey(a.Path, `models."magpie/relay/big"`, "display_name", "My Big")
		if !legacy {
			edit.SetTOMLKey(a.Path, `models."magpie/relay/wide"`, "max_output_size", 8000)
			edit.SetTOMLKey(a.Path, `models."magpie/relay/bare"`, "max_output_size", 4096)
		}
		// and magpie's catalog changes big's output
		live(64000)
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
		want("relay/big", "My Big", 64000)
		want("relay/wide", labels["relay/wide"], 8000)
		want("relay/bare", labels["relay/bare"], 4096)
		// and they stay through another write
		if err := f.Set("magpie/relay/wide"); err != nil {
			t.Fatal(err)
		}
		want("relay/big", "My Big", 64000)
		want("relay/wide", labels["relay/wide"], 8000)

		// a value magpie wrote that its catalog no longer has goes
		live(0)
		if err := a.Sync(); err != nil {
			t.Fatal(err)
		}
		want("relay/big", "My Big", 0)

		// out of magpie: its tables and what it wrote in them are gone
		if err := f.Set(""); err != nil {
			t.Fatal(err)
		}
		if len(read()) != 0 || stashLoad()[kimiWroteKey(a.Path)] != "" {
			b, _ := os.ReadFile(a.Path)
			t.Fatalf("reset: %v\n%s", stashLoad(), b)
		}
	}
}

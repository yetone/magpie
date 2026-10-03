package edit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTOMLArrayTableEditsOnlyNamedEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	before := `# global
default_model = "native/a"
[[providers]]
name = "native"
models = [
  "a", # keep
  "b",
]
[providers.headers]
x = "keep"
[[ 'providers' ]]
name = 'magpie'
models = ["old"]
[providers.model_overrides."old"]
context_window = 10
note = """
[[providers]]
name = "fake"
"""
# keep the next section's comment
[[providers]]
name = "last"
models = ["z"]
[ui]
theme = "dark"
`
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	block := "[[providers]]\nname = \"magpie\"\nmodels = [\"new\"]\n[providers.model_overrides.\"new\"]\ncontext_window = 100\n"
	if err := SetTOMLArrayTable(path, "providers", "name", "magpie", block); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	keep := before[:strings.Index(before, "[[ 'providers' ]]")]
	tail := before[strings.Index(before, "# keep the next"):]
	if !strings.HasPrefix(string(got), keep) || !strings.HasSuffix(string(got), tail) || strings.Contains(string(got), "context_window = 10\n") {
		t.Fatalf("neighboring data or the owned subtree was edited incorrectly:\n%s", got)
	}
	if err := DelTOMLArrayTable(path, "providers", "name", "magpie"); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if !strings.HasPrefix(string(got), keep) || !strings.HasSuffix(string(got), tail) || strings.Contains(string(got), `name = "magpie"`) {
		t.Fatalf("delete changed another entry:\n%s", got)
	}
}

func TestTOMLArrayTableRejectsAmbiguityAndInvalidInput(t *testing.T) {
	for _, input := range []string{
		"[[providers]]\nname = \"magpie\"\n[[providers]]\nname = \"magpie\"\n",
		"[[providers]]\nname = \"other\"\nname = \"duplicate\"\n",
		"[[providers]]\nname = [\n",
		"providers = [{ name = \"native\" }]\n",
	} {
		t.Run(input, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			os.WriteFile(path, []byte(input), 0o600)
			if err := SetTOMLArrayTable(path, "providers", "name", "magpie", "[[providers]]\nname = \"magpie\"\n"); err == nil {
				t.Fatal("accepted unsafe input")
			}
			got, _ := os.ReadFile(path)
			if string(got) != input {
				t.Fatalf("failed edit changed the file: %s", got)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := SetTOMLArrayTable(path, "providers", "name", "magpie", "[[providers]]\nname = \"foreign\"\n"); err == nil {
		t.Fatal("accepted a mismatching replacement")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid replacement created a file")
	}
}

func TestTOMLTopKeepsCommentAndSpacing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("default_model  =  'native/a'  # chosen by me\n[ui]\ntheme = 'dark'\n"), 0o600)
	if err := SetTOMLTopPreserving(path, KV{Path: "default_model", Value: "magpie/group/code"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "default_model  =  \"magpie/group/code\"  # chosen by me\n[ui]\ntheme = 'dark'\n" {
		t.Fatalf("lost formatting: %s", got)
	}
}

func TestTOMLArrayTableInterleavedChildren(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	input := "[[providers]]\nname = \"magpie\"\n[ui]\ntheme = \"dark\"\n[providers.model_overrides.\"old\"]\ncontext_window = 10\n[[providers]]\nname = \"native\"\n"
	os.WriteFile(path, []byte(input), 0o600)
	if err := SetTOMLArrayTable(path, "providers", "name", "magpie", "[[providers]]\nname = \"magpie\"\nmodels = [\"new\"]\n"); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "context_window = 10") || !strings.Contains(string(got), "[ui]\ntheme = \"dark\"") || !strings.Contains(string(got), "name = \"native\"") {
		t.Fatalf("interleaved subtree: %s", got)
	}
}

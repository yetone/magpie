package edit

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// A key set or removed at the top of a YAML file goes in or out with the
// lines that belong to the entry before it, never between a block key and its
// children (magpie goose effort off on a config.yaml ending in extensions:).
func TestYAMLTopAfterBlockEntry(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty", "", "GOOSE_THINKING_EFFORT: off\n"},
		{"block mapping",
			"# my goose\nGOOSE_PROVIDER: openrouter\nGOOSE_MODEL: 'owner''s/model' # pinned\nextensions:\n  dev:\n    enabled: true\n",
			"# my goose\nGOOSE_PROVIDER: openrouter\nGOOSE_MODEL: 'owner''s/model' # pinned\nextensions:\n  dev:\n    enabled: true\nGOOSE_THINKING_EFFORT: off\n"},
		{"no trailing newline",
			"GOOSE_PROVIDER: openrouter\nextensions:\n  dev:\n    enabled: true",
			"GOOSE_PROVIDER: openrouter\nextensions:\n  dev:\n    enabled: true\nGOOSE_THINKING_EFFORT: off\n"},
		{"blank lines and comments",
			"extensions:\n  dev:\n    enabled: true\n\n# between\n  other:\n    enabled: false\n\n# the end\n",
			"extensions:\n  dev:\n    enabled: true\n\n# between\n  other:\n    enabled: false\nGOOSE_THINKING_EFFORT: off\n\n# the end\n"},
		{"sequence",
			"GOOSE_MODEL: m\nlist:\n- a\n- b\nnested:\n  - c\n",
			"GOOSE_MODEL: m\nlist:\n- a\n- b\nnested:\n  - c\nGOOSE_THINKING_EFFORT: off\n"},
		{"flow mapping",
			"GOOSE_MODEL: m\nextensions: {\n  dev: {enabled: true}\n}\n\n# the end\n",
			"GOOSE_MODEL: m\nextensions: {\n  dev: {enabled: true}\n}\nGOOSE_THINKING_EFFORT: off\n\n# the end\n"},
		{"flow sequence",
			"extensions: [\n  {name: dev, enabled: true}\n]\n",
			"extensions: [\n  {name: dev, enabled: true}\n]\nGOOSE_THINKING_EFFORT: off\n"},
		{"document markers",
			"---\nextensions: {\n  dev: {enabled: true}\n}\n...\n",
			"---\nextensions: {\n  dev: {enabled: true}\n}\nGOOSE_THINKING_EFFORT: off\n...\n"},
		{"document end comment",
			"---\nextensions: {}\n...\t # end\n# footer\n",
			"---\nextensions: {}\nGOOSE_THINKING_EFFORT: off\n...\t # end\n# footer\n"},
		{"indented root",
			"  GOOSE_MODEL: m\n  extensions: {}\n",
			"  GOOSE_MODEL: m\n  extensions: {}\n  GOOSE_THINKING_EFFORT: off\n"},
		{"key inside a quoted scalar",
			"prompt: \"first\nGOOSE_THINKING_EFFORT: fake\nlast\"\n",
			"prompt: \"first\nGOOSE_THINKING_EFFORT: fake\nlast\"\nGOOSE_THINKING_EFFORT: off\n"},
		{"comment-like scalar ending",
			"prompt: \"first\n# last\"\n# the end\n",
			"prompt: \"first\n# last\"\nGOOSE_THINKING_EFFORT: off\n# the end\n"},
		{"terminator-like scalar ending",
			"prompt: \"first\n...# last\"\n# the end\n",
			"prompt: \"first\n...# last\"\nGOOSE_THINKING_EFFORT: off\n# the end\n"},
		{"block scalar",
			"GOOSE_MODEL: m\nprompt: |\n  first\n\n  # not a comment\n",
			"GOOSE_MODEL: m\nprompt: |\n  first\n\n  # not a comment\nGOOSE_THINKING_EFFORT: off\n"},
		{"kept block scalar",
			"prompt: |+\n  first\n\n\n",
			"prompt: |+\n  first\n\n\nGOOSE_THINKING_EFFORT: off\n"},
		{"nested kept block scalar",
			"extensions:\n  prompt: |+\n    first\n\n\n# the end\n",
			"extensions:\n  prompt: |+\n    first\n\n\nGOOSE_THINKING_EFFORT: off\n# the end\n"},
		{"empty document",
			"# my goose\n---\n...\n",
			"# my goose\n---\nGOOSE_THINKING_EFFORT: off\n...\n"},
		{"replace a block value",
			"GOOSE_THINKING_EFFORT: >\n  long\n  text\nGOOSE_MODEL: m\n",
			"GOOSE_THINKING_EFFORT: off\nGOOSE_MODEL: m\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(p, []byte(c.in), 0o600); err != nil {
				t.Fatal(err)
			}
			before := map[string]any{}
			if err := yaml.Unmarshal([]byte(c.in), &before); err != nil {
				t.Fatal(err)
			}
			delete(before, "GOOSE_THINKING_EFFORT")
			if err := SetYAMLTop(p, KV{"GOOSE_THINKING_EFFORT", "off"}); err != nil {
				t.Fatal(err)
			}
			got := read(t, p)
			if got != c.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, c.want)
			}
			after := map[string]any{}
			if err := yaml.Unmarshal([]byte(got), &after); err != nil {
				t.Fatalf("result does not parse: %v\n%s", err, got)
			}
			if after["GOOSE_THINKING_EFFORT"] != "off" {
				t.Fatalf("GOOSE_THINKING_EFFORT = %#v", after["GOOSE_THINKING_EFFORT"])
			}
			delete(after, "GOOSE_THINKING_EFFORT")
			if !maps.EqualFunc(before, after, reflect.DeepEqual) {
				t.Fatalf("other keys changed:\n%#v\n%#v", before, after)
			}
			if err := DelYAMLTop(p, "GOOSE_THINKING_EFFORT"); err != nil {
				t.Fatal(err)
			}
			again := map[string]any{}
			if err := yaml.Unmarshal([]byte(read(t, p)), &again); err != nil {
				t.Fatalf("after delete does not parse: %v\n%s", err, read(t, p))
			}
			if !maps.EqualFunc(before, again, reflect.DeepEqual) {
				t.Fatalf("after delete:\n%#v\n%#v", before, again)
			}
		})
	}
}

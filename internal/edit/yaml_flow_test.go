package edit

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

// A top-level list written in flow style, as dsh's own writers keep a
// profile's [] once they add to it (#445), comes back as the same list in
// block style, the lines around it as they were.
func TestBlockList(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"one line", "# Your patch layer for this dsh profile\n# a top-level YAML array\n[ { id: some-plugin, disabled: false } ]\n",
			"# Your patch layer for this dsh profile\n# a top-level YAML array\n- id: some-plugin\n  disabled: false\n"},
		{"folded, nested, tagged", "[\n  { id: tools, config: { disabled: [a, b] } },\n  { id: \"@x/y\", config: { cwd: !!js process.cwd(), n: \"1\" } }\n]\n# after\n",
			"- id: tools\n  config:\n    disabled:\n      - a\n      - b\n- id: \"@x/y\"\n  config:\n    cwd: !!js process.cwd()\n    n: \"1\"\n# after\n"},
		{"empty", "# head\n[ ]\n", "# head\n[]\n"},
		{"bare", "[]\n", "[]\n"},
		{"crlf", "# h\r\n[ {id: a} ]\r\n", "# h\n- id: a\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := BlockList(c.in)
			if !ok || got != c.want {
				t.Fatalf("got %v %q, want %q", ok, got, c.want)
			}
			var before, after any
			if err := yaml.Unmarshal([]byte(c.in), &before); err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal([]byte(got), &after); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("the list changed: %v -> %v", before, after)
			}
		})
	}
	for _, in := range []string{"", "# only a comment\n", "- id: a\n", "a: [1]\n", "[a]\n---\n[b]\n", "[ {id: a\n", "- [a, b]\n"} {
		if got, ok := BlockList(in); ok {
			t.Fatalf("%q taken for a flow list: %q", in, got)
		}
	}
}

// A table emptied by taking magpie's key out (Hermes's providers: {} and
// model: {} after a disconnect) is filled in block style when it is
// switched on again, not written on one line; a flow table that holds
// something stays as it is.
func TestSetYAMLFillsAnEmptyTableInBlocks(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("providers: {}\nmodel: {}\nkeep: {a: 1}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetYAML(p, KV{"providers.magpie", map[string]any{"name": "magpie", "models": []string{"x"}}}, KV{"model.default", "x"}, KV{"keep.b", 2}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	want := "providers:\n  magpie:\n    models:\n      - x\n    name: magpie\nmodel:\n  default: x\nkeep: {a: 1, b: 2}\n"
	if string(b) != want {
		t.Fatalf("got\n%s\nwant\n%s", b, want)
	}
}

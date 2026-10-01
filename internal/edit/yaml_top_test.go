package edit

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestYAMLTopQuotedKeys(t *testing.T) {
	for _, c := range []struct{ name, key string }{
		{"double quoted", `"GOOSE_MODEL"`},
		{"single quoted", `'GOOSE_MODEL'`},
		{"escaped", `"GOOSE_\u004dODEL"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			const rest = "# extensions\nextensions: {dev: {enabled: true}}\n"
			in := c.key + ": old\n" + rest
			p := tmpFile(t, "config.yaml", in)
			if v, ok := GetYAMLTop(p, "GOOSE_MODEL"); !ok || v != "old" {
				t.Errorf("GetYAMLTop = %q, %v", v, ok)
			}
			if err := SetYAMLTop(p, KV{"GOOSE_MODEL", "new"}); err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := yaml.Unmarshal([]byte(read(t, p)), &got); err != nil {
				t.Fatal(err)
			}
			if got["GOOSE_MODEL"] != "new" || len(got) != 2 {
				t.Fatalf("after set: %#v", got)
			}
			// Delete the original quoted key too, without first normalizing it.
			p = tmpFile(t, "original.yaml", in)
			if err := DelYAMLTop(p, "GOOSE_MODEL"); err != nil {
				t.Fatal(err)
			}
			if got := read(t, p); got != rest {
				t.Fatalf("after delete = %q, want %q", got, rest)
			}
		})
	}
}

func TestYAMLTopReplacesWholeValues(t *testing.T) {
	for _, c := range []struct{ name, entry string }{
		{"flow mapping", "GOOSE_MODEL: {\n  name: old\n}\n"},
		{"flow sequence", "GOOSE_MODEL: [\n  old\n]\n"},
		{"quoted scalar", "GOOSE_MODEL: \"first\n# last\"\n"},
		{"kept scalar", "GOOSE_MODEL: |+\n  old\n\n\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			const before = "# provider\nGOOSE_PROVIDER: p\n"
			const after = "# extensions\nextensions: {dev: {enabled: true}}\n"
			in := before + c.entry + after
			p := tmpFile(t, "config.yaml", in)
			if err := SetYAMLTop(p, KV{"GOOSE_MODEL", "new"}); err != nil {
				t.Fatal(err)
			}
			if got, want := read(t, p), before+"GOOSE_MODEL: new\n"+after; got != want {
				t.Fatalf("after set = %q, want %q", got, want)
			}
			p = tmpFile(t, "original.yaml", in)
			if err := DelYAMLTop(p, "GOOSE_MODEL"); err != nil {
				t.Fatal(err)
			}
			if got, want := read(t, p), before+after; got != want {
				t.Fatalf("after delete = %q, want %q", got, want)
			}
		})
	}
}

func TestYAMLTopQuotesNewKeys(t *testing.T) {
	p := tmpFile(t, "config.yaml", "existing: kept\n")
	const key = "model: name\n# comment"
	if err := SetYAMLTop(p, KV{key, "new"}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := yaml.Unmarshal([]byte(read(t, p)), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[key] != "new" || got["existing"] != "kept" {
		t.Fatalf("after set: %#v", got)
	}
}

func TestYAMLTopRejectsUnsafeEdits(t *testing.T) {
	for _, c := range []struct{ name, in string }{
		{"malformed", "extensions: {\n"},
		{"duplicate key", "GOOSE_MODEL: old\n\"GOOSE_MODEL\": duplicate\n"},
		{"multiple documents", "GOOSE_MODEL: old\n---\nGOOSE_MODEL: other\n"},
		{"flow root", "{GOOSE_MODEL: old, extensions: {}}\n"},
		{"sequence root", "- GOOSE_MODEL: old\n"},
		{"explicit key", "? GOOSE_MODEL\n: old\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, edit := range []struct {
				name string
				fn   func(string) error
			}{
				{"set", func(p string) error { return SetYAMLTop(p, KV{"GOOSE_MODEL", "new"}) }},
				{"delete", func(p string) error { return DelYAMLTop(p, "GOOSE_MODEL") }},
			} {
				t.Run(edit.name, func(t *testing.T) {
					p := tmpFile(t, "config.yaml", c.in)
					err := edit.fn(p)
					if err == nil || !strings.Contains(err.Error(), p) {
						t.Fatalf("error = %v, want an error with the file path", err)
					}
					if got := read(t, p); got != c.in {
						t.Fatalf("failed edit changed file: %q", got)
					}
				})
			}
		})
	}
}

func TestYAMLTopValidatesBeforeWriting(t *testing.T) {
	const in = "GOOSE_MODEL: &model old\nother: *model\n"
	for _, c := range []struct {
		name string
		fn   func(string) error
	}{
		{"replace anchor", func(p string) error { return SetYAMLTop(p, KV{"GOOSE_MODEL", "new"}) }},
		{"delete anchor", func(p string) error { return DelYAMLTop(p, "GOOSE_MODEL") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := tmpFile(t, "config.yaml", in)
			if err := c.fn(p); err == nil || !strings.Contains(err.Error(), p) {
				t.Fatalf("error = %v, want an error with the file path", err)
			}
			if got := read(t, p); got != in {
				t.Fatalf("failed edit changed file: %q", got)
			}
		})
	}
}

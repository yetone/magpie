package edit

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

func TestTOMLWritesRejectInvalidOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(string) error
	}{
		{"table name", func(p string) error {
			return SetTOMLTable(p, "mcp_servers.my server", KV{Path: "name", Value: "new"})
		}},
		{"table key", func(p string) error {
			return SetTOMLTable(p, "a", KV{Path: "bad key", Value: "new"})
		}},
		{"table value", func(p string) error {
			return SetTOMLTable(p, "a", KV{Path: "name", Value: Raw("[")})
		}},
		{"key table name", func(p string) error {
			return SetTOMLKey(p, "mcp_servers.my server", "name", "new")
		}},
		{"key name", func(p string) error { return SetTOMLKey(p, "a", "bad key", "new") }},
		{"key value", func(p string) error { return SetTOMLKey(p, "a", "name", Raw("[")) }},
		{"empty value", func(p string) error { return SetTOMLKey(p, "a", "name", Raw("")) }},
		{"invalid date", func(p string) error { return SetTOMLKey(p, "a", "name", Raw("2026-09-99")) }},
		{"batch table name", func(p string) error {
			return SetTOMLTables(p, []string{"a"}, []Table{{Name: "mcp_servers.my server"}})
		}},
		{"batch key", func(p string) error {
			return SetTOMLTables(p, []string{"a"}, []Table{{Name: "a", KVs: []KV{{Path: "bad key", Value: "new"}}}})
		}},
		{"top key", func(p string) error { return SetTOMLTop(p, KV{Path: "bad key", Value: "new"}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, state := range []string{"existing", "missing"} {
				t.Run(state, func(t *testing.T) {
					input := ""
					if state == "existing" {
						input = "# keep\n[a]\nname = \"old\" # keep\n\n[b]\nenabled = true\n"
					}
					p := tmpFile(t, "config.toml", input)
					err := tc.edit(p)
					if err == nil || !strings.HasPrefix(err.Error(), p+": edited TOML is invalid: line ") ||
						!strings.Contains(err.Error(), ", column ") {
						t.Fatalf("expected a located error for the edited TOML, got %v", err)
					}
					var parseErr *unstable.ParserError
					var decodeErr *toml.DecodeError
					if !errors.As(err, &parseErr) && !errors.As(err, &decodeErr) {
						t.Fatalf("lost the underlying TOML error: %v", err)
					}
					if state == "existing" {
						assertTOMLContent(t, p, input)
					} else if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("invalid edit created the missing file: %v", err)
					}
				})
			}
		})
	}
}

func TestTOMLWritesRejectDefinitionConflicts(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		edit        func(string) error
	}{
		{"scalar as table", "a = 1\n", func(p string) error { return SetTOMLTable(p, "a") }},
		{"inline table as table", "a = { name = \"old\" }\n", func(p string) error {
			return SetTOMLKey(p, "a", "name", "new")
		}},
		{"table as key", "[a]\n[b]\nkeep = true\n", func(p string) error {
			return SetTOMLTop(p, KV{Path: "a", Value: "new"})
		}},
		{"child table as key", "[a]\nname = \"old\"\n[a.child]\nkeep = true\n", func(p string) error {
			return SetTOMLKey(p, "a", "child", Raw("{}"))
		}},
		{"array as table", "[[a]]\nname = \"old\"\n", func(p string) error {
			return SetTOMLTables(p, nil, []Table{{Name: "a"}})
		}},
		{"duplicate key", "[a]\nname = \"old\"\n", func(p string) error {
			return SetTOMLTable(p, "a", KV{Path: "name", Value: "new"}, KV{Path: `"name"`, Value: "duplicate"})
		}},
		{"duplicate table", "[a]\nname = \"old\"\n", func(p string) error {
			return SetTOMLTables(p, []string{"a"}, []Table{{Name: "a"}, {Name: `"a"`}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tmpFile(t, "config.toml", tc.input)
			err := tc.edit(p)
			if err == nil || !strings.HasPrefix(err.Error(), p+": edited TOML is invalid: ") {
				t.Fatalf("expected a definition conflict naming the file, got %v", err)
			}
			assertTOMLContent(t, p, tc.input)
		})
	}
}

func TestTOMLTopRejectsBrokenMultilineEdits(t *testing.T) {
	const input = "value = [\n1,\n2\n]\n\n[a]\nkeep = true\n"
	for _, tc := range []struct {
		name string
		edit func(string) error
	}{
		{"replace", func(p string) error { return SetTOMLTop(p, KV{Path: "value", Value: "new"}) }},
		{"delete", func(p string) error { return DelTOMLTop(p, "value") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := tmpFile(t, "config.toml", input)
			if err := tc.edit(p); err == nil {
				t.Fatal("expected the incomplete multiline edit to be rejected")
			}
			assertTOMLContent(t, p, input)
		})
	}
}

func TestTOMLWritesAcceptQuotedNames(t *testing.T) {
	const name = `mcp_servers."my server"`
	p := tmpFile(t, "config.toml", "# keep\n")
	if err := SetTOMLTable(p, name, KV{Path: `"display name"`, Value: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := SetTOMLKey(p, name, "display name", "new"); err != nil {
		t.Fatal(err)
	}
	assertTOMLContent(t, p, "# keep\n\n["+name+"]\n\"display name\" = \"new\"\n")
}

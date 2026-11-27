package agent

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// tomlTopKeys reads the same top-level keys a whole parse finds, where
// text that looks like a table or a key sits in a string or an array.
func TestTomlTopKeysAsAWholeParse(t *testing.T) {
	docs := []string{
		"",
		"model = \"gpt\"\n",
		"model = \"gpt\"\na.b.c = 1\n\"quoted key\" = 2\n[mcp_servers.x]\ncommand = \"y\"\n",
		"notes = \"\"\"\n[projects.\"/fake\"]\nfake = 1\n\"\"\"\n[shell_environment_policy]\ninherit = \"all\"\n",
		"list = [\n  \"[not.a.table]\",\n  'x = 1',\n]\n[[hooks]]\nname = \"a\"\n[[hooks]]\nname = \"b\"\n",
		"[projects.\"/a\"]\ntrust_level = \"trusted\"\n[projects.\"/b\"]\ntrust_level = \"trusted\"\n[tui]\nx = 1\n",
		"inline = { a = 1, b = { c = 2 } }\n'lit' = 'v'\n[\"tab le\".sub]\nk = 1\n",
	}
	for _, d := range docs {
		got, err := tomlTopKeys([]byte(d))
		if err != nil {
			t.Fatalf("%q: %v", d, err)
		}
		var m map[string]any
		if err := toml.Unmarshal([]byte(d), &m); err != nil {
			t.Fatalf("%q: whole parse: %v", d, err)
		}
		want := slices.Sorted(maps.Keys(m))
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%q: top keys %v, a whole parse %v", d, got, want)
		}
	}
	if _, err := tomlTopKeys([]byte("x = \n")); err == nil {
		t.Error("a broken document read without an error")
	}
}

// A config.toml that holds only the keys magpie writes is magpie's; one
// with a key given twice, which Codex can't read, isn't known to be, as
// when the whole file was parsed for its keys; and one with the user's
// keys too is the agent's.
func TestSyncedOnlyTOML(t *testing.T) {
	dir := t.TempDir()
	a := &Agent{Dir: dir, syncedKeys: codexSyncedKeys}
	p := filepath.Join(dir, "config.toml")
	for _, c := range []struct {
		doc  string
		want bool
	}{
		{"[mcp_servers.x]\ncommand = \"y\"\n[shell_environment_policy]\ninherit = \"all\"\n", true},
		{"[mcp_servers.x]\ncommand = \"y\"\n[mcp_servers.x]\ncommand = \"z\"\n", false},
		{"model = \"gpt\"\n[mcp_servers.x]\ncommand = \"y\"\n", false},
		{"[projects.\"/a\"]\ntrust_level = \"trusted\"\n[mcp_servers.x]\ncommand = \"y\"\n", false},
		{"", false},
	} {
		if err := os.WriteFile(p, []byte(c.doc), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := a.syncedOnly(p); got != c.want {
			t.Errorf("%q: syncedOnly = %v, want %v", c.doc, got, c.want)
		}
	}
}

// Codex's config.toml with a table per trusted folder, as the owner's
// (10,940 of them): read for its keys in a small part of a whole parse.
func BenchmarkTomlTopKeysTrustedFolders(b *testing.B) {
	var s strings.Builder
	s.WriteString("model = \"gpt-5\"\n[mcp_servers.x]\ncommand = \"y\"\n")
	for i := range 11000 {
		fmt.Fprintf(&s, "[projects.\"/Users/someone/workspace/projects/p%d\"]\ntrust_level = \"trusted\"\n\n", i)
	}
	doc := []byte(s.String())
	b.Run("keys", func(b *testing.B) {
		for b.Loop() {
			if _, err := tomlTopKeys(doc); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("whole", func(b *testing.B) {
		for b.Loop() {
			var m map[string]any
			if err := toml.Unmarshal(doc, &m); err != nil {
				b.Fatal(err)
			}
		}
	})
}

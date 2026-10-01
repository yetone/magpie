package library

import (
	"path/filepath"
	"strings"
	"testing"
)

// A patch list in flow style (#445) is read and written like a block one:
// the user's rows stay, magpie's server goes in as an insert of its own.
func TestDshFlowPatchList(t *testing.T) {
	h := sandbox(t)
	p := filepath.Join(h, ".dsh/profiles/web/cordis.patch.yml")
	write(t, p, "# Your patch layer for this dsh profile\n[ { id: some-plugin, disabled: false } ]\n")
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: []string{"dsh"}}))
	s := read(t, p)
	for _, want := range []string{"# Your patch layer for this dsh profile\n- id: some-plugin\n  disabled: false\n", "- insert: # magpie\n    - id: magpie-mcp-fs\n", `serverName: "fs"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
}

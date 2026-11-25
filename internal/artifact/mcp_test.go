package artifact

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The mcp manifest is checked at publish as the contract has it: 1 to 32
// servers of a name each, each with 1 to 128 tools of a tool's name, none
// listed twice; a version reads what it allows.
func TestMCPManifest(t *testing.T) {
	many := func(n int, f func(i int) string) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = f(i)
		}
		return strings.Join(parts, ",")
	}
	for _, bad := range []string{
		`{"mcp":{}}`,
		`{"mcp":true}`,
		`{"mcp":{"servers":[]}}`,
		`{"mcp":{"servers":"github"}}`,
		`{"mcp":{"servers":[{"server":"github"}]}}`,
		`{"mcp":{"servers":[{"server":"github","tools":[]}]}}`,
		`{"mcp":{"servers":[{"server":"","tools":["a"]}]}}`,
		`{"mcp":{"servers":[{"server":"git\nhub","tools":["a"]}]}}`,
		`{"mcp":{"servers":[{"server":"github","tools":["a b"]}]}}`,
		`{"mcp":{"servers":[{"server":"github","tools":["a","a"]}]}}`,
		`{"mcp":{"servers":[{"server":"github","tools":["a"]},{"server":"github","tools":["b"]}]}}`,
		`{"mcp":{"servers":[{"server":"` + strings.Repeat("x", 201) + `","tools":["a"]}]}}`,
		`{"mcp":{"servers":[{"server":"github","tools":["` + strings.Repeat("t", 129) + `"]}]}}`,
		`{"mcp":{"servers":[` + many(33, func(i int) string { return fmt.Sprintf(`{"server":"s%d","tools":["a"]}`, i) }) + `]}}`,
		`{"mcp":{"servers":[{"server":"github","tools":[` + many(129, func(i int) string { return fmt.Sprintf(`"t%d"`, i) }) + `]}]}}`,
	} {
		if _, err := ParseCapabilities(json.RawMessage(bad)); err == nil || !strings.Contains(err.Error(), "mcp") {
			t.Errorf("%.80s was taken: %v", bad, err)
		}
	}
	c, err := ParseCapabilities(json.RawMessage(`{"mcp":{"servers":[{"server":"Google Calendar","tools":["list_events","create-event","a.b"]},{"server":"host:my_fs","tools":["read_file"]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	m := c.MCP()
	if m == nil || !m.Allows("Google Calendar", "create-event") || !m.Allows("host:my_fs", "read_file") || m.Allows("Google Calendar", "read_file") || m.Allows("github", "list_events") {
		t.Fatalf("%+v", m)
	}
	if (Capabilities{"db": json.RawMessage(`{}`)}).MCP() != nil {
		t.Fatal("a version without mcp has a manifest")
	}
}

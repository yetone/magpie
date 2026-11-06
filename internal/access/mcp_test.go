package access

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// A key's MCP list is servers whole and <server>/<tool> entries, kept as
// written; "-" alone holds it to none, and a bad entry is refused.
func TestCleanMCP(t *testing.T) {
	got, err := CleanMCP([]string{" github ", "", "fs/read", "github/search_code", "fs/read", "magpie-web-search/web_fetch"})
	if err != nil || !slices.Equal(got, []string{"github", "fs/read", "magpie-web-search/web_fetch"}) {
		t.Fatal(got, err)
	}
	if got, err := CleanMCP([]string{NoMCP}); err != nil || !slices.Equal(got, []string{NoMCP}) {
		t.Fatal("none:", got, err)
	}
	if got, err := CleanMCP([]string{NoMCP, "fs"}); err != nil || !slices.Equal(got, []string{"fs"}) {
		t.Fatal("none beside a server:", got, err)
	}
	if got, err := CleanMCP(nil); err != nil || len(got) != 0 {
		t.Fatal("all:", got, err)
	}
	for _, bad := range []string{"fs/", "/read", "-fs", "a b", "fs/ read", "fs/" + strings.Repeat("x", 129), strings.Repeat("s", 65)} {
		if _, err := CleanMCP([]string{bad}); err == nil {
			t.Error("accepted", bad)
		}
	}
	many := make([]string, 501)
	for i := range many {
		many[i] = "fs/t" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + strings.Repeat("y", i/26)
	}
	if _, err := CleanMCP(many); err == nil {
		t.Error("took 501 entries")
	}
}

// What a key may use: everything with no list, a server listed whole
// with every tool, a listed tool alone of its server, nothing with "-";
// an issued key is held to its issuer's list too.
func TestKeyMCPAllows(t *testing.T) {
	all := Identity{}
	if all.MCPHeld() || !all.MCPAllows("fs", "read") {
		t.Fatal("a key with no list is held")
	}
	if ok, every := all.MCPServer("fs"); !ok || !every {
		t.Fatal("a key with no list: fs", ok, every)
	}
	who := Identity{MCP: []string{"github", "fs/read"}}
	for _, c := range []struct {
		server, tool string
		want         bool
	}{
		{"github", "create_issue", true},
		{"fs", "read", true},
		{"fs", "write", false},
		{"magpie-web-search", "web_search", false},
		{"fs/read", "", false},
	} {
		if got := who.MCPAllows(c.server, c.tool); got != c.want {
			t.Errorf("%s %s: %v", c.server, c.tool, got)
		}
	}
	if ok, every := who.MCPServer("fs"); !ok || every {
		t.Error("fs is some tools:", ok, every)
	}
	if ok, every := who.MCPServer("github"); !ok || !every {
		t.Error("github is whole:", ok, every)
	}
	if ok, _ := who.MCPServer("linear"); ok {
		t.Error("linear isn't listed")
	}
	none := Identity{MCP: []string{NoMCP}}
	if ok, _ := none.MCPServer("fs"); ok || none.MCPAllows("fs", "read") || !none.MCPHeld() {
		t.Error("a key held to none")
	}
	// issued under a key held to github: its own wider list doesn't widen it
	child := Identity{MCP: []string{"github", "fs"}, Issuer: &Identity{MCP: []string{"github/search_code"}}}
	if !child.MCPHeld() || child.MCPAllows("fs", "read") || child.MCPAllows("github", "create_issue") || !child.MCPAllows("github", "search_code") {
		t.Error("an issued key past its issuer")
	}
	if ok, every := child.MCPServer("github"); !ok || every {
		t.Error("github under the issuer:", ok, every)
	}
	free := Identity{Issuer: &Identity{MCP: []string{"fs"}}}
	if !free.MCPHeld() || free.MCPAllows("github", "x") || !free.MCPAllows("fs", "x") {
		t.Error("an issued key with no list of its own")
	}
}

// A key's MCP list is kept in caller-keys.json, reaches the gateway with
// the key and with every key issued under it, and is cleared by an empty
// list; a bad one is refused and leaves the list as it was.
func TestKeyMCPKept(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clockAt(t, time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC))
	id, secret := addKey(t, "Phone")
	if who, ok := Authenticate(secret); !ok || who.MCPHeld() {
		t.Fatal("a new key is held", who)
	}
	if _, err := Update("mcp-key", Change{Key: id, MCP: []string{"github", "fs/read"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Update("mcp-key", Change{Key: id, MCP: []string{"fs/"}}); err == nil {
		t.Fatal("took a bad entry")
	}
	who, ok := Authenticate(secret)
	if !ok || !slices.Equal(who.MCP, []string{"github", "fs/read"}) || who.MCPAllows("fs", "write") {
		t.Fatal(who, ok)
	}
	if keys, _ := List(); !slices.Equal(keys[0].MCP, []string{"github", "fs/read"}) {
		t.Fatal("listed", keys[0].MCP)
	}
	if _, err := Update("issuer-key", Change{Key: id, Issues: true}); err != nil {
		t.Fatal(err)
	}
	child, err := IssueKey(Issue{TTL: time.Hour, Issuer: id})
	if err != nil {
		t.Fatal(err)
	}
	if cw, ok := Authenticate(child.Key); !ok || !cw.MCPHeld() || cw.MCPAllows("fs", "write") || !cw.MCPAllows("github", "x") {
		t.Fatal("issued:", cw, ok)
	}
	if _, err := Update("mcp-key", Change{Key: id}); err != nil {
		t.Fatal(err)
	}
	if who, _ := Authenticate(secret); who.MCPHeld() {
		t.Fatal("an empty list still holds", who.MCP)
	}
}

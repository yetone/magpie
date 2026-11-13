package library

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestServerHereThroughSync: one server on a Mac and in WSL (#3836, #6426,
// jinpwng on X). This computer runs its own program; its note, agents and
// the other computer's command still come and go with a sync, and what
// this computer carries is the other computer's command, never its own.
func TestServerHereThroughSync(t *testing.T) {
	sandbox(t)
	ok(t)(SaveServer("", Server{Name: "order-tools", Transport: "stdio", Command: "/Users/me/order-mcp", Args: []string{"--config", "mac.yaml"}, Agents: []string{"claude"}, Note: "orders"}))
	mac := carry(t)

	h := sandbox(t)
	ok(t)(Put(&mac))
	ok(t)(ServerHere("order-tools", []string{"/opt/order-mcp", "--config", "/etc/ops.yaml"}))
	s := serverIn(t, "order-tools")
	if s.Command != "/opt/order-mcp" || s.Shared == nil || s.Shared.Command != "/Users/me/order-mcp" {
		t.Fatalf("after its own command here: %+v", s)
	}
	if c := read(t, filepath.Join(h, ".claude.json")); !strings.Contains(c, "/opt/order-mcp") || strings.Contains(c, "/Users/me") {
		t.Fatalf("Claude Code was given %s", c)
	}
	b := carry(t)
	if len(b.MCP) != 1 || b.MCP[0].Command != "/Users/me/order-mcp" || strings.Join(b.MCP[0].Args, " ") != "--config mac.yaml" || b.MCP[0].Shared != nil {
		t.Fatalf("carried %s", mustJSON(t, b))
	}
	if j := mustJSON(t, b); strings.Contains(j, "/opt/") || strings.Contains(j, `"shared"`) {
		t.Fatalf("this computer's own command was carried: %s", j)
	}
	if got, want := mustJSON(t, b), mustJSON(t, mac); got != want {
		t.Fatalf("what this computer carries isn't what came in:\n%s\n%s", want, got)
	}

	// the Mac changes the note and its own path; this computer keeps its own
	mac.MCP[0].Note, mac.MCP[0].Command = "orders, read only", "/Users/me/bin/order-mcp"
	ok(t)(Put(&mac))
	s = serverIn(t, "order-tools")
	if s.Note != "orders, read only" || s.Command != "/opt/order-mcp" || s.Shared == nil || s.Shared.Command != "/Users/me/bin/order-mcp" {
		t.Fatalf("after the Mac's change came in: %+v %+v", s, s.Shared)
	}
	// an edit here, as the editor sends it, keeps the synced command
	n := *s
	n.Agents = []string{"claude", "codex"}
	ok(t)(SaveServer("order-tools", n))
	if b := carry(t); b.MCP[0].Command != "/Users/me/bin/order-mcp" || len(b.MCP[0].Agents) != 2 {
		t.Fatalf("carried after an edit here: %s", mustJSON(t, b))
	}
	if c := read(t, filepath.Join(h, ".codex/config.toml")); !strings.Contains(c, "/opt/order-mcp") {
		t.Fatalf("Codex was given %s", c)
	}

	// back to the synced one
	ok(t)(ServerHere("order-tools", nil))
	if s := serverIn(t, "order-tools"); s.Command != "/Users/me/bin/order-mcp" || s.Shared != nil || strings.Join(s.Args, " ") != "--config mac.yaml" {
		t.Fatalf("after going back to the synced command: %+v", s)
	}
}

// TestServerHereEnds: the editor sending no synced command, the server
// kept to this computer, or one that comes in as a URL each end this
// computer's own command.
func TestServerHereEnds(t *testing.T) {
	sandbox(t)
	ok(t)(SaveServer("", Server{Name: "db", Transport: "stdio", Command: "their-db", Agents: []string{"claude"}}))
	theirs := carry(t)
	ok(t)(ServerHere("db", []string{"my-db"}))

	n := *serverIn(t, "db")
	n.Shared = nil
	ok(t)(SaveServer("db", n))
	if b := carry(t); b.MCP[0].Command != "my-db" {
		t.Fatalf("the editor turned it off, yet carried %s", mustJSON(t, b))
	}

	ok(t)(ServerHere("db", []string{"mine"}))
	ok(t)(ServerLocal("db", true))
	if s := serverIn(t, "db"); s.Shared != nil || s.Command != "mine" {
		t.Fatalf("kept to this computer: %+v", s)
	}
	ok(t)(ServerLocal("db", false))

	ok(t)(ServerHere("db", []string{"mine"}))
	url := theirs
	url.MCP = []*Server{{Name: "db", Transport: "http", URL: "https://db.example/mcp", Agents: []string{"claude"}}}
	ok(t)(Put(&url))
	if s := serverIn(t, "db"); s.Shared != nil || s.URL != "https://db.example/mcp" || s.Command != "" {
		t.Fatalf("after a URL came in: %+v", s)
	}
	if _, err := ServerHere("db", []string{"mine"}); err == nil {
		t.Fatal("a URL server took a command of its own")
	}
}

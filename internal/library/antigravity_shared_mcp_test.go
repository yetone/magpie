package library

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// izhidigua on X (0.1.4479): Antigravity and agy read one mcp_config.json.
// With both ticked, the MCP router off and on again left an entry behind,
// and took the "magpie" entry the one wrote for a server of the user's
// own in the other, so it kept the servers direct.
func TestAntigravitySharedMCPFile(t *testing.T) {
	h, cfg := antigravity(t)
	write(t, filepath.Join(h, ".gemini/antigravity/agyhub_summaries_proto.pb"), "x")
	p := filepath.Join(cfg, "mcp_config.json")
	both := []string{"agy", "antigravity"}
	for _, id := range both {
		if tg := targetByID(id); tg == nil || tg.MCP == nil || tg.MCP.Path != p {
			t.Fatalf("%s: %+v", id, tg)
		}
	}
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Agents: both}))
	ok(t)(SaveServer("", Server{Name: "web", Transport: "http", URL: "https://example.com/mcp", Agents: both}))
	names := func() map[string]bool {
		var f struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(read(t, p)), &f); err != nil {
			t.Fatalf("%v:\n%s", err, read(t, p))
		}
		m := map[string]bool{}
		for k := range f.MCPServers {
			m[k] = true
		}
		return m
	}
	t.Cleanup(hubStopAll)
	for round := 0; round < 2; round++ {
		ok(t)(SetMCPHub(true))
		if got := names(); len(got) != 1 || !got[HubEntry] {
			t.Fatalf("round %d, router on: %v\n%s", round, got, read(t, p))
		}
		ok(t)(SetMCPHub(false))
		if got := names(); len(got) != 2 || !got["fs"] || !got["web"] {
			t.Fatalf("round %d, router off: %v\n%s", round, got, read(t, p))
		}
	}
	l, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range l.Servers {
		if s.Hub == HubDirect {
			t.Errorf("%s kept direct", s.Name)
		}
	}
}

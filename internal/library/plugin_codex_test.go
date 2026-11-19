package library

import (
	"path/filepath"
	"strings"
	"testing"
)

// A plugin with only Codex's manifest (#7255) is imported like a Claude Code
// one: its skills and servers by the manifest's paths, and the hooks it names
// are Codex's own, given to Codex alone, as written.
func TestPluginCodexManifest(t *testing.T) {
	h := sandbox(t)
	pluginAgents(t, h)
	src := filepath.Join(t.TempDir(), "cx")
	write(t, filepath.Join(src, ".codex-plugin/plugin.json"), `{"name":"cx-kit","version":"0.3.0","description":"For Codex","skills":"./skills/","mcpServers":"./.mcp.json","hooks":"./codex-hooks.json"}`)
	write(t, filepath.Join(src, "skills/tdd/SKILL.md"), "---\nname: tdd\ndescription: Test first\n---\nWrite the test first.\n")
	write(t, filepath.Join(src, ".mcp.json"), `{"mcpServers":{"cx-db":{"command":"db"}}}`)
	write(t, filepath.Join(src, "codex-hooks.json"), `{"hooks":{
	  "PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"echo ask"}]}],
	  "Stop":[{"hooks":[{"type":"command","command":"echo done"}]}],
	  "Nonsense":[{"hooks":[{"type":"command","command":"echo no"}]}]}}`)

	p, err := ProbePlugins(src)
	if err != nil || len(p.Candidates) != 1 {
		t.Fatalf("probe: %v %+v", err, p)
	}
	c := p.Candidates[0]
	if c.Name != "cx-kit" || c.Skills != 1 || c.MCP != 1 || c.Hooks != 2 || len(c.Left) != 1 {
		t.Fatalf("candidate: %+v", c)
	}
	ok(t)(InstallPlugins(src, nil, []string{"claude", "codex"}))
	codex := read(t, filepath.Join(h, ".codex/hooks.json"))
	for _, want := range []string{"PermissionRequest", "echo ask", "Stop", "echo done"} {
		if !strings.Contains(codex, want) {
			t.Errorf("codex hooks lack %q:\n%s", want, codex)
		}
	}
	if s := read(t, filepath.Join(h, ".claude/settings.json")); strings.Contains(s, "echo ask") || strings.Contains(s, "echo done") {
		t.Errorf("claude was given Codex's hooks:\n%s", s)
	}
	if !strings.Contains(read(t, filepath.Join(h, ".codex/config.toml")), "cx-db") {
		t.Errorf("codex lacks the server")
	}
	ok(t)(RemovePlugin("cx-kit"))
	if s := read(t, filepath.Join(h, ".codex/hooks.json")); strings.Contains(s, "echo ask") {
		t.Errorf("hooks stay after remove:\n%s", s)
	}
}

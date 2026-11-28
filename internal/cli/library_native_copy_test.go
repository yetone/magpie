package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/library"
)

// magpie library native copy (0xRichardH on X): an MCP server a plugin
// Codex installed itself declares goes into the library for no agent; one
// that works only inside the Codex app is refused with why, and a name two
// agents' plugins declare needs the agent.
func TestLibraryNativeCopy(t *testing.T) {
	h := t.TempDir()
	for k, v := range map[string]string{"HOME": h, "USERPROFILE": h, "XDG_CONFIG_HOME": filepath.Join(h, ".config"), "CLAUDE_CONFIG_DIR": "", "CODEX_HOME": "", "APPDATA": "", "LOCALAPPDATA": "", "PATH": filepath.Join(h, "bin")} {
		t.Setenv(k, v)
	}
	put := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cache := filepath.Join(h, ".codex", "plugins", "cache")
	put(filepath.Join(h, ".codex", "config.toml"), "")
	lin := filepath.Join(cache, "openai-curated", "linear", "1.0.0")
	put(filepath.Join(lin, ".codex-plugin", "plugin.json"), `{"name": "linear", "version": "1.0.0"}`)
	put(filepath.Join(lin, ".mcp.json"), `{"mcpServers": {"linear": {"type": "http", "url": "https://mcp.linear.app/mcp"}}}`)
	app := filepath.Join(cache, "openai-bundled", "codex-app-tools", "1.0.0")
	put(filepath.Join(app, ".codex-plugin", "plugin.json"), `{"name": "codex-app-tools", "version": "1.0.0"}`)
	put(filepath.Join(app, ".mcp.json"), `{"mcpServers": {"codex_app": {"command": "node", "args": ["./server.mjs"], "cwd": "."}}}`)
	cc := filepath.Join(h, ".claude")
	inst := filepath.Join(cc, "plugins", "cache", "m", "lin", "0.1.0")
	put(filepath.Join(cc, "plugins", "installed_plugins.json"), `{"version": 2, "plugins": {"lin@m": [{"scope": "user", "installPath": "`+filepath.ToSlash(inst)+`", "version": "0.1.0"}]}}`)
	put(filepath.Join(inst, ".claude-plugin", "plugin.json"), `{"name": "lin"}`)
	put(filepath.Join(inst, ".mcp.json"), `{"mcpServers": {"linear": {"type": "http", "url": "https://mcp.linear.app/mcp"}}}`)

	if err := libraryCmd([]string{"library", "native", "copy", "linear"}); err == nil || !strings.Contains(err.Error(), "say which agent") {
		t.Errorf("two agents' linear: %v", err)
	}
	if err := libraryCmd([]string{"library", "native", "copy", "codex_app"}); err == nil || err.Error() != library.NoCopyWhy("app", "Codex") {
		t.Errorf("codex_app: %v", err)
	}
	if err := libraryCmd([]string{"library", "native", "copy", "linear", "codex"}); err != nil {
		t.Fatal(err)
	}
	v, err := library.Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	var got *library.Server
	for _, s := range v.Servers {
		if s.Name == "linear" {
			got = s.Server
		}
	}
	if got == nil || got.URL != "https://mcp.linear.app/mcp" || len(got.Agents) != 0 {
		t.Fatalf("library: %+v", got)
	}
	if err := libraryCmd([]string{"library", "native", "copy", "nope"}); err == nil {
		t.Error("a server no plugin declares")
	}
}

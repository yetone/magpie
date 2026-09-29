package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tidwall/jsonc"
)

func piDoc(t *testing.T, p string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(jsonc.ToJSON([]byte(read(t, p))), &doc); err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return doc
}

func piServers(t *testing.T, p string) map[string]map[string]any {
	t.Helper()
	var doc struct{ MCPServers map[string]map[string]any }
	if err := json.Unmarshal(jsonc.ToJSON([]byte(read(t, p))), &doc); err != nil {
		t.Fatalf("%s: %v", p, err)
	}
	return doc.MCPServers
}

func TestPiPackageName(t *testing.T) {
	for spec, want := range map[string][2]string{
		"npm:pi-mcp-adapter":                               {"pi-mcp-adapter", ""},
		"npm:pi-mcp-adapter@2.9.1":                         {"pi-mcp-adapter", "2.9.1"},
		"npm:@scope/pkg@1.0.0":                             {"@scope/pkg", "1.0.0"},
		"git:github.com/nicobailon/pi-mcp-adapter@v3.0.0":  {"pi-mcp-adapter", "3.0.0"},
		"https://github.com/nicobailon/pi-mcp-adapter.git": {"pi-mcp-adapter", ""},
		`C:\src\pi-mcp-extension\`:                         {"pi-mcp-extension", ""},
		"../pi-mcp-adapter":                                {"pi-mcp-adapter", ""},
	} {
		if n, v := piPackageName(spec); n != want[0] || v != want[1] {
			t.Errorf("%s: %s %s", spec, n, v)
		}
	}
}

// xiaozhu1337 on #58: pi-mcp-adapter 3 installed where Pi used to put
// packages, npm's global folder, so the adapter's own folder in the agent
// dir wasn't there and magpie kept writing mcp.json. With mcp-adapter.json
// already there too, mcp.json's servers are merged in and mcp.json goes.
func TestPiMCPGlobalAdapterMerge(t *testing.T) {
	h := sandbox(t)
	d := filepath.Join(h, ".pi/agent")
	g := filepath.Join(h, "npm-global")
	piGlobalRoots = func() []string { return []string{g} }
	write(t, filepath.Join(d, "settings.json"), `{"packages": ["npm:pi-mcp-adapter"]}`)
	write(t, filepath.Join(g, "pi-mcp-adapter/package.json"), `{"name": "pi-mcp-adapter", "version": "3.1.0"}`)
	old, adapter := filepath.Join(d, "mcp.json"), filepath.Join(d, "mcp-adapter.json")
	write(t, old, `{
  // mine
  "mcpServers": {
    "same": {"command": "a"},
    "moved": {"command": "b", "args": ["x"], "directTools": true},
    "clash": {"command": "old"}
  },
  "settings": {"toolPrefix": "short"}
}`)
	write(t, adapter, `{"mcpServers": {"same": {"command": "a"}, "clash": {"command": "new"}}}`)
	tg := targetByID("pi")
	if tg.MCP.Path != adapter {
		t.Fatalf("path %s", tg.MCP.Path)
	}
	got := piServers(t, adapter)
	if got["moved"]["directTools"] != true || got["clash"]["command"] != "new" || got["same"] == nil {
		t.Errorf("mcp-adapter.json: %v", got)
	}
	if piDoc(t, adapter)["settings"] == nil {
		t.Error("settings not moved")
	}
	// what differs stays for the user, and is still found
	left := piServers(t, old)
	if len(left) != 1 || left["clash"]["command"] != "old" {
		t.Errorf("left in mcp.json: %v", left)
	}
	if len(tg.MCP.Extra) != 1 || tg.MCP.Extra[0] != old {
		t.Errorf("extra: %v", tg.MCP.Extra)
	}
	have, err := tg.MCP.read()
	if err != nil || have["moved"] == nil || have["clash"].Command != "new" {
		t.Errorf("read: %v %v", have, err)
	}
	bs, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "pi", "mcp.json"))
	if len(bs) != 1 {
		t.Errorf("backups: %v", bs)
	}
	// once nothing is left, mcp.json goes
	write(t, old, `{"mcpServers": {"clash": {"command": "new"}}}`)
	targetByID("pi")
	if exists(old) {
		t.Errorf("mcp.json left: %s", read(t, old))
	}
}

// Listed but not found (a git or odd install): taken as the current 3.
func TestPiMCPAdapterListed(t *testing.T) {
	h := sandbox(t)
	d := filepath.Join(h, ".pi/agent")
	write(t, filepath.Join(d, "settings.json"), `{"packages": [{"source": "npm:pi-mcp-adapter"}]}`)
	write(t, filepath.Join(d, "mcp.json"), `{"mcpServers": {"mine": {"command": "x", "lifecycle": "eager"}}}`)
	tg := targetByID("pi")
	if tg.MCP.Path != filepath.Join(d, "mcp-adapter.json") || exists(filepath.Join(d, "mcp.json")) {
		t.Fatalf("path %s", tg.MCP.Path)
	}
	if piServers(t, tg.MCP.Path)["mine"]["lifecycle"] != "eager" {
		t.Error("renamed file lost the user's field")
	}
	// pinned to 2: mcp.json, which it reads
	write(t, filepath.Join(d, "settings.json"), `{"packages": ["npm:pi-mcp-adapter@2.9.1"]}`)
	os.Remove(filepath.Join(d, "mcp-adapter.json"))
	if tg := targetByID("pi"); tg.MCP.Path != filepath.Join(d, "mcp.json") {
		t.Errorf("pinned 2: %s", tg.MCP.Path)
	}
}

// With pi-mcp-extension too, both files get the servers, each keeping the
// user's fields, and the extension's mcp.json is left where it is.
func TestPiMCPBoth(t *testing.T) {
	h := sandbox(t)
	d := filepath.Join(h, ".pi/agent")
	write(t, filepath.Join(d, "settings.json"), `{"packages": ["npm:pi-mcp-extension", "npm:pi-mcp-adapter"]}`)
	write(t, filepath.Join(d, "npm/node_modules/pi-mcp-adapter/package.json"), `{"name": "pi-mcp-adapter", "version": "3.1.0"}`)
	old, adapter := filepath.Join(d, "mcp.json"), filepath.Join(d, "mcp-adapter.json")
	write(t, old, `{"mcpServers": {"ev": {"transport": "sse", "url": "https://example.com/old", "lifecycle": "eager"}, "theirs": {"command": "t"}}}`)
	tg := targetByID("pi")
	if tg.MCP.Path != adapter || len(tg.MCP.Also) != 1 || tg.MCP.Also[0] != old {
		t.Fatalf("target: %+v", tg.MCP)
	}
	if have, _ := tg.MCP.read(); have["theirs"] == nil {
		t.Errorf("mcp.json's own server not found: %v", have)
	}
	ok(t)(SaveServer("", Server{Name: "ev", Transport: "sse", URL: "https://example.com/sse", Agents: []string{"pi"}}))
	for _, p := range []string{old, adapter} {
		ev := piServers(t, p)["ev"]
		if ev["url"] != "https://example.com/sse" || ev["transport"] != "sse" || ev["httpTransport"] != "sse" {
			t.Errorf("%s: %v", p, ev)
		}
	}
	if piServers(t, old)["ev"]["lifecycle"] != "eager" || piServers(t, old)["theirs"] == nil {
		t.Errorf("mcp.json: %v", piServers(t, old))
	}
	// a file of the two missing the server is written again
	write(t, old, `{"mcpServers": {}}`)
	ok(t)(Sync())
	if piServers(t, old)["ev"] == nil {
		t.Error("mcp.json not written again")
	}
	ok(t)(RemoveServer("ev"))
	if piServers(t, old)["ev"] != nil || piServers(t, adapter)["ev"] != nil {
		t.Error("ev left behind")
	}
}

// pi-mcp-extension alone reads its home's ~/.pi/agent/mcp.json, whatever
// PI_CODING_AGENT_DIR says; with nothing installed, mcp-adapter.json.
func TestPiMCPExtension(t *testing.T) {
	h := sandbox(t)
	if tg := targetByID("pi"); tg.MCP.Path != filepath.Join(h, ".pi/agent/mcp-adapter.json") {
		t.Errorf("nothing installed: %s", tg.MCP.Path)
	}
	d := filepath.Join(h, "pi-dir")
	t.Setenv("PI_CODING_AGENT_DIR", d)
	write(t, filepath.Join(d, "settings.json"), `{"packages": ["npm:pi-mcp-extension"]}`)
	if tg := targetByID("pi"); tg.MCP.Path != filepath.Join(h, ".pi/agent/mcp.json") {
		t.Errorf("extension: %s", tg.MCP.Path)
	}
}

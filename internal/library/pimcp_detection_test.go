package library

import (
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

func piAtVersion(t *testing.T, v string) {
	t.Helper()
	previous := piVersion
	piVersion = func(*agent.Agent) string { return v }
	t.Cleanup(func() { piVersion = previous })
}

// #1097: storage left after removing a package isn't a loaded extension.
// Sync must keep Pi 1.0's own servers in the file Pi actually reads.
func TestPiMCPUnusedPackagesKeepNative(t *testing.T) {
	for _, location := range []string{"npm", "git", "global"} {
		for _, name := range []string{"pi-mcp-adapter", "pi-mcp-extension"} {
			t.Run(location+"/"+name, func(t *testing.T) {
				h := sandbox(t)
				piAtVersion(t, "1.0.0")
				d := filepath.Join(h, ".pi", "agent")
				root := filepath.Join(d, "npm", "node_modules")
				switch location {
				case "git":
					root = filepath.Join(d, "git", "github.com", "owner")
				case "global":
					root = filepath.Join(h, "npm-global", "node_modules")
					piGlobalRoots = func() []string { return []string{root} }
				}
				write(t, filepath.Join(root, name, "package.json"), `{"name":"`+name+`","version":"4.0.0","pi":{"extensions":["./index.ts"]}}`)
				write(t, filepath.Join(root, name, "index.ts"), "export default function extension() {}\n")
				write(t, filepath.Join(d, "settings.json"), `{"quietStartup":false,"packages":[],"extensions":[]}`)
				native, adapter := filepath.Join(d, "mcp.json"), filepath.Join(d, "mcp-adapter.json")
				mine := "{\n  // added through pi mcp add\n  \"mcpServers\": {\"demo\": {\"url\": \"https://example.com/mcp\", \"enabled\": true}},\n  \"autoEnableCodemode\": false\n}\n"
				write(t, native, mine)
				if p := piDetect(d); p.adapter || p.ext || p.major != 0 {
					t.Errorf("unused %s in %s detected as loaded: %+v", name, location, p)
				}
				tg := targetByID("pi")
				if tg.MCP.Path != native || tg.MCP.Format != fmtPiNative || tg.MCPVia != "" {
					t.Fatalf("Pi 1.0 must use its native mcp.json: %+v via %q", tg.MCP, tg.MCPVia)
				}
				if read(t, native) != mine || exists(adapter) {
					t.Fatal("detecting an unused package moved the native config")
				}
				ok(t)(SaveServer("", Server{Name: "library", Transport: "http", URL: "https://example.com/library", Agents: []string{"pi"}}))
				ok(t)(Sync())
				got := piServers(t, native)
				if got["demo"]["url"] != "https://example.com/mcp" || got["demo"]["enabled"] != true || got["library"]["url"] != "https://example.com/library" {
					t.Fatalf("native file lost a server: %v", got)
				}
				if piDoc(t, native)["autoEnableCodemode"] != false || got["library"]["httpTransport"] != nil || exists(adapter) {
					t.Error("native sync changed the user's settings or wrote the adapter's format/file")
				}
				servers, err := tg.MCP.read()
				if err != nil || servers["demo"] == nil || servers["library"] == nil {
					t.Errorf("native servers unreadable: %v %v", servers, err)
				}
				bs, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "pi", "mcp.json"))
				if len(bs) != 1 || read(t, bs[0]) != mine {
					t.Fatalf("native sync must keep the original backup: %v", bs)
				}
			})
		}
	}
}

func TestPiDetectConfiguredAndDiscovered(t *testing.T) {
	for _, tc := range []struct {
		name, settings, dir, pkg, entry string
		want                            piPlugins
	}{
		{"npm", `{"packages":["npm:pi-mcp-adapter"]}`, "npm/node_modules/pi-mcp-adapter", `{"name":"pi-mcp-adapter","version":"4.0.0"}`, "", piPlugins{adapter: true, major: 4}},
		{"npm-disabled-adapter", `{"packages":[{"source":"npm:pi-mcp-adapter","extensions":[]}]}`, "npm/node_modules/pi-mcp-adapter", `{"name":"pi-mcp-adapter","version":"4.0.0"}`, "", piPlugins{}},
		{"npm-disabled-extension", `{"packages":[{"source":"npm:pi-mcp-extension","extensions":[]}]}`, "npm/node_modules/pi-mcp-extension", `{"name":"pi-mcp-extension","version":"1.5.0"}`, "", piPlugins{}},
		{"npm-autoload-true", `{"packages":[{"source":"npm:pi-mcp-adapter","autoload":true}]}`, "npm/node_modules/pi-mcp-adapter", `{"name":"pi-mcp-adapter","version":"4.0.0"}`, "", piPlugins{adapter: true, major: 4}},
		{"npm-autoload-selected", `{"packages":[{"source":"npm:pi-mcp-adapter","autoload":false,"extensions":["./index.ts"]}]}`, "npm/node_modules/pi-mcp-adapter", `{"name":"pi-mcp-adapter","version":"4.0.0","pi":{"extensions":["./index.ts"]}}`, "index.ts", piPlugins{adapter: true, major: 4}},
		{"npm-pinned", `{"packages":["npm:pi-mcp-adapter@2.9.1"]}`, "npm/node_modules/pi-mcp-adapter", `{"name":"pi-mcp-adapter","version":"4.0.0"}`, "", piPlugins{adapter: true, major: 2}},
		{"git", `{"packages":[{"source":"git:github.com/owner/pi-mcp-adapter"}]}`, "git/github.com/owner/pi-mcp-adapter", `{"name":"pi-mcp-adapter","version":"3.1.0"}`, "", piPlugins{adapter: true, major: 3}},
		{"local-package", `{"packages":["./pi-mcp-adapter"]}`, "pi-mcp-adapter", `{"name":"pi-mcp-adapter","version":"2.9.1"}`, "index.ts", piPlugins{adapter: true, major: 2}},
		{"explicit-extension", `{"extensions":["./pi-mcp-adapter"]}`, "pi-mcp-adapter", `{"name":"pi-mcp-adapter","version":"3.1.0"}`, "index.ts", piPlugins{adapter: true, major: 3}},
		{"explicit-no-manifest", `{"extensions":["./pi-mcp-adapter"]}`, "pi-mcp-adapter", "", "index.ts", piPlugins{adapter: true}},
		{"auto-index-ts", `{}`, "extensions/pi-mcp-adapter", `{"name":"pi-mcp-adapter","version":"4.0.0"}`, "index.ts", piPlugins{adapter: true, major: 4}},
		{"auto-index-js", `{}`, "extensions/pi-mcp-extension", `{"name":"pi-mcp-extension","version":"1.5.0"}`, "index.js", piPlugins{ext: true}},
		{"auto-manifest", `{}`, "extensions/pi-mcp-adapter", `{"name":"pi-mcp-adapter","version":"3.1.0","pi":{"extensions":["./src/extension.ts"]}}`, "src/extension.ts", piPlugins{adapter: true, major: 3}},
		{"auto-no-entry", `{}`, "extensions/pi-mcp-adapter", `{"name":"pi-mcp-adapter","version":"4.0.0"}`, "", piPlugins{}},
		{"auto-missing-entry", `{}`, "extensions/pi-mcp-extension", `{"name":"pi-mcp-extension","pi":{"extensions":["./missing.ts"]}}`, "", piPlugins{}},
		{"builtin-disabled", `{"extensions":["-builtin:mcp"]}`, "npm/node_modules/pi-mcp-adapter", `{"name":"pi-mcp-adapter","version":"4.0.0"}`, "", piPlugins{noBuiltin: true}},
		{"both", `{"packages":["npm:pi-mcp-adapter","npm:pi-mcp-extension"]}`, "npm/node_modules/pi-mcp-adapter", `{"name":"pi-mcp-adapter","version":"3.1.0"}`, "", piPlugins{adapter: true, major: 3, ext: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := sandbox(t)
			d := filepath.Join(h, ".pi", "agent")
			write(t, filepath.Join(d, "settings.json"), tc.settings)
			write(t, filepath.Join(d, tc.dir, "package.json"), tc.pkg)
			if tc.entry != "" {
				write(t, filepath.Join(d, tc.dir, tc.entry), "export default function extension() {}\n")
			}
			if got := piDetect(d); got != tc.want {
				t.Errorf("loaded MCP extensions: %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPiMCPUnusedAdapterRecoveryKeepsConflicts(t *testing.T) {
	h := sandbox(t)
	d := filepath.Join(h, ".pi", "agent")
	write(t, filepath.Join(d, "settings.json"), `{"packages":[]}`)
	write(t, filepath.Join(d, "npm/node_modules/pi-mcp-adapter/package.json"), `{"name":"pi-mcp-adapter","version":"4.0.0"}`)
	native, adapter := filepath.Join(d, "mcp.json"), filepath.Join(d, "mcp-adapter.json")
	nativeBefore := `{"autoEnableCodemode":false,"mcpServers":{"clash":{"command":"native"}}}`
	adapterBefore := `{"settings":{"toolPrefix":"short"},"mcpServers":{"clash":{"command":"adapter"},"recover":{"command":"recovered","idleTimeout":5}}}`
	write(t, native, nativeBefore)
	write(t, adapter, adapterBefore)
	f, via := piMCP(h, d, "1.0.0")
	if f.Path != native || f.Format != fmtPiNative || via != "" {
		t.Fatalf("unused adapter prevented native recovery: %+v via %q", f, via)
	}
	got, left := piServers(t, native), piServers(t, adapter)
	if got["clash"]["command"] != "native" || got["recover"]["command"] != "recovered" || got["recover"]["idleTimeout"] != 5.0 {
		t.Errorf("native recovery: %v", got)
	}
	if len(left) != 1 || left["clash"]["command"] != "adapter" || piDoc(t, adapter)["settings"] == nil {
		t.Errorf("conflict or adapter settings lost: %s", read(t, adapter))
	}
	if len(f.Extra) != 1 || f.Extra[0] != adapter {
		t.Errorf("unmerged servers no longer available for import: %v", f.Extra)
	}
	for name, before := range map[string]string{"mcp.json": nativeBefore, "mcp-adapter.json": adapterBefore} {
		bs, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "pi", name))
		if len(bs) != 1 || read(t, bs[0]) != before {
			t.Errorf("%s backup: %v", name, bs)
		}
	}
}

func TestPiMCPLegacyUnusedPackageKeepsExistingFile(t *testing.T) {
	h := sandbox(t)
	d := filepath.Join(h, ".pi", "agent")
	write(t, filepath.Join(d, "settings.json"), `{"packages":[]}`)
	write(t, filepath.Join(d, "npm/node_modules/pi-mcp-adapter/package.json"), `{"name":"pi-mcp-adapter","version":"4.0.0"}`)
	native, adapter := filepath.Join(d, "mcp.json"), filepath.Join(d, "mcp-adapter.json")
	mine := `{"mcpServers":{"mine":{"command":"x"}}}`
	write(t, native, mine)
	f, via := piMCP(h, d, "0.98.9")
	if f.Path != native || f.Format != fmtPi || via != "pi-mcp-adapter" || read(t, native) != mine || exists(adapter) {
		t.Errorf("Pi before native MCP must retain the existing file: %+v via %q", f, via)
	}
}

package library

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestPiMCPDisabledExtensionsKeepNative(t *testing.T) {
	for _, name := range []string{"pi-mcp-adapter", "pi-mcp-extension"} {
		for _, tc := range []struct {
			name, settings, location string
		}{
			{"no-autoload", `{"packages":[{"source":"npm:NAME","autoload":false}]}`, "npm/node_modules/NAME"},
			{"no-autoload-empty", `{"packages":[{"source":"npm:NAME","autoload":false,"extensions":[]}]}`, "npm/node_modules/NAME"},
			{"exclude-only", `{"extensions":["!extensions/NAME"]}`, "npm/node_modules/NAME"},
			{"include-only", `{"extensions":["+extensions/NAME"]}`, "npm/node_modules/NAME"},
			{"disable-only", `{"extensions":["-extensions/NAME"]}`, "npm/node_modules/NAME"},
			{"auto-excluded", `{"extensions":["!extensions/NAME/**"]}`, "extensions/NAME"},
			{"auto-disabled", `{"extensions":["-extensions/NAME/index.ts"]}`, "extensions/NAME"},
			{"explicit-excluded", `{"extensions":["./NAME","!NAME/**"]}`, "NAME"},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				h := sandbox(t)
				piAtVersion(t, "1.1.0")
				d := filepath.Join(h, ".pi", "agent")
				settings := strings.ReplaceAll(tc.settings, "NAME", name)
				location := strings.ReplaceAll(tc.location, "NAME", name)
				write(t, filepath.Join(d, "settings.json"), settings)
				write(t, filepath.Join(d, location, "package.json"), `{"name":"`+name+`","version":"4.0.0","pi":{"extensions":["./index.ts"]}}`)
				write(t, filepath.Join(d, location, "index.ts"), "export default function extension() {}\n")
				native, adapter := filepath.Join(d, "mcp.json"), filepath.Join(d, "mcp-adapter.json")
				mine := "{\n  // user-owned native settings\n  \"mcpServers\": {\"mine\": {\"command\": \"example\", \"enabled\": false}},\n  \"autoEnableCodemode\": false\n}\n"
				write(t, native, mine)
				if p := piDetect(d); p != (piPlugins{}) {
					t.Errorf("disabled extension detected as loaded: %+v", p)
				}
				tg := targetByID("pi")
				if tg.MCP.Path != native || tg.MCP.Format != fmtPiNative || tg.MCPVia != "" {
					t.Fatalf("disabled extension must keep native mcp.json: %+v via %q", tg.MCP, tg.MCPVia)
				}
				if read(t, native) != mine || exists(adapter) {
					t.Fatal("disabled extension moved the user's native config")
				}
				ok(t)(SaveServer("", Server{Name: "library", Transport: "http", URL: "https://example.com/mcp", Agents: []string{"pi"}}))
				got := piServers(t, native)
				if got["mine"]["enabled"] != false || got["library"]["url"] != "https://example.com/mcp" || got["library"]["httpTransport"] != nil || piDoc(t, native)["autoEnableCodemode"] != false || exists(adapter) {
					t.Fatal("sync did not preserve native MCP servers and settings")
				}
			})
		}
	}
}

func TestPiDetectExtensionOverrides(t *testing.T) {
	for _, tc := range []struct {
		name     string
		patterns []string
		want     bool
	}{
		{"default", nil, true},
		{"exclude-entry", []string{"!extensions/pi-mcp-adapter/src/index.ts"}, false},
		{"exclude-basename", []string{"!index.ts"}, false},
		{"exclude-globstar", []string{"!**/pi-mcp-adapter/**"}, false},
		{"exclude-wildcard", []string{"!extensions/pi-mcp-*/src/index.?s"}, false},
		{"exclude-character-class", []string{"!extensions/pi-mcp-adapter/src/[!a]*.ts"}, false},
		{"include-overrides-exclude", []string{"+extensions/pi-mcp-adapter/src/index.ts", "!**/pi-mcp-adapter/**"}, true},
		{"disable-overrides-include", []string{"-extensions/pi-mcp-adapter/src/index.ts", "+extensions/pi-mcp-adapter/src/index.ts"}, false},
		{"include-is-exact", []string{"!**/pi-mcp-adapter/**", "+extensions/pi-mcp-adapter/**"}, false},
		{"disable-is-exact", []string{"-extensions/pi-mcp-adapter/**"}, true},
		{"disable-relative", []string{"-./extensions/pi-mcp-adapter/src/index.ts"}, false},
		{"exclude-directory-is-not-entry", []string{"!extensions/pi-mcp-adapter"}, true},
		{"disable-basename-is-not-path", []string{"-index.ts"}, true},
		{"unrelated", []string{"!extensions/pi-mcp-extension/**"}, true},
		{"disable-absolute", []string{"-$ENTRY"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := sandbox(t)
			d := filepath.Join(h, ".pi", "agent")
			dir := filepath.Join(d, "extensions", "pi-mcp-adapter")
			entry := filepath.Join(dir, "src", "index.ts")
			patterns := make([]string, len(tc.patterns))
			for i, p := range tc.patterns {
				patterns[i] = strings.ReplaceAll(p, "$ENTRY", entry)
			}
			raw, err := json.Marshal(map[string]any{"extensions": patterns})
			if err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(d, "settings.json"), string(raw))
			write(t, filepath.Join(dir, "package.json"), `{"name":"pi-mcp-adapter","version":"3.1.0","pi":{"extensions":["./src/index.ts"]}}`)
			write(t, entry, "export default function extension() {}\n")
			got := piDetect(d)
			if got.adapter != tc.want || got.ext || got.noBuiltin || (got.major != 0 && !tc.want) {
				t.Errorf("loaded MCP extensions: %+v, want adapter=%v", got, tc.want)
			}
		})
	}
}

func TestPiDetectOverridesUseManifestEntries(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, patterns string
		want                     bool
	}{
		{"disabled-manifest-no-fallback", `["./src/adapter.ts"]`, `["!**/src/**"]`, false},
		{"one-manifest-entry-enabled", `["./src/adapter.ts","./index.ts"]`, `["!**/src/**"]`, true},
		{"missing-manifest-falls-back", `["./missing.ts"]`, `["!**/src/**"]`, true},
		{"index-ts-disabled-no-js-fallback", `[]`, `["-extensions/pi-mcp-adapter/index.ts"]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := sandbox(t)
			d := filepath.Join(h, ".pi", "agent")
			dir := filepath.Join(d, "extensions", "pi-mcp-adapter")
			write(t, filepath.Join(d, "settings.json"), `{"extensions":`+tc.patterns+`}`)
			write(t, filepath.Join(dir, "package.json"), `{"name":"pi-mcp-adapter","version":"3.1.0","pi":{"extensions":`+tc.manifest+`}}`)
			for _, entry := range []string{"src/adapter.ts", "index.ts", "index.js"} {
				write(t, filepath.Join(dir, entry), "export default function extension() {}\n")
			}
			if got := piDetect(d); got.adapter != tc.want {
				t.Errorf("loaded MCP extensions: %+v, want adapter=%v", got, tc.want)
			}
		})
	}
}

func TestPiDetectBuiltinOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, settings string
		want           bool
	}{
		{"empty", `{"extensions":["!","+","-"]}`, false},
		{"exclude", `{"extensions":["!builtin:mcp"]}`, true},
		{"restore", `{"extensions":["+builtin:mcp","!builtin:*"]}`, false},
		{"disable-wins", `{"extensions":["-builtin:mcp","+builtin:mcp"]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := sandbox(t)
			d := filepath.Join(h, ".pi", "agent")
			write(t, filepath.Join(d, "settings.json"), tc.settings)
			if p := piDetect(d); p != (piPlugins{noBuiltin: tc.want}) {
				t.Errorf("builtin MCP override: %+v, want noBuiltin=%v", p, tc.want)
			}
		})
	}
}

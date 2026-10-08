package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/testenv"
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

// fakePi puts a pi on PATH that says it is version out: what magpie asks
// to tell Pi's own MCP (0.99) from before it. Each is a new binary (its
// size changes with out), so magpie asks it again.
func fakePi(t *testing.T, bin, out string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script for pi")
	}
	p := filepath.Join(bin, "pi")
	testenv.Program(t, p, "#!/bin/sh\necho '"+out+"'\n")
	t.Setenv("PATH", bin)
}

// Pi 0.99 reads mcp.json itself (extensions/mcp/config.ts): with no MCP
// extension installed, the servers go there in its shape, those magpie put
// in mcp-adapter.json for pi-mcp-adapter 3 are moved over, and SSE, which
// it can't reach, is refused.
func TestPiMCPNative(t *testing.T) {
	h := sandbox(t)
	d := filepath.Join(h, ".pi/agent")
	native, adapter := filepath.Join(d, "mcp.json"), filepath.Join(d, "mcp-adapter.json")
	bin := filepath.Join(h, "bin")

	// 0.98: pi-mcp-adapter's file, as before
	fakePi(t, bin, "0.98.2")
	tg := targetByID("pi")
	if tg.MCP.Path != adapter || tg.MCP.Format != fmtPi || tg.MCPVia != "pi-mcp-adapter" {
		t.Fatalf("0.98: %+v via %q", tg.MCP, tg.MCPVia)
	}
	ok(t)(SaveServer("", Server{Name: "docs", Transport: "http", URL: "https://example.com/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${DOCS_TOKEN}"}, Agents: []string{"pi"}}))
	ok(t)(SaveServer("", Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "fs"}, Agents: []string{"pi"}}))
	if s := piServers(t, adapter); s["docs"]["httpTransport"] != "streamable-http" || s["fs"]["command"] != "npx" {
		t.Fatalf("mcp-adapter.json: %v", s)
	}
	// the user's own, and a field of theirs on magpie's; the adapter's settings
	edit := piDoc(t, adapter)
	servers := edit["mcpServers"].(map[string]any)
	servers["theirs"] = map[string]any{"command": "t", "lifecycle": "eager"}
	servers["fs"].(map[string]any)["idleTimeout"] = 5
	edit["settings"] = map[string]any{"toolPrefix": "short"}
	b, _ := json.Marshal(edit)
	write(t, adapter, string(b))
	// and one already in mcp.json, differently: it stays as it is
	write(t, native, `{"autoEnableCodemode": false, "mcpServers": {"theirs": {"command": "mine"}}}`)

	backups := func() int {
		bs, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "pi", "mcp-adapter.json"))
		return len(bs)
	}
	before := backups()

	// 0.99: Pi's own mcp.json
	fakePi(t, bin, "pi 0.99.0")
	tg = targetByID("pi")
	if tg.MCP.Path != native || tg.MCP.Format != fmtPiNative || tg.MCPVia != "" {
		t.Fatalf("0.99: %+v via %q", tg.MCP, tg.MCPVia)
	}
	got := piServers(t, native)
	if got["docs"]["url"] != "https://example.com/mcp" || got["fs"]["command"] != "npx" || got["fs"]["idleTimeout"] != 5.0 {
		t.Errorf("not moved: %v", got)
	}
	if got["theirs"]["command"] != "mine" || piDoc(t, native)["autoEnableCodemode"] != false {
		t.Errorf("mcp.json's own changed: %s", read(t, native))
	}
	// what differs, and the adapter's settings, stay; still found
	left := piDoc(t, adapter)
	if s := piServers(t, adapter); len(s) != 1 || s["theirs"]["command"] != "t" || left["settings"] == nil {
		t.Errorf("left in mcp-adapter.json: %s", read(t, adapter))
	}
	if len(tg.MCP.Extra) != 1 || tg.MCP.Extra[0] != adapter {
		t.Errorf("extra: %v", tg.MCP.Extra)
	}
	if n := backups(); n != before+1 {
		t.Errorf("backups of mcp-adapter.json: %d, %d before", n, before)
	}
	have, err := tg.MCP.read()
	if err != nil || have["docs"] == nil || have["docs"].Transport != "http" || have["theirs"].Command != "mine" {
		t.Errorf("read: %v %v", have, err)
	}

	// written again, in Pi's shape: no adapter keys, the user's kept
	ok(t)(Sync())
	ok(t)(SaveServer("docs", Server{Name: "docs", Transport: "http", URL: "https://example.com/v2/mcp", Agents: []string{"pi"}}))
	ok(t)(SaveServer("fs", Server{Name: "fs", Transport: "stdio", Command: "uvx", Env: map[string]string{"K": "${K}"}, Agents: []string{"pi"}}))
	got = piServers(t, native)
	docs, fs := got["docs"], got["fs"]
	if docs["url"] != "https://example.com/v2/mcp" || docs["httpTransport"] != nil || docs["transport"] != nil || docs["type"] != nil {
		t.Errorf("docs: %v", docs)
	}
	if fs["command"] != "uvx" || fs["env"].(map[string]any)["K"] != "${K}" || fs["idleTimeout"] != 5.0 {
		t.Errorf("fs: %v", fs)
	}
	if piServers(t, adapter)["docs"] != nil {
		t.Error("docs written to mcp-adapter.json")
	}
	// SSE is refused: Pi's own has none
	if err := tg.MCP.supports(&Server{Transport: "sse"}); err != errNoSSE {
		t.Errorf("sse: %v", err)
	}
	ok(t)(RemoveServer("fs"))
	if piServers(t, native)["fs"] != nil {
		t.Error("fs left behind")
	}

	// pi-mcp-adapter still installed stands in for Pi's own: its file
	write(t, filepath.Join(d, "settings.json"), `{"packages": ["npm:pi-mcp-adapter"]}`)
	if tg := targetByID("pi"); tg.MCP.Path != adapter || tg.MCPVia != "pi-mcp-adapter" {
		t.Errorf("0.99 with the adapter: %+v via %q", tg.MCP, tg.MCPVia)
	}
	// and in Pi's extensions folder, unlisted
	write(t, filepath.Join(d, "settings.json"), `{}`)
	write(t, filepath.Join(d, "extensions/pi-mcp-adapter/package.json"), `{"name": "pi-mcp-adapter", "version": "3.1.0"}`)
	if tg := targetByID("pi"); tg.MCP.Path != adapter {
		t.Errorf("0.99 with the adapter in extensions: %s", tg.MCP.Path)
	}
	os.RemoveAll(filepath.Join(d, "extensions"))
	// Pi's own turned off
	write(t, filepath.Join(d, "settings.json"), `{"extensions": ["-builtin:mcp"]}`)
	if tg := targetByID("pi"); tg.MCP.Format == fmtPiNative {
		t.Errorf("-builtin:mcp: %+v", tg.MCP)
	}
	write(t, filepath.Join(d, "settings.json"), `{}`)
	fakePi(t, bin, "pi version 1.2.0")
	if tg := targetByID("pi"); tg.MCP.Path != native || tg.MCP.Format != fmtPiNative {
		t.Errorf("1.2: %+v", tg.MCP)
	}
}

func TestPiNativeVersions(t *testing.T) {
	for v, want := range map[string]bool{"": false, "0.98.9": false, "0.99.0-rc.1": false, "0.99.0": true, "0.99.1": true, "1.0.0": true} {
		if piNative(v) != want {
			t.Errorf("%q: %v", v, !want)
		}
	}
}

// heliar-k on #1097: the adapter found, mcp.json was moved whole to
// mcp-adapter.json with no backup, the servers the user added with
// `pi mcp add` leaving the file Pi reads. Whatever moves it, the user's
// mcp.json is copied aside first, byte for byte, and its servers survive.
func TestPiMCPWholeMoveKeepsABackup(t *testing.T) {
	h := sandbox(t)
	d := filepath.Join(h, ".pi/agent")
	write(t, filepath.Join(d, "settings.json"), `{ "quietStartup": false, "packages": [] }`)
	write(t, filepath.Join(d, "npm/node_modules/pi-mcp-adapter/package.json"), `{"name": "pi-mcp-adapter", "version": "4.0.0"}`)
	old, adapter := filepath.Join(d, "mcp.json"), filepath.Join(d, "mcp-adapter.json")
	mine := `{ "mcpServers": { "demo": { "url": "https://mcp.exa.ai/mcp" } } }
`
	write(t, old, mine)
	targetByID("pi")
	bs, _ := filepath.Glob(filepath.Join(BackupDir(), "*", "pi", "mcp.json"))
	if len(bs) != 1 {
		t.Fatalf("backups of mcp.json: %v", bs)
	}
	if got := read(t, bs[0]); got != mine {
		t.Errorf("backup: %q, want the user's %q", got, mine)
	}
	if exists(old) {
		if piServers(t, old)["demo"] == nil {
			t.Errorf("mcp.json lost demo: %s", read(t, old))
		}
	} else if piServers(t, adapter)["demo"] == nil {
		t.Errorf("demo neither in mcp.json nor in mcp-adapter.json: %s", read(t, adapter))
	}
}

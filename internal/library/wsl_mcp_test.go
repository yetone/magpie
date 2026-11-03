package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// A Pi in a WSL distro is offered MCP servers, written where the Pi there
// reads them: as on this machine, by the version the distro's pi gave its
// probe and the extensions its settings.json and the distro's npm folders
// have (leokon on Discord: only the Windows Pi was listed under MCP).
func TestWSLPiMCP(t *testing.T) {
	sandbox(t)
	root := map[string]string{"Ubuntu": t.TempDir(), "Debian": t.TempDir(), "Arch": t.TempDir()}
	homeOf := func(d string) string { return filepath.Join(root[d], "home", "me") }
	// Ubuntu: Pi 1.2.0, MCP of its own; Debian: Pi 0.98, before it, with
	// no extension; Arch: Pi 0.98 with pi-mcp-adapter 2, from the
	// distro's npm global folder (settings.json doesn't pin it)
	write(t, filepath.Join(homeOf("Ubuntu"), ".pi", "agent", "settings.json"), "{}\n")
	write(t, filepath.Join(homeOf("Debian"), ".pi", "agent", "settings.json"), "{}\n")
	write(t, filepath.Join(homeOf("Arch"), ".pi", "agent", "settings.json"), `{"packages": ["npm:pi-mcp-adapter"]}`+"\n")
	write(t, filepath.Join(homeOf("Arch"), ".npm-global", "lib", "node_modules", "pi-mcp-adapter", "package.json"), `{"name": "pi-mcp-adapter", "version": "2.4.0"}`)
	t.Cleanup(agent.FakeWSL(map[string]string{
		"Ubuntu": "home:/home/me\ndir:.pi\nbin:pi /usr/local/bin/pi\nver:pi 1.2.0\n",
		"Debian": "home:/home/me\ndir:.pi\nbin:pi /usr/local/bin/pi\nver:pi 0.98.0\n",
		"Arch":   "home:/home/me\ndir:.pi\nbin:pi /usr/local/bin/pi\nver:pi 0.98.0\n",
	}, root))

	want := map[string]struct {
		file   string
		format mcpFormat
		via    string
	}{
		"Ubuntu": {".pi/agent/mcp.json", fmtPiNative, ""},
		"Debian": {".pi/agent/mcp-adapter.json", fmtPi, "pi-mcp-adapter"},
		"Arch":   {".pi/agent/mcp.json", fmtPi, "pi-mcp-adapter"},
	}
	var all []string
	for d, w := range want {
		id := "pi@wsl:" + d
		all = append(all, id)
		tg := targetByID(id)
		if tg == nil {
			t.Fatalf("%s isn't a target: %v", id, ids(Targets()))
		}
		if tg.MCP == nil || tg.MCP.Path != filepath.Join(homeOf(d), w.file) || tg.MCP.Format != w.format || tg.MCPVia != w.via ||
			!tg.MCP.WSL || tg.MCP.Distro != d {
			t.Errorf("%s: mcp %+v via %q, want %+v", id, tg.MCP, tg.MCPVia, w)
		}
	}
	// the page lists it under MCP: its view names the file
	view, err := Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range view.Agents {
		if strings.HasPrefix(v.ID, "pi@wsl:") && v.MCP == "" {
			t.Errorf("%s has no MCP file in the page's view", v.ID)
		}
	}

	ok(t)(SaveServer("", Server{Name: "fetch", Transport: "stdio", Command: "uvx", Args: []string{"mcp-server-fetch"}, Agents: all}))
	for d, w := range want {
		if s := read(t, filepath.Join(homeOf(d), w.file)); !strings.Contains(s, `"fetch"`) || !strings.Contains(s, `"uvx"`) {
			t.Errorf("pi@wsl:%s %s:\n%s", d, w.file, s)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(home(), ".pi", "agent", "mcp.json")); strings.Contains(string(b), "fetch") {
		t.Error("this machine's Pi got the server")
	}
	// a Windows program is nothing it can start
	res, err := SaveServer("", Server{Name: "win", Transport: "stdio", Command: `C:\Program Files\nodejs\npx.cmd`, Args: []string{"x"}, Agents: []string{"pi@wsl:Ubuntu"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 1 || res.Problems[0].Agent != "pi@wsl:Ubuntu" {
		t.Errorf("problems: %+v", res.Problems)
	}
	if strings.Contains(read(t, filepath.Join(homeOf("Ubuntu"), ".pi/agent/mcp.json")), `"win"`) {
		t.Error("the Windows program was given to pi@wsl")
	}
}

// Every other agent this machine's MCP servers reach that magpie finds in a
// WSL distro is given them there too, in its own file under the distro's
// home and never a Windows program: Gemini CLI, Antigravity CLI, Crush,
// Cline, CodeBuddy Code, Kimi Code and Goose (each was set up in WSL but
// left out of the library).
func TestWSLMCPSiblings(t *testing.T) {
	sandbox(t)
	root := t.TempDir()
	h := filepath.Join(root, "home", "me")
	for _, f := range []string{
		".gemini/settings.json", ".gemini/antigravity-cli/settings.json", ".config/crush/crush.json",
		".cline/data/settings/providers.json", ".codebuddy/models.json", ".kimi-code/config.toml", ".config/goose/config.yaml",
	} {
		content := "{}\n"
		if strings.HasSuffix(f, ".toml") || strings.HasSuffix(f, ".yaml") {
			content = ""
		}
		write(t, filepath.Join(h, f), content)
	}
	t.Cleanup(agent.FakeWSL(map[string]string{
		"Ubuntu": "home:/home/me\nbin:gemini /usr/bin/gemini\ndir:.gemini/antigravity-cli\nbin:agy /usr/bin/agy\n" +
			"dir:.config/crush\nbin:crush /usr/bin/crush\ndir:.cline\nbin:cline /usr/bin/cline\n" +
			"dir:.codebuddy\nbin:codebuddy /usr/bin/codebuddy\ndir:.kimi-code\nbin:kimi /usr/bin/kimi\ndir:.config/goose\n",
	}, map[string]string{"Ubuntu": root}))

	want := map[string]string{
		"gemini@wsl:Ubuntu":    ".gemini/settings.json",
		"agy@wsl:Ubuntu":       ".gemini/config/mcp_config.json",
		"crush@wsl:Ubuntu":     ".config/crush/crush.json",
		"cline@wsl:Ubuntu":     ".cline/data/settings/cline_mcp_settings.json",
		"codebuddy@wsl:Ubuntu": ".codebuddy/.mcp.json",
		"kimi@wsl:Ubuntu":      ".kimi-code/mcp.json",
		"goose@wsl:Ubuntu":     ".config/goose/config.yaml",
	}
	var all []string
	for id, f := range want {
		all = append(all, id)
		tg := targetByID(id)
		if tg == nil {
			t.Errorf("%s isn't a target: %v", id, ids(Targets()))
			continue
		}
		if tg.MCP == nil || tg.MCP.Path != filepath.Join(h, f) || !tg.MCP.WSL || tg.MCP.Distro != "Ubuntu" || tg.Copy || tg.Skills != "" && tg.Store == "" {
			t.Errorf("%s: mcp %+v", id, tg.MCP)
		}
		// nothing it is given is this machine's
		for _, p := range []string{tg.Instructions, tg.Skills} {
			if p != "" && !strings.HasPrefix(p, h) {
				t.Errorf("%s: %s isn't in the distro", id, p)
			}
		}
	}
	if tg := targetByID("crush@wsl:Ubuntu"); tg != nil && (len(tg.SkillsAlso) != 1 || tg.SkillsAlso[0] != "claude@wsl:Ubuntu") {
		t.Errorf("crush@wsl reads skills of %v", tg.SkillsAlso)
	}

	ok(t)(SaveServer("", Server{Name: "fetch", Transport: "stdio", Command: "uvx", Args: []string{"mcp-server-fetch"}, Agents: all}))
	for id, f := range want {
		if s := read(t, filepath.Join(h, f)); !strings.Contains(s, "fetch") || !strings.Contains(s, "uvx") {
			t.Errorf("%s %s:\n%s", id, f, s)
		}
	}
	res, err := SaveServer("", Server{Name: "win", Transport: "stdio", Command: `C:\Program Files\nodejs\npx.cmd`, Args: []string{"x"}, Agents: all})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != len(all) {
		t.Errorf("problems: %+v", res.Problems)
	}
	for id, f := range want {
		if strings.Contains(read(t, filepath.Join(h, f)), "npx.cmd") {
			t.Errorf("the Windows program was given to %s", id)
		}
	}
}

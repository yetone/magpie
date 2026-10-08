package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// EUPH on Discord: MCP servers couldn't be given to agents that have a
// user-wide MCP file of their own. Each is written as the agent's own
// `mcp add` writes it (or its loader reads it), read back, found when the
// user put it there, and taken out again leaving the user's servers and
// keys: omp's mcp.json (type), Kimi Code's mcp.json old and new
// (transport), Devin's mcp_config.json (transport on each), Grok Build's
// config.toml [mcp_servers.<name>] (no SSE).
func TestAgentMCPFiles(t *testing.T) {
	type entry = map[string]any
	for _, c := range []struct {
		id   string
		file string // under the sandbox home
		env  map[string]string
		// the user's own server and file, as the agent writes them
		user string
		toml bool
		// what magpie writes for each server, as the agent's add does
		fs, web, sse entry
		noSSE        bool
		// pre makes the agent there, beyond its MCP file
		pre func(t *testing.T, h string)
	}{
		{
			id: "omp", file: ".omp/agent/mcp.json",
			user: `{"$schema": "x", "mcpServers": {"mine": {"command": "uvx", "args": ["mine"], "timeout": 30}}}`,
			fs:   entry{"command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}},
			web:  entry{"type": "http", "url": "https://example.com/mcp", "headers": entry{"Authorization": "Bearer x"}},
			sse:  entry{"type": "sse", "url": "https://example.com/sse"},
		},
		{
			// the old Python kimi-cli, ~/.kimi
			id: "kimi", file: ".kimi/mcp.json",
			user: `{"mcpServers": {"mine": {"command": "uvx", "args": ["mine"], "timeout": 30}}}`,
			fs:   entry{"command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}},
			web:  entry{"url": "https://example.com/mcp", "transport": "http", "headers": entry{"Authorization": "Bearer x"}},
			sse:  entry{"url": "https://example.com/sse", "transport": "sse"},
		},
		{
			// the new Kimi Code, $KIMI_CODE_HOME
			id: "kimi", file: "kc/mcp.json", env: map[string]string{"KIMI_CODE_HOME": "kc"},
			user: `{"mcpServers": {"mine": {"command": "uvx", "args": ["mine"], "enabled": false}}}`,
			fs:   entry{"command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}},
			web:  entry{"url": "https://example.com/mcp", "transport": "http", "headers": entry{"Authorization": "Bearer x"}},
			sse:  entry{"url": "https://example.com/sse", "transport": "sse"},
		},
		{
			id: "devin", file: ".config/devin/mcp_config.json",
			user: `{"mcpServers": {"mine": {"command": "uvx", "args": ["mine"], "transport": "stdio", "disabled": true}}}`,
			fs:   entry{"command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}, "transport": "stdio"},
			web:  entry{"url": "https://example.com/mcp", "transport": "http", "headers": entry{"Authorization": "Bearer x"}},
			sse:  entry{"url": "https://example.com/sse", "transport": "sse"},
		},
		{
			id: "droid", file: ".factory/mcp.json",
			user: `{"mcpServers": {"mine": {"type": "stdio", "command": "uvx", "args": ["mine"], "disabled": true}}}`,
			fs:   entry{"type": "stdio", "command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}},
			web:  entry{"type": "http", "url": "https://example.com/mcp", "headers": entry{"Authorization": "Bearer x"}},
			sse:  entry{"type": "sse", "url": "https://example.com/sse"},
		},
		{
			// Qoder's settings.json, which magpie's provider is in too
			id: "qoder", file: ".qoder/settings.json",
			user: `{"model": {"name": "auto"}, "mcpServers": {"mine": {"command": "uvx", "args": ["mine"], "trust": true}}}`,
			fs:   entry{"command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}},
			web:  entry{"type": "http", "url": "https://example.com/mcp", "headers": entry{"Authorization": "Bearer x"}},
			sse:  entry{"type": "sse", "url": "https://example.com/sse"},
		},
		{
			id: "qoder-cn", file: ".qoder-cn/settings.json",
			user: `{"mcpServers": {"mine": {"command": "uvx", "args": ["mine"]}}}`,
			fs:   entry{"command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}},
			web:  entry{"type": "http", "url": "https://example.com/mcp", "headers": entry{"Authorization": "Bearer x"}},
			sse:  entry{"type": "sse", "url": "https://example.com/sse"},
		},
		{
			// as `cline mcp add` writes them, the user's in the flat shape
			id: "cline", file: ".cline/data/settings/cline_mcp_settings.json",
			user: `{"mcpServers": {"mine": {"command": "uvx", "args": ["mine"], "disabled": true, "timeout": 30}}}`,
			fs:   entry{"transport": entry{"type": "stdio", "command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}}},
			web:  entry{"transport": entry{"type": "streamableHttp", "url": "https://example.com/mcp", "headers": entry{"Authorization": "Bearer x"}}},
			sse:  entry{"transport": entry{"type": "sse", "url": "https://example.com/sse"}},
		},
		{
			id: "commandcode", file: ".commandcode/mcp.json",
			user:  `{"mcpServers": {"mine": {"transport": "stdio", "enabled": false, "command": "uvx", "args": ["mine"]}}}`,
			fs:    entry{"transport": "stdio", "enabled": true, "command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}},
			web:   entry{"transport": "http", "enabled": true, "url": "https://example.com/mcp", "headers": entry{"Authorization": "Bearer x"}},
			noSSE: true,
		},
		{
			// as WorkBuddy's own add writes them (connectCustomMcpServer)
			id: "workbuddy", file: ".workbuddy/mcp.json",
			user: `{"mcpServers": {"mine": {"command": "uvx", "args": ["mine"], "disabled": false}}}`,
			fs:   entry{"type": "stdio", "command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}},
			web:  entry{"type": "http", "url": "https://example.com/mcp", "headers": entry{"Authorization": "Bearer x"}},
			sse:  entry{"type": "sse", "url": "https://example.com/sse"},
		},
		{
			// CodeBuddy Code reads the first of .mcp.json, mcp.json and
			// ~/.codebuddy.json that is there
			id: "codebuddy", file: ".codebuddy/.mcp.json",
			user: `{"mcpServers": {"mine": {"type": "stdio", "command": "uvx", "args": ["mine"]}}, "disabledMcpServers": []}`,
			fs:   entry{"type": "stdio", "command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}},
			web:  entry{"type": "http", "url": "https://example.com/mcp", "headers": entry{"Authorization": "Bearer x"}},
			sse:  entry{"type": "sse", "url": "https://example.com/sse"},
		},
		{
			id: "codebuddy", file: ".codebuddy/mcp.json",
			user: `{"mcpServers": {"mine": {"type": "stdio", "command": "uvx", "args": ["mine"], "timeout": 30}}}`,
			fs:   entry{"type": "stdio", "command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}},
			web:  entry{"type": "http", "url": "https://example.com/mcp", "headers": entry{"Authorization": "Bearer x"}},
			sse:  entry{"type": "sse", "url": "https://example.com/sse"},
		},
		{
			id: "codebuddy", file: "cb/.codebuddy.json", env: map[string]string{"CODEBUDDY_CONFIG_DIR": "cb"},
			user: `{"$schema": "x", "mcpServers": {"mine": {"type": "stdio", "command": "uvx", "args": ["mine"]}}}`,
			fs:   entry{"type": "stdio", "command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}},
			web:  entry{"type": "http", "url": "https://example.com/mcp", "headers": entry{"Authorization": "Bearer x"}},
			sse:  entry{"type": "sse", "url": "https://example.com/sse"},
		},
		{
			// as Alma's own add writes them, the server it adds itself kept
			// (#1292); Alma is there once it has its data folder
			id: "alma", file: ".config/alma/mcp.json",
			user: `{"mcpServers": {"mine": {"command": "uvx", "args": ["mine"], "env": {"ELECTRON_RUN_AS_NODE": "1"}}}}`,
			fs:   entry{"command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}},
			web:  entry{"url": "https://example.com/mcp", "headers": entry{"Authorization": "Bearer x"}},
			sse:  entry{"url": "https://example.com/sse", "transport": "sse"},
			pre: func(t *testing.T, h string) {
				t.Setenv("APPDATA", filepath.Join(h, "AppData", "Roaming"))
				cfg, err := os.UserConfigDir()
				if err != nil {
					t.Fatal(err)
				}
				os.MkdirAll(filepath.Join(cfg, "alma"), 0o755)
			},
		},
		{
			id: "grok", file: ".grok/config.toml", toml: true,
			user:  "# mine\n[models]\ndefault = \"grok-4\"\n\n[mcp_servers.mine]\ncommand = \"uvx\"\nargs = [\"mine\"]\nenabled = true\n\n[mcp_servers.mine.env]\nA = \"b\"\n",
			fs:    entry{"command": "npx", "args": []any{"-y", "@mcp/fs"}, "env": entry{"K": "V"}},
			web:   entry{"url": "https://example.com/mcp", "headers": entry{"Authorization": "Bearer x"}},
			noSSE: true,
		},
	} {
		t.Run(c.id+" "+c.file, func(t *testing.T) {
			h := sandbox(t)
			for k, v := range c.env {
				t.Setenv(k, filepath.Join(h, v))
			}
			if c.pre != nil {
				c.pre(t, h)
			}
			p := filepath.Join(h, c.file)
			write(t, p, c.user)
			parse := func() map[string]entry {
				t.Helper()
				var doc map[string]any
				var err error
				if c.toml {
					err = toml.Unmarshal([]byte(read(t, p)), &doc)
				} else {
					err = json.Unmarshal([]byte(read(t, p)), &doc)
				}
				if err != nil {
					t.Fatalf("%v:\n%s", err, read(t, p))
				}
				key := "mcpServers"
				if c.toml {
					key = "mcp_servers"
				}
				out := map[string]entry{}
				all, _ := doc[key].(map[string]any)
				for k, v := range all {
					out[k], _ = v.(map[string]any)
				}
				return out
			}

			tg := targetByID(c.id)
			if tg == nil || tg.MCP == nil || tg.MCP.Path != p {
				t.Fatalf("%s has no MCP target at %s: %+v", c.id, p, tg)
			}
			if id, err := Takes(c.id, "mcp"); id != c.id || err != nil {
				t.Errorf("Takes: %q %v", id, err)
			}
			// the user's server is there to bring in
			l, err := load()
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, f := range foundServers(l) {
				found = found || f.Server.Name == "mine" && f.Server.Command == "uvx" && strings.Contains(strings.Join(f.Server.Agents, ","), c.id)
			}
			if !found {
				t.Errorf("the user's server isn't found: %+v", foundServers(l))
			}

			agents := []string{c.id}
			stdio := Server{Name: "fs", Transport: "stdio", Command: "npx", Args: []string{"-y", "@mcp/fs"}, Env: map[string]string{"K": "V"}, Agents: agents}
			web := Server{Name: "web", Transport: "http", URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer x"}, Agents: agents}
			sse := Server{Name: "old", Transport: "sse", URL: "https://example.com/sse", Agents: agents}
			ok(t)(SaveServer("", stdio))
			ok(t)(SaveServer("", web))
			res, err := SaveServer("", sse)
			if err != nil {
				t.Fatal(err)
			}
			if c.noSSE {
				if len(res.Problems) != 1 || res.Problems[0].Error != errNoSSE.Error() {
					t.Errorf("an SSE server was given to %s, which can't reach it: %+v", c.id, res.Problems)
				}
				ok(t)(RemoveServer("old"))
			} else if len(res.Problems) > 0 {
				t.Errorf("sse: %+v", res.Problems)
			}

			got := parse()
			for name, want := range map[string]entry{"fs": c.fs, "web": c.web, "old": c.sse} {
				g, _ := json.Marshal(got[name])
				w, _ := json.Marshal(want)
				if want == nil {
					w = []byte("null")
				}
				if string(g) != string(w) {
					t.Errorf("%s:\n got %s\nwant %s", name, g, w)
				}
			}
			if got["mine"]["command"] != "uvx" {
				t.Errorf("the user's server: %v", got["mine"])
			}
			back, err := tg.MCP.read()
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []Server{stdio, web, sse} {
				if want.Transport == "sse" && c.noSSE {
					continue
				}
				if s := back[want.Name]; s == nil || !s.same(&want) {
					t.Errorf("%s read back as %+v", want.Name, s)
				}
			}
			before := read(t, p)
			ok(t)(Sync())
			if read(t, p) != before {
				t.Errorf("a sync rewrote servers that are as the library has them:\n%s", read(t, p))
			}

			for _, s := range []string{"fs", "web", "old"} {
				if s != "old" || !c.noSSE {
					ok(t)(RemoveServer(s))
				}
			}
			got = parse()
			if len(got) != 1 || got["mine"] == nil {
				t.Errorf("after removing:\n%s", read(t, p))
			}
			for _, keep := range []string{`"$schema"`, "timeout", "disabled", "enabled", "# mine", `default = "grok-4"`, "A = "} {
				if strings.Contains(c.user, keep) && !strings.Contains(read(t, p), keep) {
					t.Errorf("lost %s:\n%s", keep, read(t, p))
				}
			}
		})
	}
}

// With none of its three files there, CodeBuddy Code's servers go where
// its own `mcp add -s user` puts them, .mcp.json in its folder; one there
// later than ~/.codebuddy.json is still read first (#1266).
func TestCodeBuddyMCPFile(t *testing.T) {
	h := sandbox(t)
	write(t, filepath.Join(h, ".codebuddy/models.json"), "")
	tg := targetByID("codebuddy")
	if tg == nil || tg.MCP == nil || tg.MCP.Path != filepath.Join(h, ".codebuddy/.mcp.json") {
		t.Fatalf("with no file: %+v", tg)
	}
	write(t, filepath.Join(h, ".codebuddy.json"), `{"mcpServers": {}}`)
	if p := targetByID("codebuddy").MCP.Path; p != filepath.Join(h, ".codebuddy.json") {
		t.Errorf("with only ~/.codebuddy.json: %s", p)
	}
	write(t, filepath.Join(h, ".codebuddy/.mcp.json"), `{"mcpServers": {}}`)
	if p := targetByID("codebuddy").MCP.Path; p != filepath.Join(h, ".codebuddy/.mcp.json") {
		t.Errorf("with both: %s", p)
	}
	if s := targetByID("codebuddy").Skills; s != filepath.Join(h, ".codebuddy/skills") {
		t.Errorf("skills: %s", s)
	}
}

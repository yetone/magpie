package library

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// fwxkogaia on X: Serena installed from the market showed "已退出 (2)".
// magpie ran `uvx serena-agent`, which prints its help and exits 2: the
// registry has uvx run it with start-mcp-server, among its
// runtimeArguments. Each entry is the registry's own, as it lists it; a
// package's executables (bins) are the market crawl's.
func TestRegistryLaunchLines(t *testing.T) {
	for _, c := range []struct {
		name, entry string
		want        string // command and arguments; "" when it isn't offered
	}{
		{"Serena: start-mcp-server among uvx's arguments, -p kept uvx's",
			`{"name": "io.github.oraios/serena", "packages": [{"registryType": "pypi", "identifier": "serena-agent", "runtimeHint": "uvx", "transport": {"type": "stdio"}, "runtimeArguments": [{"description": "Select the python version", "type": "named", "name": "-p", "value": "3.13"}, {"type": "positional", "value": "start-mcp-server"}, {"type": "positional", "value": "--project-from-cwd"}, {"description": "The name of a markdown file representing Serena's execution context", "default": "desktop-app", "type": "named", "name": "--context"}]}]}`,
			"uvx -p 3.13 serena-agent start-mcp-server --project-from-cwd --context desktop-app"},
		{"Docling: --from and the executable among uvx's",
			`{"name": "io.github.docling-project/docling-mcp", "packages": [{"registryType": "pypi", "identifier": "docling-mcp", "runtimeHint": "uvx", "transport": {"type": "stdio"}, "runtimeArguments": [{"type": "named", "name": "--from", "value": "docling-mcp==3.5.0"}, {"type": "positional", "value": "docling-mcp-server"}, {"type": "named", "name": "--transport", "value": "stdio"}]}]}`,
			"uvx --from docling-mcp==3.5.0 docling-mcp-server --transport stdio"},
		{"terravision: --from with no executable, the program's own after",
			`{"name": "io.github.patrickchugh/terravision", "packages": [{"registryType": "pypi", "identifier": "terravision", "runtimeHint": "uvx", "transport": {"type": "stdio"}, "runtimeArguments": [{"type": "named", "name": "--from", "value": "terravision[mcp]"}], "packageArguments": [{"type": "positional", "value": "mcp"}], "bins": ["terravision"]}]}`,
			"uvx --from terravision[mcp] terravision mcp"},
		{"visualtorch: --from among the program's arguments",
			`{"name": "io.github.willyfh/visualtorch", "packages": [{"registryType": "pypi", "identifier": "visualtorch", "runtimeHint": "uvx", "transport": {"type": "stdio"}, "packageArguments": [{"type": "positional", "value": "--from"}, {"type": "positional", "value": "visualtorch[mcp]"}, {"type": "positional", "value": "visualtorch-mcp"}], "bins": ["visualtorch-mcp"]}]}`,
			"uvx --from visualtorch[mcp] visualtorch-mcp"},
		{"basic-memory: the whole line among uvx's, the package in it",
			`{"name": "io.github.basicmachines-co/basic-memory", "packages": [{"registryType": "pypi", "identifier": "basic-memory", "runtimeHint": "uvx", "transport": {"type": "stdio"}, "runtimeArguments": [{"type": "named", "name": "--prerelease", "value": "allow"}, {"type": "positional", "value": "basic-memory"}, {"type": "positional", "value": "mcp"}]}]}`,
			"uvx --prerelease allow basic-memory mcp"},
		{"alfanous: python -m its module",
			`{"name": "io.github.Alfanous-team/alfanous", "packages": [{"registryType": "pypi", "identifier": "alfanous3-mcp", "transport": {"type": "stdio"}, "runtimeArguments": [{"type": "positional", "value": "-m"}, {"type": "positional", "value": "alfanous_mcp.mcp_server"}], "bins": ["alfanous-mcp"]}]}`,
			"uvx --from alfanous3-mcp python -m alfanous_mcp.mcp_server"},
		{"a PyPI package whose one executable has another name",
			`{"name": "io.github.vishalsachdev/canvas-mcp", "packages": [{"registryType": "pypi", "identifier": "canvas-mcp", "transport": {"type": "stdio"}, "bins": ["canvas-mcp-server"]}]}`,
			"uvx --from canvas-mcp canvas-mcp-server"},
		{"several executables, one for MCP",
			`{"name": "io.github.agent-next/polymarket-paper-trader", "packages": [{"registryType": "pypi", "identifier": "polymarket-paper-trader", "transport": {"type": "stdio"}, "bins": ["pm-trader", "pm-trader-mcp"]}]}`,
			"uvx --from polymarket-paper-trader pm-trader-mcp"},
		{"several executables, none standing out",
			`{"name": "io.github.flytohub/flyto-core", "packages": [{"registryType": "pypi", "identifier": "flyto-core", "transport": {"type": "stdio"}, "bins": ["flyto", "flyto-serve", "flyto-verification"]}]}`,
			""},
		{"a package PyPI doesn't have",
			`{"name": "io.github.feder-cr/aihawk", "packages": [{"registryType": "pypi", "identifier": "aihawk", "transport": {"type": "stdio"}, "bins": []}]}`,
			""},
		{"executables unknown (a search): as the registry says",
			`{"name": "io.github.x/y", "packages": [{"registryType": "pypi", "identifier": "y-mcp", "transport": {"type": "stdio"}}]}`,
			"uvx y-mcp"},
		{"Firebase: mcp among npx's arguments is the program's",
			`{"name": "io.github.firebase/firebase-mcp", "packages": [{"registryType": "npm", "identifier": "firebase-tools", "runtimeHint": "npx", "transport": {"type": "stdio"}, "runtimeArguments": [{"type": "positional", "value": "mcp"}], "bins": ["firebase"]}]}`,
			"npx -y firebase-tools mcp"},
		{"olcli: --package among npx's, the executable among the program's",
			`{"name": "io.github.aloth/olcli", "packages": [{"registryType": "npm", "identifier": "@aloth/olcli", "runtimeHint": "npx", "transport": {"type": "stdio"}, "runtimeArguments": [{"type": "positional", "value": "-y"}, {"type": "positional", "value": "--package"}], "packageArguments": [{"type": "positional", "value": "olcli-mcp"}]}]}`,
			"npx -y --package @aloth/olcli olcli-mcp"},
		{"pm-claude-skills: npx's whole line",
			`{"name": "io.github.mohitagw15856/pm-claude-skills", "packages": [{"registryType": "npm", "identifier": "pm-claude-skills", "runtimeHint": "npx", "transport": {"type": "stdio"}, "runtimeArguments": [{"type": "positional", "value": "-y"}, {"type": "positional", "value": "-p"}, {"type": "positional", "value": "pm-claude-skills"}, {"type": "positional", "value": "pm-claude-skills-mcp"}]}]}`,
			"npx -y --package pm-claude-skills pm-claude-skills-mcp"},
		{"an npm package with two executables, none its name",
			`{"name": "io.github.fkiene/llmtrim", "packages": [{"registryType": "npm", "identifier": "@llmtrim/cli", "transport": {"type": "stdio"}, "packageArguments": [{"type": "positional", "value": "mcp"}], "bins": ["llmtrim", "llmtrim-tray"]}]}`,
			"npx -y --package @llmtrim/cli llmtrim mcp"},
		{"an npm package's one executable: npx finds it",
			`{"name": "io.github.x/kaneo", "packages": [{"registryType": "npm", "identifier": "@kaneo/mcp", "transport": {"type": "stdio"}, "bins": ["kaneo-mcp"]}]}`,
			"npx -y @kaneo/mcp"},
		{"an npm package with no executable",
			`{"name": "io.github.enuno/unifi-mcp-server", "packages": [{"registryType": "npm", "identifier": "unifi-mcp-server", "transport": {"type": "stdio"}, "bins": []}]}`,
			""},
		{"MARM: docker's own, the entrypoint too, before the image",
			`{"name": "io.github.Lyellr88/marm-mcp-server", "packages": [{"registryType": "oci", "identifier": "lyellr88/marm-mcp-server:2.60.1", "runtimeHint": "docker", "transport": {"type": "stdio"}, "runtimeArguments": [{"type": "named", "name": "-i"}, {"type": "named", "name": "--rm"}, {"type": "named", "name": "--mount", "value": "type=volume,src=marm-data,dst=/home/marm/.marm"}, {"type": "named", "name": "-e", "value": "HOME=/home/marm"}, {"type": "named", "name": "--entrypoint", "value": "marm-mcp-stdio"}]}]}`,
			"docker run -i --rm --mount type=volume,src=marm-data,dst=/home/marm/.marm -e HOME=/home/marm --entrypoint marm-mcp-stdio lyellr88/marm-mcp-server:2.60.1"},
		{"a docker option magpie can't fill",
			`{"name": "io.github.kOlapsis/maintenant", "packages": [{"registryType": "oci", "identifier": "ghcr.io/kolapsis/maintenant:1.11.0", "transport": {"type": "stdio"}, "runtimeArguments": [{"type": "named", "name": "--volume", "value": "{data_volume}:/data", "isRequired": true}]}]}`,
			""},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, ok := fromRegistry(registryEntry(t, c.entry))
			got := ""
			if ok {
				got = strings.Join(append([]string{m.server.Command}, m.server.Args...), " ")
			}
			if got != c.want {
				t.Errorf("runs %q, want %q", got, c.want)
			}
			if ok && m.Runs != m.server.Command {
				t.Errorf("Runs %q, command %q", m.Runs, m.server.Command)
			}
		})
	}
}

// The market's own Serena, from the crawl shipped with magpie, starts the
// MCP server, and is the same server as the one it offered before.
func TestPopularSerenaStarts(t *testing.T) {
	i := slices.IndexFunc(popularServers(), func(m MarketServer) bool { return m.ID == "io.github.oraios/serena" })
	if i < 0 {
		t.Fatal("the market has no Serena")
	}
	s := popularServers()[i].server
	if s.Command != "uvx" || !slices.Contains(s.Args, "start-mcp-server") || !slices.Contains(s.Args, "serena-agent") {
		t.Fatalf("Serena runs %s %q", s.Command, s.Args)
	}
	if k := serverKey(&s); k != "uvx serena-agent" {
		t.Errorf("key %q: an installed one wouldn't be found, nor keep its icon", k)
	}
	if p := packageOf(&s); p != "serena-agent" {
		t.Errorf("package %q", p)
	}
}

// Serena installed before is given the line that starts it, in the library
// and the agents. A server of the user's own with another line is left as
// it is.
func TestInstalledMarketLineHealed(t *testing.T) {
	h := sandbox(t)
	seedLibrary(t,
		Server{Name: "serena", Transport: "stdio", Command: "uvx", Args: []string{"serena-agent"}, Env: map[string]string{"A": "b"}, Agents: []string{"codex"}},
		Server{Name: "mine", Transport: "stdio", Command: "uvx", Args: []string{"serena-agent", "start-mcp-server", "--context", "codex"}, Agents: []string{"codex"}},
	)
	ok(t)(Sync())
	want := []string{"-p", "3.13", "serena-agent", "start-mcp-server", "--project-from-cwd", "--context", "desktop-app"}
	var c struct {
		Servers map[string]struct {
			Command string            `toml:"command"`
			Args    []string          `toml:"args"`
			Env     map[string]string `toml:"env"`
		} `toml:"mcp_servers"`
	}
	if err := toml.Unmarshal([]byte(read(t, filepath.Join(h, ".codex", "config.toml"))), &c); err != nil {
		t.Fatal(err)
	}
	if got := c.Servers["serena"]; got.Command != "uvx" || !slices.Equal(got.Args, want) || got.Env["A"] != "b" {
		t.Errorf("Codex starts serena as %+v", got)
	}
	if got := c.Servers["mine"]; !slices.Equal(got.Args, []string{"serena-agent", "start-mcp-server", "--context", "codex"}) {
		t.Errorf("the user's own line changed: %q", got.Args)
	}
	var l Library
	if err := json.Unmarshal([]byte(read(t, path())), &l); err != nil {
		t.Fatal(err)
	}
	for _, s := range l.MCP {
		if s.Name == "serena" && !slices.Equal(s.Args, want) {
			t.Errorf("library.json keeps %q", s.Args)
		}
	}
	if res := ok(t)(Sync()); len(res.Changed) != 0 {
		t.Errorf("a second sync changed %v", res.Changed)
	}
}

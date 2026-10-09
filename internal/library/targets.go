package library

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/desktopdir"
)

// Target is where one agent keeps each of the three: an empty path is
// something it has no user-wide place for.
type Target struct {
	Agent *agent.Agent
	// Instructions is the file the agent reads before every conversation;
	// Override, when it exists, is read instead of it (Codex's
	// AGENTS.override.md), which the page warns of.
	Instructions, Override string
	MCP                    *mcpFile
	Skills                 string // the folder the agent finds skills in
	// SkillsAlso are agents whose skills this one reads as well, as
	// OpenCode reads Claude Code's.
	SkillsAlso []string
	// MCPVia is the extension the agent reads its MCP servers through, for
	// one that has none of its own.
	MCPVia string
	// Note is what the page says of the agent's instructions file.
	Note string
	// Copy is an agent that gets a copy of each skill, made again when
	// the library's changes, rather than a link: one in a WSL distro,
	// which can't follow a link to a Windows folder.
	Copy bool
	// Desktop are Claude Desktop's skills-plugin folders, one for each
	// account (desktop_skills.go): each gets a copy of the skills and their
	// entries in its manifest.json. Skills is the first one's skills.
	Desktop []string
}

func home() string { h, _ := os.UserHomeDir(); return h }

func claudeDir() string {
	if d := appdir.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(home(), ".claude")
}

// claudeJSON is where Claude Code keeps its user-wide MCP servers: beside
// its folder, or in it when CLAUDE_CONFIG_DIR moves it.
func claudeJSON() string {
	if d := appdir.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json")
	}
	return filepath.Join(home(), ".claude.json")
}

// codebuddyMCP is the file CodeBuddy Code, its folder dir, reads its
// user-wide MCP servers from (see targetOf).
func codebuddyMCP(dir string) string {
	base := appdir.Getenv("CODEBUDDY_CONFIG_DIR")
	if base == "" {
		base = home()
	}
	all := []string{filepath.Join(dir, ".mcp.json"), filepath.Join(dir, "mcp.json"), filepath.Join(base, ".codebuddy.json")}
	for _, p := range all {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return all[0]
}

func codexDir() string {
	if d := appdir.Getenv("CODEX_HOME"); d != "" {
		return d
	}
	return filepath.Join(home(), ".codex")
}

// targetOf is where a known agent keeps them, or nil for one magpie can't
// give any of them to.
func targetOf(a *agent.Agent) *Target {
	if a.WSL != "" {
		return wslTargetOf(a)
	}
	h := home()
	t := &Target{Agent: a}
	switch a.ID {
	case "claude":
		d := claudeDir()
		t.Instructions = filepath.Join(d, "CLAUDE.md")
		t.MCP = &mcpFile{Path: claudeJSON(), Format: fmtClaude}
		t.Skills = filepath.Join(d, "skills")
	case "codex":
		d := codexDir()
		t.Instructions = filepath.Join(d, "AGENTS.md")
		t.Override = filepath.Join(d, "AGENTS.override.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "config.toml"), Format: fmtCodex}
		t.Skills = filepath.Join(d, "skills")
	case "gemini":
		d := filepath.Join(h, ".gemini")
		t.Instructions = filepath.Join(d, "GEMINI.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "settings.json"), Format: fmtGemini}
		t.Skills = filepath.Join(d, "skills")
	case "agy":
		// Antigravity — the app and agy alike — reads its user-wide
		// customizations from ~/.gemini/config: GEMINI.md there (as well as
		// Gemini CLI's ~/.gemini/GEMINI.md), mcp_config.json and skills
		// (antigravity.google/docs/rules, /mcp, /skills; agy 1.2's `mcp
		// add` writes that file); agy 1.2 reads no mcp_config.json from the
		// IDE's older ~/.gemini/antigravity.
		d := filepath.Join(h, ".gemini", "config")
		t.Instructions = filepath.Join(d, "GEMINI.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "mcp_config.json"), Format: fmtAntigravity}
		t.Skills = filepath.Join(d, "skills")
	case "opencode":
		d := filepath.Dir(a.Path)
		t.Instructions = filepath.Join(d, "AGENTS.md")
		t.Note = "opencode-claude"
		t.MCP = &mcpFile{Path: a.Path, Format: fmtOpenCode}
		t.Skills = filepath.Join(d, "skills")
		t.SkillsAlso = []string{"claude"}
	case "mimocode":
		// MiMo Code is OpenCode's config shape; it reads Claude Code's
		// CLAUDE.md but its own skills only (a flag opens .claude's), so the
		// shared skills go into its own folder, not Claude Code's.
		d := filepath.Dir(a.Path)
		t.Instructions = filepath.Join(d, "AGENTS.md")
		t.MCP = &mcpFile{Path: a.Path, Format: fmtOpenCode}
		t.Skills = filepath.Join(d, "skills")
	case "pi":
		// PI_CODING_AGENT_DIR's, else ~/.pi/agent (agent.piDir)
		d := a.Dir
		t.Instructions = filepath.Join(d, "AGENTS.md")
		// Pi 0.99 reads MCP servers itself, from its mcp.json; before it,
		// and while an MCP extension replaces its own (pi-mcp-adapter,
		// pi-mcp-extension), each extension reads its own file (pimcp.go)
		t.MCP, t.MCPVia = piMCP(h, d, piVersion(a))
		t.Skills = filepath.Join(d, "skills")
	case "dsh":
		// DeepSeek Harness reads $DSH_HOME/AGENTS.md and $DSH_HOME/skills;
		// its MCP servers are @deepseek-ai/dsh-mcp-client rows its patch
		// lists insert, every profile's
		d := a.Dir
		t.Instructions = filepath.Join(d, "AGENTS.md")
		files := agent.DshPatchFiles(d)
		t.MCP = &mcpFile{Path: files[0], Also: files[1:], Format: fmtDsh}
		t.Skills = filepath.Join(d, "skills")
	case "omp":
		// ~/.omp/agent, or where omp's variables move it (agent.ompDir)
		d := a.Dir
		t.Instructions = filepath.Join(d, "AGENTS.md")
		// omp reads mcpServers from its agent folder's mcp.json
		// (discovery/builtin.ts), type choosing the transport
		t.MCP = &mcpFile{Path: filepath.Join(d, "mcp.json"), Format: fmtOmp}
		t.Skills = filepath.Join(d, "skills")
	case "omo":
		// OmO's engine (senpi, a fork of Pi) reads its agent folder's
		// AGENTS.md, skills and mcp.json — mcpServers in the shape Pi 0.99's
		// own has (command/args/env, url/headers), any other key refusing
		// the whole file (docs/mcp.md, config-schema.js)
		d := a.Dir
		t.Instructions = filepath.Join(d, "AGENTS.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "mcp.json"), Format: fmtPiNative}
		t.Skills = filepath.Join(d, "skills")
	case "goose":
		t.Instructions = filepath.Join(filepath.Dir(a.Path), ".goosehints")
		t.MCP = &mcpFile{Path: a.Path, Format: fmtGoose}
		// Goose finds skills in ~/.agents/skills first, its docs say
		t.Skills = sharedSkillsDir()
	case "cursor":
		d := filepath.Join(h, ".cursor")
		t.MCP = &mcpFile{Path: filepath.Join(d, "mcp.json"), Format: fmtCursor}
		t.Skills = filepath.Join(d, "skills")
	case "copilot":
		d := appdir.Getenv("COPILOT_HOME")
		if d == "" {
			d = filepath.Join(h, ".copilot")
		}
		t.Instructions = filepath.Join(d, "copilot-instructions.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "mcp-config.json"), Format: fmtCopilot}
		t.Skills = filepath.Join(d, "skills")
	case "crush":
		// Crush reads CRUSH.md from its config folder, ~/.config/crush on
		// Windows too since Crush 0.14; the crush.json magpie edits there is
		// %LOCALAPPDATA%\crush's, where Crush keeps its own picks and reads
		// skills but no CRUSH.md
		cfg := appdir.Getenv("XDG_CONFIG_HOME")
		if cfg == "" {
			cfg = filepath.Join(h, ".config")
		}
		t.Instructions = filepath.Join(cfg, "crush", "CRUSH.md")
		t.MCP = &mcpFile{Path: a.Path, Format: fmtCrush}
		t.Skills = filepath.Join(filepath.Dir(a.Path), "skills")
		t.SkillsAlso = []string{"claude"}
	case "zcode":
		// ZCode's own servers are its cli/config.json's mcp.servers (the
		// app's MCP settings write there); AGENTS.md and skills beside it
		d := filepath.Join(h, ".zcode")
		t.Instructions = filepath.Join(d, "AGENTS.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "cli", "config.json"), Format: fmtZCode}
		t.Skills = filepath.Join(d, "skills")
	case "kimi":
		// Kimi Code reads one user-wide folder of each kind, the first there
		// is: ~/.kimi/skills, ~/.claude/skills, ~/.codex/skills — so one of
		// its own would hide Claude Code's from it — and ~/.config/agents/
		// skills, ~/.agents/skills (kimi_cli/skill), wherever KIMI_SHARE_DIR
		// is; before its 1.x brand/generic split, only the first of all five.
		// The new Kimi Code (2.x) reads its own and ~/.agents/skills only.
		//
		// Its MCP servers are the mcp.json in its folder, which the old
		// kimi-cli (fastmcp's MCPConfig) and the new Kimi Code read alike.
		t.MCP = &mcpFile{Path: filepath.Join(a.Dir, "mcp.json"), Format: fmtKimi}
		if _, legacy := agent.KimiDir(h); !legacy {
			t.Skills = sharedSkillsDir()
		} else if d := filepath.Join(h, ".config", "agents", "skills"); isDir(d) {
			t.Skills = d
		} else {
			t.Skills = sharedSkillsDir()
		}
	case "alma":
		// Alma reads personal skills from ~/.config/alma/skills, on every
		// system (its home, not its data folder), and Claude Code's, Codex's
		// and ~/.agents/skills besides (its skills service, #824). Its MCP
		// servers are ~/.config/alma/mcp.json's mcpServers, read as Alma
		// starts and when its MCP settings refresh (0.4.164's
		// out/main/index.js, #1292). It has no user-wide instructions file,
		// its prompts being its settings' own
		t.Skills = filepath.Join(h, ".config", "alma", "skills")
		t.MCP = &mcpFile{Path: filepath.Join(h, ".config", "alma", "mcp.json"), Format: fmtAlma}
		t.SkillsAlso = []string{"claude", "codex"}
	case "cindy":
		// Cindy keeps its user-wide skills in ~/.agents/skills
		t.Skills = sharedSkillsDir()
	case "hermes":
		// Hermes Agent reads mcp_servers from $HERMES_HOME/config.yaml, the
		// file magpie sets its model in too (tools/mcp_tool.py), and skills
		// from $HERMES_HOME/skills
		t.MCP = &mcpFile{Path: a.Path, Format: fmtHermes}
		t.Skills = filepath.Join(a.Dir, "skills")
	case "minimax-code":
		// MiniMax Code loads the skills in its data folder ($MINIMAX_DATA_DIR,
		// else ~/.minimax); it has no user-wide MCP file, only plugins' own
		t.Skills = filepath.Join(a.Dir, "skills")
	case "devin":
		// Devin reads its user-wide MCP servers from mcp_config.json
		// beside its config.json (devin mcp add --scope user)
		t.MCP = &mcpFile{Path: filepath.Join(a.Dir, "mcp_config.json"), Format: fmtDevin}
		t.Skills = filepath.Join(a.Dir, "skills")
	case "grok":
		// Grok Build reads [mcp_servers.<name>] from its config.toml, as
		// `grok mcp add` writes them; its user-wide instructions are
		// $GROK_HOME/AGENTS.md, loaded before a project's own
		t.MCP = &mcpFile{Path: a.Path, Format: fmtGrok}
		t.Instructions = filepath.Join(a.Dir, "AGENTS.md")
		t.Skills = filepath.Join(a.Dir, "skills")
	case "droid":
		// Droid's user-wide servers are ~/.factory/mcp.json's mcpServers,
		// type stdio, http or sse as Claude Code's (docs.factory.ai/cli/
		// configuration/mcp); its personal instructions are ~/.factory/
		// AGENTS.md, which project files override (cli/configuration/agents-md)
		t.MCP = &mcpFile{Path: filepath.Join(a.Dir, "mcp.json"), Format: fmtClaude}
		t.Instructions = filepath.Join(a.Dir, "AGENTS.md")
		t.Skills = filepath.Join(a.Dir, "skills")
	case "qoder", "qoder-cn":
		// Qoder's user-wide servers are its settings.json's mcpServers
		// (docs.qoder.com/cli/mcp-reference), beside magpie's provider
		t.MCP = &mcpFile{Path: a.Path, Format: fmtOmp}
		t.Skills = filepath.Join(a.Dir, "skills")
	case "cline":
		// Cline's CLI reads its MCP servers from settings/
		// cline_mcp_settings.json beside providers.json, or
		// $CLINE_MCP_SETTINGS_PATH (@cline/shared's storage)
		p := appdir.Getenv("CLINE_MCP_SETTINGS_PATH")
		if p == "" {
			p = filepath.Join(filepath.Dir(a.Path), "cline_mcp_settings.json")
		}
		t.MCP = &mcpFile{Path: p, Format: fmtCline}
		t.Skills = filepath.Join(a.Dir, "skills")
	case "commandcode":
		// Command Code reads ~/.commandcode/mcp.json (getUserMcpConfigPath)
		t.MCP = &mcpFile{Path: filepath.Join(a.Dir, "mcp.json"), Format: fmtCommandCode}
		t.Skills = filepath.Join(a.Dir, "skills")
	case "workbuddy":
		// WorkBuddy's own servers are mcp.json in its folder
		// ($WORKBUDDY_CONFIG_DIR, else ~/.workbuddy: ConnectorService's
		// customMcpConfigPath in 5.5.6's app.asar), mcpServers as Claude
		// Code's, which its CodeBuddy engine runs (type stdio, http or sse).
		// WorkBuddy connects one only once it is trusted there: a server
		// mcp-approvals.json has no approval for (its command, args and env
		// names, or its URL's origin) is listed as needing approval until it
		// is switched on in WorkBuddy, which magpie leaves to the user
		// (#1266). Its skills are in skills/ there.
		t.MCP = &mcpFile{Path: filepath.Join(a.Dir, "mcp.json"), Format: fmtClaude}
		t.Skills = filepath.Join(a.Dir, "skills")
	case "codebuddy":
		// CodeBuddy Code's user-wide servers are in the first of
		// .mcp.json, mcp.json (in its folder) and .codebuddy.json (beside
		// it, or in $CODEBUDDY_CONFIG_DIR) that is there, else the first,
		// as it reads them and `codebuddy mcp add -s user` writes them
		// (PathUtils.resolveMcpFilePath, @tencent-ai/codebuddy-code
		// 2.162.0); mcpServers as Claude Code's, and no approval for a
		// user's server (isAllowed asks only of a project's). Its skills
		// are in skills/ in its folder (getHomeSkillsDir) (#1266).
		t.MCP = &mcpFile{Path: codebuddyMCP(a.Dir), Format: fmtClaude}
		t.Skills = filepath.Join(a.Dir, "skills")
	case "hanako", "fx":
		// a skills folder in the agent's own: Hanako's $HANA_HOME, fx's
		// ~/.fx — each said by its docs or source. No MCP servers:
		// OpenHanako runs one only once switched on for each agent and
		// tool, and fx's mcp.json, which one entry it refuses makes it read
		// none of, isn't one magpie could try.
		t.Skills = filepath.Join(a.Dir, "skills")
	case "atomcode":
		// AtomCode reads ~/.atomcode/ATOMCODE.md before every conversation
		// (its ATOMCODE.md, beside AGENTS.md and CLAUDE.md), its MCP servers
		// from mcp.json in its folder (`atomcode mcp add --global`, the same
		// mcpServers as omp's) and its skills from skills/ there
		t.Instructions = filepath.Join(a.Dir, "ATOMCODE.md")
		t.MCP = &mcpFile{Path: filepath.Join(a.Dir, "mcp.json"), Format: fmtOmp}
		t.Skills = filepath.Join(a.Dir, "skills")
	case "claude-desktop":
		// Claude Desktop reads only commands from its file: a remote server
		// is added in its own Connectors settings. In its 3p mode (magpie's
		// gateway, or any other) it reads the file in Claude-3p instead, so
		// where that folder is the servers go into both: each mode finds
		// them, a switch between the two leaves nothing behind in either
		t.MCP = &mcpFile{Path: filepath.Join(filepath.Dir(a.Path), "claude_desktop_config.json"), Format: fmtDesktop}
		if p := agent.DesktopConfig3p(h); p != t.MCP.Path && isDir(filepath.Dir(p)) {
			t.MCP.Also = []string{p}
		}
		// Cowork's skills, in every account's skills-plugin (#638)
		if t.Desktop = desktopSkillRoots(a.Dir); len(t.Desktop) > 0 {
			t.Skills = filepath.Join(t.Desktop[0], "skills")
		}
	default:
		return nil
	}
	return t
}

// wslTargetOf is where an agent in a WSL distro keeps them: its files at
// their defaults under the distro's $HOME (the distro's variables that
// move them aren't read), opened through \\wsl.localhost; nil while the
// distro is stopped, which opening them would start (asked again here: the
// agent may have been made before the user stopped it). What is written is
// the same as for this machine's agent: nothing in it names a place on
// Windows, and each skill is a copy.
func wslTargetOf(a *agent.Agent) *Target {
	h := a.Home
	if h == "" || !agent.WSLRunning(a.WSL) {
		return nil
	}
	t := &Target{Agent: a, Copy: true}
	id, _, _ := strings.Cut(a.ID, "@")
	switch id {
	case "claude":
		d := filepath.Join(h, ".claude")
		t.Instructions = filepath.Join(d, "CLAUDE.md")
		t.MCP = &mcpFile{Path: filepath.Join(h, ".claude.json"), Format: fmtClaude, WSL: true, Distro: a.WSL, Home: linuxHome(h)}
		t.Skills = filepath.Join(d, "skills")
	case "codex":
		d := filepath.Join(h, ".codex")
		t.Instructions = filepath.Join(d, "AGENTS.md")
		t.Override = filepath.Join(d, "AGENTS.override.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "config.toml"), Format: fmtCodex, WSL: true, Distro: a.WSL, Home: linuxHome(h)}
		t.Skills = filepath.Join(d, "skills")
	case "pi":
		// its MCP servers go where the Pi installed there reads them,
		// which its version decides (piMCP), and that isn't known here
		d := filepath.Join(h, ".pi", "agent")
		t.Instructions = filepath.Join(d, "AGENTS.md")
		t.Skills = filepath.Join(d, "skills")
	case "omo":
		d := filepath.Join(h, ".omo", "agent")
		t.Instructions = filepath.Join(d, "AGENTS.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "mcp.json"), Format: fmtPiNative, WSL: true, Distro: a.WSL, Home: linuxHome(h)}
		t.Skills = filepath.Join(d, "skills")
	default:
		return wslOwnFolder(a)
	}
	return t
}

// ownFolder are the agents that keep all the library gives them in their
// own folder (a.Dir, or beside a.Path), read from no variable of this
// machine's and from nothing under its home: one magpie found in a distro
// has that folder at the distro's home already, so its target there is
// the one targetOf makes on this machine, given copies (#1323).
var ownFolder = map[string]bool{
	"hermes": true, "omp": true, "opencode": true, "mimocode": true, "droid": true,
	"grok": true, "commandcode": true, "atomcode": true, "qoder": true, "qoder-cn": true,
	"minimax-code": true, "fx": true, "dsh": true,
}

// wslOwnFolder is the target of a, an agent in a running WSL distro that
// keeps everything in its own folder; nil for any other.
func wslOwnFolder(a *agent.Agent) *Target {
	id, at, _ := strings.Cut(a.ID, "@")
	if !ownFolder[id] {
		return nil
	}
	local := *a
	local.ID, local.WSL = id, ""
	t := targetOf(&local)
	if t == nil {
		return nil
	}
	t.Agent, t.Copy = a, true
	if t.MCP != nil {
		t.MCP.WSL, t.MCP.Distro, t.MCP.Home = true, a.WSL, linuxHome(a.Home)
	}
	// the skills it reads besides its own are those agents' in the same
	// distro, not this machine's
	for i, o := range t.SkillsAlso {
		t.SkillsAlso[i] = o + "@" + at
	}
	return t
}

// apps are what the library can give MCP servers to that aren't agents
// magpie sets up: known by the folder they keep their settings in.
func apps() []*agent.Agent {
	// Desktop's own folder (%APPDATA%\Claude on Windows, the MSIX
	// package's for a packaged Desktop, which doesn't see a file written
	// into %APPDATA%). Desktop only ever run in its 3p mode has no Claude
	// folder, only Claude-3p: its file is the one there
	d := desktopdir.Here()
	dir := d.Data
	if !isDir(dir) && isDir(d.ThreeP) {
		dir = d.ThreeP
	}
	return []*agent.Agent{
		{ID: "claude-desktop", Name: "Claude Desktop", Icon: "claude-color", Dir: dir, Path: filepath.Join(dir, "claude_desktop_config.json")},
	}
}

// Targets are the agents on this machine that magpie can give any of the
// three to, in the order the rest of magpie lists them.
func Targets() []*Target {
	var out []*Target
	own := map[string]bool{}
	for _, a := range apps() {
		own[a.ID] = true
	}
	for _, a := range agent.Detected() {
		// an app magpie also sets up as an agent (Claude Desktop) keeps its
		// MCP servers where apps says, once
		if own[a.ID] {
			continue
		}
		if t := targetOf(a); t != nil && t.open() {
			out = append(out, t)
		}
	}
	for _, a := range apps() {
		if !a.Detected() {
			continue
		}
		if t := targetOf(a); t != nil && t.open() {
			out = append(out, t)
		}
	}
	readsWith(out)
	return out
}

// readsShared are the agents that read ~/.agents/skills as well as their
// own folder (each one's docs or source).
var readsShared = []string{"codex", "gemini", "opencode", "crush", "dsh", "commandcode", "devin", "droid", "cline", "grok", "fx", "alma"}

// readsWith adds to each agent's SkillsAlso the agents whose folder it
// reads skills from too: one that is the very same folder (Kimi Code's,
// Goose's and Cindy's, all ~/.agents/skills), or ~/.agents/skills for one
// that reads it besides its own — a skill magpie gives there once, for
// any of them, every one of them has.
func readsWith(ts []*Target) {
	real := make([]string, len(ts))
	for i, t := range ts {
		if t.Skills != "" {
			real[i] = realDir(t.Skills)
		}
	}
	shared := realDir(sharedSkillsDir())
	for i, t := range ts {
		for j, o := range ts {
			if i == j || real[i] == "" || real[j] == "" || slices.Contains(t.SkillsAlso, o.Agent.ID) {
				continue
			}
			if real[j] == real[i] || real[j] == shared && slices.Contains(readsShared, t.Agent.ID) {
				t.SkillsAlso = append(t.SkillsAlso, o.Agent.ID)
			}
		}
	}
}

// open leaves out of the target each place a file stands where a folder
// of it would be — another tool's ~/.dsh or ~/.gemini: nothing can be
// written there, and the agent isn't reading anything from it — and says
// whether any place is left.
func (t *Target) open() bool {
	if t.Instructions != "" && agent.Taken(filepath.Dir(t.Instructions)) {
		t.Instructions, t.Override, t.Note = "", "", ""
	}
	if t.MCP != nil && slices.ContainsFunc(t.MCP.files(), func(p string) bool { return agent.Taken(filepath.Dir(p)) }) {
		t.MCP, t.MCPVia = nil, ""
	}
	if t.Skills != "" && agent.Taken(t.Skills) {
		t.Skills, t.SkillsAlso = "", nil
	}
	return t.Instructions != "" || t.MCP != nil || t.Skills != ""
}

func targetByID(id string) *Target {
	for _, t := range Targets() {
		if t.Agent.ID == id {
			return t
		}
	}
	return nil
}

// Takes is the id of the agent q names (its id, an alias, its name) when
// the library can give it kind — "instructions", "mcp" or "skills" — or
// why not: an agent it has no place for isn't recorded as getting it and
// then given nothing.
func Takes(q, kind string) (string, error) {
	var a *agent.Agent
	for _, app := range apps() {
		if strings.EqualFold(app.ID, q) || strings.EqualFold(app.Name, q) {
			a = app
		}
	}
	if a == nil {
		var err error
		if a, err = agent.Find(q); err != nil {
			return "", err
		}
	}
	t := targetOf(a)
	var has bool
	what := map[string]string{"instructions": "instructions", "mcp": "MCP servers", "skills": "skills"}[kind]
	if t == nil && a.WSL != "" && (a.Home == "" || !agent.WSLRunning(a.WSL)) {
		return "", fmt.Errorf("WSL %s isn't running: start it, and %s can be given %s", a.WSL, a.Name, what)
	}
	if t != nil {
		switch kind {
		case "instructions":
			has = t.Instructions != ""
		case "mcp":
			has = t.MCP != nil
		case "skills":
			has = t.Skills != ""
		}
	}
	if !has {
		return "", fmt.Errorf("%s%s", noPlace(a, kind, what), takenBy(kind, what))
	}
	return a.ID, nil
}

// noPlace says why an agent can't be given kind, and where it gets it
// instead when another agent's files are what it reads (MOMO on Discord).
func noPlace(a *agent.Agent, kind, what string) string {
	switch {
	case a.ID == "openchamber":
		// OpenChamber's server reads the global AGENTS.md, skills folder
		// and opencode.json from OpenCode's config folder
		// (packages/web/server/lib/opencode/shared.js), and the OpenCode it
		// runs reads them there too
		return fmt.Sprintf("OpenChamber has no place of its own for %s: it runs OpenCode on OpenCode's config, so it has what OpenCode is given · give them to opencode", what)
	case a.ID == "claude-desktop" && kind == "instructions":
		// Desktop's chat takes its instructions in its own settings, kept
		// with the Claude account, not in a file; its Code tab is Claude
		// Code, which reads Claude Code's CLAUDE.md
		return "Claude Desktop keeps its instructions in its own settings, with your Claude account, not in a file magpie can write; its Code tab runs Claude Code, which reads Claude Code's · give them to claude"
	}
	return fmt.Sprintf("%s has no user-wide place for %s that magpie knows of", a.Name, what)
}

// takenBy lists the agents here that kind can be given to.
func takenBy(kind, what string) string {
	var ids []string
	for _, t := range Targets() {
		if kind == "instructions" && t.Instructions != "" || kind == "mcp" && t.MCP != nil || kind == "skills" && t.Skills != "" {
			ids = append(ids, t.Agent.ID)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	return "\n  agents here that take " + what + ": " + strings.Join(ids, ", ")
}

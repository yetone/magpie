package library

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yetone/magpie/internal/agent"
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
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	return filepath.Join(home(), ".claude")
}

// claudeJSON is where Claude Code keeps its user-wide MCP servers: beside
// its folder, or in it when CLAUDE_CONFIG_DIR moves it.
func claudeJSON() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, ".claude.json")
	}
	return filepath.Join(home(), ".claude.json")
}

func codexDir() string {
	if d := os.Getenv("CODEX_HOME"); d != "" {
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
		d := os.Getenv("COPILOT_HOME")
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
		cfg := os.Getenv("XDG_CONFIG_HOME")
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
		// `grok mcp add` writes them
		t.MCP = &mcpFile{Path: a.Path, Format: fmtGrok}
		t.Skills = filepath.Join(a.Dir, "skills")
	case "droid":
		// Droid's user-wide servers are ~/.factory/mcp.json's mcpServers,
		// type stdio, http or sse as Claude Code's (docs.factory.ai/cli/
		// configuration/mcp)
		t.MCP = &mcpFile{Path: filepath.Join(a.Dir, "mcp.json"), Format: fmtClaude}
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
		p := os.Getenv("CLINE_MCP_SETTINGS_PATH")
		if p == "" {
			p = filepath.Join(filepath.Dir(a.Path), "cline_mcp_settings.json")
		}
		t.MCP = &mcpFile{Path: p, Format: fmtCline}
		t.Skills = filepath.Join(a.Dir, "skills")
	case "commandcode":
		// Command Code reads ~/.commandcode/mcp.json (getUserMcpConfigPath)
		t.MCP = &mcpFile{Path: filepath.Join(a.Dir, "mcp.json"), Format: fmtCommandCode}
		t.Skills = filepath.Join(a.Dir, "skills")
	case "workbuddy", "hanako", "fx":
		// a skills folder in the agent's own: WorkBuddy's ~/.workbuddy,
		// Hanako's $HANA_HOME, fx's ~/.fx — each said by its docs or
		// source. No MCP servers: WorkBuddy runs one in its mcp.json only
		// once it is approved in WorkBuddy, OpenHanako only once switched
		// on for each agent and tool, and fx's mcp.json, which one entry it
		// refuses makes it read none of, isn't one magpie could try.
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
// distro is stopped, which opening them would start. What is written is
// the same as for this machine's agent: nothing in it names a place on
// Windows, and each skill is a copy.
func wslTargetOf(a *agent.Agent) *Target {
	h := a.Home
	if h == "" {
		return nil
	}
	t := &Target{Agent: a, Copy: true}
	id, _, _ := strings.Cut(a.ID, "@")
	switch id {
	case "claude":
		d := filepath.Join(h, ".claude")
		t.Instructions = filepath.Join(d, "CLAUDE.md")
		t.MCP = &mcpFile{Path: filepath.Join(h, ".claude.json"), Format: fmtClaude, WSL: true}
		t.Skills = filepath.Join(d, "skills")
	case "codex":
		d := filepath.Join(h, ".codex")
		t.Instructions = filepath.Join(d, "AGENTS.md")
		t.Override = filepath.Join(d, "AGENTS.override.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "config.toml"), Format: fmtCodex, WSL: true}
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
		t.MCP = &mcpFile{Path: filepath.Join(d, "mcp.json"), Format: fmtPiNative, WSL: true}
		t.Skills = filepath.Join(d, "skills")
	default:
		return nil
	}
	return t
}

// apps are what the library can give MCP servers to that aren't agents
// magpie sets up: known by the folder they keep their settings in.
func apps() []*agent.Agent {
	d, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	// Desktop only ever run in its 3p mode has no Claude folder, only
	// Claude-3p: its file is the one there
	dir := filepath.Join(d, "Claude")
	if p := agent.DesktopConfig3p(home()); !isDir(dir) && isDir(filepath.Dir(p)) {
		dir = filepath.Dir(p)
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
var readsShared = []string{"codex", "gemini", "opencode", "crush", "dsh", "commandcode", "devin", "droid", "cline", "grok", "fx"}

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
	if t == nil && a.WSL != "" && a.Home == "" {
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
		return "", fmt.Errorf("%s has no user-wide place for %s that magpie knows of", a.Name, what)
	}
	return a.ID, nil
}

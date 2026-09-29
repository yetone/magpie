package library

import (
	"fmt"
	"os"
	"path/filepath"
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
		d := os.Getenv("PI_CODING_AGENT_DIR")
		if d == "" {
			d = filepath.Join(h, ".pi", "agent")
		}
		t.Instructions = filepath.Join(d, "AGENTS.md")
		// Pi has no MCP of its own: its extensions for it (pi-mcp-adapter,
		// pi-mcp-extension) each read their own file (pimcp.go)
		t.MCP = piMCP(h, d)
		t.MCPVia = "pi-mcp-adapter"
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
		d := filepath.Join(h, ".omp", "agent")
		t.Instructions = filepath.Join(d, "AGENTS.md")
		t.Skills = filepath.Join(d, "skills")
	case "goose":
		t.Instructions = filepath.Join(filepath.Dir(a.Path), ".goosehints")
		t.MCP = &mcpFile{Path: a.Path, Format: fmtGoose}
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
	case "claude-desktop":
		// Claude Desktop reads only commands from its file: a remote server
		// is added in its own Connectors settings
		t.MCP = &mcpFile{Path: filepath.Join(filepath.Dir(a.Path), "claude_desktop_config.json"), Format: fmtDesktop}
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
	return []*agent.Agent{
		{ID: "claude-desktop", Name: "Claude Desktop", Icon: "claude-color", Dir: filepath.Join(d, "Claude"), Path: filepath.Join(d, "Claude", "claude_desktop_config.json")},
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
		if t := targetOf(a); t != nil {
			out = append(out, t)
		}
	}
	for _, a := range apps() {
		if !a.Detected() {
			continue
		}
		if t := targetOf(a); t != nil {
			out = append(out, t)
		}
	}
	return out
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

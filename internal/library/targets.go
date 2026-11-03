package library

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/jsonc"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/agentplug"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/desktopdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/plugin"
	"github.com/yetone/magpie/internal/provider"
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
	// the library's changes, rather than a link: one in a WSL distro whose
	// skills folder is on a Windows drive (wslStored).
	Copy bool
	// Store, for an agent in a WSL distro, is magpie's folder there
	// (wslStoreRel) holding one copy of each skill given to the distro's
	// agents, as magpie opens it: each agent gets a link the distro makes
	// to that copy, as it can't follow one to a Windows folder
	// (wsl_skills.go).
	Store string
	// Desktop are Claude Desktop's skills-plugin folders, one for each
	// account (desktop_skills.go): each gets a copy of the skills and their
	// entries in its manifest.json. Skills is the first one's skills.
	Desktop []string
	// Settings is Claude Code's settings.json, whose permissions.deny
	// leaves out the tools of a server turned off (mcp_tools_off.go)
	Settings string
	// Gone is an agent no longer installed here that still has what magpie
	// gave it (held): a sync only takes things out of it, as they are
	// unticked, and gives it nothing.
	Gone bool
	// SharedToo is an agent that reads ~/.agents/skills besides its own
	// folder only from some version on (Pi, omp: sharedSince), with that
	// version here; the others that always do are in readsShared
	SharedToo bool
	// Profile says the instructions file is the user's profile to the
	// agent, not instructions alone (Alma's USER.md): magpie adds its part
	// after the user's, but never moves the user's own lines out of it
	// (ImportInstructions), as the agent reads who its owner is there.
	Profile bool
	// Unreached, for an agent in a WSL distro, is why its folder there
	// can't be opened (agent.WSLOpen): a link the distro couldn't
	// resolve. Nothing is written to it; each sync says so.
	Unreached error
}

// unreached says whether t's folder can't be opened, and, once in res,
// why.
func (r *Result) unreached(t *Target) bool {
	if t.Unreached == nil {
		return false
	}
	for _, p := range r.Problems {
		if p.Agent == t.Agent.ID && p.What == "wsl" {
			return true
		}
	}
	r.fail(t.Agent.ID, "wsl", t.Unreached)
	return true
}

func home() string { h, _ := os.UserHomeDir(); return h }

// grokDir is where Grok Build keeps its config.toml: $GROK_HOME, else ~/.grok
// (internal/agent/grok.go grokIn).
func grokDir() string {
	if d := appdir.Getenv("GROK_HOME"); d != "" {
		return d
	}
	return filepath.Join(home(), ".grok")
}

func claudeDir() string { return appdir.ClaudeDirIn(home()) }

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
	return codebuddyMCPIn(dir, base)
}

// codebuddyMCPIn is codebuddyMCP with base as where .codebuddy.json is
// beside: a WSL distro's home for the CodeBuddy Code there.
func codebuddyMCPIn(dir, base string) string {
	all := []string{filepath.Join(dir, ".mcp.json"), filepath.Join(dir, "mcp.json"), filepath.Join(base, ".codebuddy.json")}
	for _, p := range all {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return all[0]
}

func codexDir() string { return appdir.CodexHomeIn(home()) }

// targetOf is where a known agent keeps them, or nil for one magpie can't
// give any of them to.
func targetOf(a *agent.Agent) *Target {
	if a.WSL != "" {
		return wslTargetOf(a)
	}
	h := home()
	t := &Target{Agent: a}
	if n := a.SharedName(); n != a.Name {
		// the files are another app's too (the Cursor editor's ~/.cursor):
		// named for both, not for the CLI alone
		named := *a
		named.Name = n
		t.Agent = &named
	}
	switch a.ID {
	case "claude":
		d := claudeDir()
		t.Instructions = filepath.Join(d, "CLAUDE.md")
		t.MCP = &mcpFile{Path: claudeJSON(), Format: fmtClaude}
		t.Skills = filepath.Join(d, "skills")
		t.Settings = filepath.Join(d, "settings.json")
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
	case "opencode", "deveco":
		// DevEco Code is OpenCode's fork and reads as it does: AGENTS.md
		// in its config folder, else Claude Code's CLAUDE.md, skills there
		// and under ~/.claude and ~/.agents (packages/opencode/src/session
		// /instruction.ts, skill/index.ts)
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
		// PI_CODING_AGENT_DIR's, else ~/.pi/agent (agent.piDir). Pi reads
		// AGENTS.override.md there when it is there, else the first there
		// is of AGENTS.md, AGENTS.MD, CLAUDE.md and CLAUDE.MD
		// (loadContextFileFromDir in dist/core/resource-loader.js,
		// @earendil-works/pi-coding-agent 1.2.0; the older
		// @mariozechner/pi-coding-agent has the same order without the
		// override), so a CLAUDE.md of the user's own is the one to edit
		d := a.Dir
		t.Instructions = firstThere(d, "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD")
		t.Override = filepath.Join(d, "AGENTS.override.md")
		// Pi 0.99 reads MCP servers itself, from its mcp.json; before it,
		// and while an MCP extension replaces its own (pi-mcp-adapter,
		// pi-mcp-extension), each extension reads its own file (pimcp.go)
		v := piVersion(a)
		t.MCP, t.MCPVia = piMCP(h, d, v, piGlobalRoots())
		t.Skills = filepath.Join(d, "skills")
		t.SharedToo = since(v, piSharedSince)
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
		t.SharedToo = since(ompVersion(a), ompSharedSince)
	case "omo":
		// OmO's engine (senpi, a fork of Pi) reads its agent folder's
		// AGENTS.md, skills and mcp.json — mcpServers in the shape Pi 0.99's
		// own has (command/args/env, url/headers), any other key refusing
		// the whole file (docs/mcp.md, config-schema.js). Its instructions
		// are found as Pi's are: AGENTS.override.md, else the first there
		// is of AGENTS.md, AGENTS.MD, CLAUDE.md and CLAUDE.MD
		// (loadContextFileFromDir, @code-yeongyu/senpi 2026.10.12)
		d := a.Dir
		t.Instructions = firstThere(d, "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD")
		t.Override = filepath.Join(d, "AGENTS.override.md")
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
		// under the home ZCode reads, HOME first on Windows too
		d := filepath.Join(appdir.ZCodeHome(h), ".zcode")
		t.Instructions = filepath.Join(d, "AGENTS.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "cli", "config.json"), Format: fmtZCode}
		t.Skills = filepath.Join(d, "skills")
	case "kimi":
		// Kimi Code reads one user-wide folder of each kind, the first there
		// is: ~/.kimi/skills, ~/.claude/skills, ~/.codex/skills — so one of
		// its own would hide Claude Code's from it — and ~/.config/agents/
		// skills, ~/.agents/skills (kimi_cli/skill), wherever KIMI_SHARE_DIR
		// is; before its 1.x brand/generic split, only the first of all five.
		// The new Kimi Code (2.x) reads its own folder of skills
		// ($KIMI_CODE_HOME/skills, else ~/.kimi-code/skills, docs/
		// customization/skills.md "Skill Locations") and ~/.agents/skills:
		// its skills go into its own, for one in the shared folder reaches
		// every reader of it whatever the library ticks (#4379).
		//
		// Its MCP servers are the mcp.json in its folder, which the old
		// kimi-cli (fastmcp's MCPConfig) and the new Kimi Code read alike.
		t.MCP = &mcpFile{Path: filepath.Join(a.Dir, "mcp.json"), Format: fmtKimi}
		if _, legacy := agent.KimiDir(h); !legacy {
			// the new Kimi Code reads AGENTS.md in its folder ($KIMI_CODE_HOME,
			// else ~/.kimi-code) before every conversation; kimi-cli reads no
			// user-wide one (loadAgentsMdForRoots in packages/agent-core-v2/
			// src/agent/profile/context.ts, MoonshotAI/kimi-code afd95f64)
			t.Instructions = filepath.Join(a.Dir, "AGENTS.md")
			t.Skills = filepath.Join(a.Dir, "skills")
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
		// out/main/index.js, #1292). Its main chat puts ~/.config/alma/
		// USER.md, on every system, in its system prompt as its owner's
		// profile ("name, preferences, habits"), unless the thread's
		// workspace has a USER.md of its own (0.4.164's out/main/index.js,
		// #2315): magpie's part goes after what the user has there. The
		// file's YAML front matter is where Alma's Telegram and Discord
		// bots read who the owner is, so it is a Profile: the user's own
		// lines are never moved out of it
		t.Instructions = filepath.Join(h, ".config", "alma", "USER.md")
		t.Profile = true
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
		// and its global rules from AGENTS.md there, else the singular
		// AGENT.md it also reads, at the start of every session (Rules &
		// AGENTS.md → Global Rules in the docs Devin CLI 3000.11.92 ships,
		// share/devin/docs/extensibility/rules.mdx, #3151)
		t.Instructions = firstThere(a.Dir, "AGENTS.md", "AGENT.md")
	case "grok":
		// Grok Build reads [mcp_servers.<name>] from its config.toml, as
		// `grok mcp add` writes them; its user-wide instructions are
		// $GROK_HOME/AGENTS.md, loaded before a project's own
		t.MCP = &mcpFile{Path: a.Path, Format: fmtGrok}
		t.Instructions = filepath.Join(a.Dir, "AGENTS.md")
		t.Skills = filepath.Join(a.Dir, "skills")
	case "grok-app":
		// Independent mode (Use on a custom provider) starts the CLI with
		// GROK_HOME at agent-home, beside config.toml (grok-app's
		// providers.md). Shared mode keeps ~/.grok, which is Grok Build's
		// row. Skills there are read as well as ~/.agents/skills.
		home := filepath.Dir(a.Path)
		t.MCP = &mcpFile{Path: a.Path, Format: fmtGrok}
		t.Instructions = filepath.Join(home, "AGENTS.md")
		t.Skills = filepath.Join(home, "skills")
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
		// and AGENTS.md in its folder before every conversation, which its
		// settings' context.fileName renames as Qwen Code's does
		// (discoverMemoryPaths, @qoder-ai/qodercli 1.1.69's bundle)
		t.Instructions = filepath.Join(a.Dir, contextFile(a.Path, "AGENTS.md"))
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
		// and ~/.commandcode/AGENTS.md before every conversation
		// (getUserMemoryPath, command-code 1.81.0's dist/cli.mjs)
		t.Instructions = filepath.Join(a.Dir, "AGENTS.md")
	case "workbuddy", "workbuddy-ai":
		// WorkBuddy's own servers are mcp.json in its folder (WorkBuddy
		// AI's, ~/.workbuddy-ai, for the international build)
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
		// Its user-wide instructions are CODEBUDDY.md in its folder, else
		// CODEBUDDY.mdc there (MemoryLoader.loadUserMemories, 2.164.0)
		t.Instructions = firstThere(a.Dir, "CODEBUDDY.md", "CODEBUDDY.mdc")
	case "hanako", "fx":
		// a skills folder in the agent's own: Hanako's $HANA_HOME, fx's
		// ~/.fx — each said by its docs or source. No MCP servers:
		// OpenHanako runs one only once switched on for each agent and
		// tool, and fx's mcp.json, which one entry it refuses makes it read
		// none of, isn't one magpie could try.
		t.Skills = filepath.Join(a.Dir, "skills")
		if a.ID == "fx" {
			// fx reads ~/.fx/AGENTS.md before a project's own
			// (src/builtins/context.zig, vercel-labs/fx c3b4b926)
			t.Instructions = filepath.Join(a.Dir, "AGENTS.md")
		}
	case "prime-agent":
		// Prime Agent reads the first there is of AGENTS.md, AGENTS.MD,
		// CLAUDE.md and CLAUDE.MD in its agent folder
		// ($PRIME_AGENT_CODING_AGENT_DIR, else ~/.prime/agent)
		// (CONTEXT_FILE_CANDIDATES in crates/pa-core/src/resources/mod.rs,
		// prime-agent d592d5e7)
		t.Instructions = firstThere(a.Dir, "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD")
	case "stepcode":
		// StepCode reads AGENTS.override.md in its agent folder
		// ($STEP_CODING_AGENT_DIR, else agent/ in its config folder) when
		// it is there, else the first there is of AGENTS.md, AGENTS.MD,
		// CLAUDE.md and CLAUDE.MD (resolveStepAgentDir in step/
		// environment.ts, loadContextFileFromDir in core/resource-loader.ts,
		// stepfun-ai/Step-Code 3ba89e0c)
		d := strings.TrimSpace(appdir.Getenv("STEP_CODING_AGENT_DIR"))
		if !filepath.IsAbs(d) {
			d = filepath.Join(a.Dir, "agent")
		}
		t.Instructions = firstThere(d, "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD")
		t.Override = filepath.Join(d, "AGENTS.override.md")
	case "jcode":
		// jcode's own user-wide file is prompt-overlay.md in its folder
		// ($JCODE_HOME, else ~/.jcode), added to every conversation's
		// prompt; the AGENTS.md it also reads is the one in the home folder
		// itself, which isn't jcode's (load_prompt_overlay_files_from_dir in
		// crates/jcode-base/src/prompt.rs, jcode_dir in crates/jcode-storage,
		// 1jehuang/jcode 0d240f1d)
		t.Instructions = filepath.Join(a.Dir, "prompt-overlay.md")
	case "atomcode":
		// AtomCode reads ~/.atomcode/ATOMCODE.md before every conversation
		// (its ATOMCODE.md, beside AGENTS.md and CLAUDE.md), its MCP servers
		// from mcp.json in its folder (`atomcode mcp add --global`, the same
		// mcpServers as omp's) and its skills from skills/ there
		t.Instructions = filepath.Join(a.Dir, "ATOMCODE.md")
		t.MCP = &mcpFile{Path: filepath.Join(a.Dir, "mcp.json"), Format: fmtOmp}
		t.Skills = filepath.Join(a.Dir, "skills")
	case "qwen":
		// Qwen Code reads QWEN.md (and AGENTS.md) in its global folder,
		// $QWEN_HOME else ~/.qwen, before every conversation; its settings'
		// context.fileName renames them, the first name being the one it
		// writes itself (getGlobalQwenDir in packages/core/src/config/
		// storage.ts, getMemoryFilePaths in memory/memoryDiscovery.ts,
		// getCurrentMemoryFilename in utils/memory-constants.ts,
		// QwenLM/qwen-code c5098a27)
		t.Instructions = filepath.Join(a.Dir, contextFile(a.Path, "QWEN.md"))
	case "reasonix":
		// Reasonix reads every one there is of REASONIX.md, AGENTS.md and
		// CLAUDE.md in its state folder; REASONIX.md is its own
		// (DocumentNames in internal/state/instruction/resolver.go,
		// MemoryUserDir in internal/contract/config/paths.go, esengine/
		// DeepSeek-Reasonix d460758e)
		t.Instructions = filepath.Join(reasonixState(a), "REASONIX.md")
	case "zed":
		// Zed reads its MCP servers from context_servers in the settings.json
		// magpie sets its model in (~/.config/zed, $XDG_CONFIG_HOME/zed,
		// %APPDATA%\Zed or $MAGPIE_ZED_CONFIG_DIR: agent.zed), and reloads
		// the file as it changes (crates/settings_content/src/project.rs
		// ProjectSettingsContent.context_servers, zed-industries/zed
		// 2c99f547; lc on Discord)
		t.MCP = &mcpFile{Path: a.Path, Format: fmtZed}
	case "kiro-cli":
		// Kiro CLI — and the Kiro IDE, which reads the same files — finds
		// its global skills in ~/.kiro/skills, its user-wide MCP servers
		// in ~/.kiro/settings/mcp.json, mcpServers of command/args/env or
		// url/headers that hot-reloads, and ~/.kiro/steering/AGENTS.md of
		// its global steering, always included (kiro.dev/docs/skills,
		// /docs/mcp/configuration, /docs/steering; #4769). The app is
		// found by the kiro-cli program alone (apps): ~/.kiro is Kiro
		// Crew's and the Kiro IDE's too
		d := filepath.Join(h, ".kiro")
		t.Instructions = filepath.Join(d, "steering", "AGENTS.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "settings", "mcp.json"), Format: fmtKiro}
		t.Skills = filepath.Join(d, "skills")
	case "claude-desktop":
		// Claude Desktop reads only commands from its file: a remote server
		// is added in its own Connectors settings. In its 3p mode (magpie's
		// gateway, or any other) it reads the file in Claude-3p instead, so
		// where that folder is the servers go into both: each mode finds
		// them, a switch between the two leaves nothing behind in either
		t.MCP = &mcpFile{Path: filepath.Join(filepath.Dir(a.Path), "claude_desktop_config.json"), Format: fmtDesktop}
		for _, p := range agent.DesktopConfigs3p(h) {
			if p != t.MCP.Path && isDir(filepath.Dir(p)) {
				t.MCP.Also = append(t.MCP.Also, p)
			}
		}
		// Cowork's skills, in every account's skills-plugin (#638)
		if t.Desktop = desktopSkillRoots(a.Dir); len(t.Desktop) > 0 {
			t.Skills = filepath.Join(t.Desktop[0], "skills")
		}
	default:
		if a.Plugin != "" {
			return pluginTarget(a)
		}
		return nil
	}
	return t
}

// firstThere is the first of names in dir that is there, as an agent that
// reads only the first of them finds it; the first name when none is.
func firstThere(dir string, names ...string) string {
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			return filepath.Join(dir, n)
		}
	}
	return filepath.Join(dir, names[0])
}

// contextFile is the name of the file Qwen Code, or Qoder, reads its
// user-wide instructions from: the first of its settings' context.fileName,
// a name or a list of them, else def (Qwen's packages/cli/src/config/
// config.ts sets it with setMemoryFilename, which trims each name and skips
// an empty one).
func contextFile(settings, def string) string {
	raw, _ := os.ReadFile(settings)
	v := gjson.GetBytes(jsonc.ToJSON(raw), "context.fileName")
	names := []gjson.Result{v}
	if v.IsArray() {
		names = v.Array()
	}
	for _, n := range names {
		if name := strings.TrimSpace(n.String()); n.Type == gjson.String && name != "" {
			return name
		}
	}
	return def
}

// reasonixState is Reasonix's state folder: $REASONIX_STATE_HOME, else
// [storage] state in its config.toml, else its home (Roots.Dir in
// internal/contract/config/storage_roots.go, readStorageDirs in
// storage_config.go).
func reasonixState(a *agent.Agent) string {
	d := appdir.Getenv("REASONIX_STATE_HOME")
	if strings.TrimSpace(d) == "" {
		st, _ := edit.GetTOMLTable(a.Path, "storage")
		d = st["state"]
	}
	if d = strings.TrimSpace(os.ExpandEnv(d)); d == "" {
		return a.Dir
	}
	if d == "~" || strings.HasPrefix(d, "~/") || strings.HasPrefix(d, `~\`) {
		d = filepath.Join(home(), strings.TrimLeft(d[1:], `/\`))
	}
	if abs, err := filepath.Abs(d); err == nil {
		d = abs
	}
	return d
}

// pluginMCP is each MCP format an agent plugin may name
// (agentplug.MCPFormats), by name.
var pluginMCP = map[string]mcpFormat{
	"alma": fmtAlma, "antigravity": fmtAntigravity, "claude": fmtClaude,
	"cline": fmtCline, "codex": fmtCodex, "commandcode": fmtCommandCode,
	"copilot": fmtCopilot, "crush": fmtCrush, "cursor": fmtCursor,
	"devin": fmtDevin, "gemini": fmtGemini, "goose": fmtGoose, "grok": fmtGrok,
	"hermes": fmtHermes, "kimi": fmtKimi, "omp": fmtOmp, "opencode": fmtOpenCode,
	"zed": fmtZed,
}

// pluginTarget is where an agent a plugin adds reads the Library's three,
// as its agent.library says; nil when it names none. A path that isn't
// absolute once ~ and $VAR are expanded is left out: relative to what, no
// one knows.
func pluginTarget(a *agent.Agent) *Target {
	list, _ := agentplug.List()
	var l agentplug.Places
	for _, p := range list {
		if p.Spec == a.Plugin {
			l = p.Desc.Library
		}
	}
	at := func(p string) string {
		if p == "" {
			return ""
		}
		if p = agent.PluginPath(p); !filepath.IsAbs(p) {
			return ""
		}
		return p
	}
	t := &Target{Agent: a, Instructions: at(l.Instructions), Skills: at(l.Skills)}
	if f, ok := pluginMCP[l.MCPFormat]; ok && at(l.MCP) != "" {
		t.MCP = &mcpFile{Path: at(l.MCP), Format: f}
	}
	if t.Instructions == "" && t.MCP == nil && t.Skills == "" {
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
// Windows, and each skill is a link the distro makes to magpie's one copy
// of it there (wsl_skills.go).
func wslTargetOf(a *agent.Agent) *Target {
	h := a.Home
	if h == "" || !agent.WSLRunning(a.WSL) {
		return nil
	}
	t := &Target{Agent: a}
	// a folder there that is a link is opened where it leads (#4502)
	j := func(elem ...string) string { return wslOpen(t, filepath.Join(elem...)) }
	id, _, _ := strings.Cut(a.ID, "@")
	switch id {
	case "claude":
		d := j(h, ".claude")
		t.Instructions = filepath.Join(d, "CLAUDE.md")
		t.MCP = &mcpFile{Path: j(h, ".claude.json"), Format: fmtClaude}
		t.Skills = filepath.Join(d, "skills")
		t.Settings = filepath.Join(d, "settings.json")
	case "codex":
		d := j(h, ".codex")
		t.Instructions = filepath.Join(d, "AGENTS.md")
		t.Override = filepath.Join(d, "AGENTS.override.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "config.toml"), Format: fmtCodex}
		t.Skills = filepath.Join(d, "skills")
	case "pi":
		// its MCP servers go where the Pi installed there reads them, as on
		// this machine (piMCP): its version is what the distro's pi said
		// at the probe, and its extensions are those its settings.json and
		// the distro's npm folders have (leokon on Discord: only the
		// Windows Pi was offered MCP servers)
		d := j(h, ".pi", "agent")
		t.Instructions = firstThere(d, "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD")
		t.Override = filepath.Join(d, "AGENTS.override.md")
		t.MCP, t.MCPVia = piMCP(h, d, piVersion(a), wslPiRoots(h))
		t.Skills = filepath.Join(d, "skills")
	case "omo":
		d := j(h, ".omo", "agent")
		t.Instructions = firstThere(d, "AGENTS.md", "AGENTS.MD", "CLAUDE.md", "CLAUDE.MD")
		t.Override = filepath.Join(d, "AGENTS.override.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "mcp.json"), Format: fmtPiNative}
		t.Skills = filepath.Join(d, "skills")
	case "gemini":
		// as targetOf's, under the distro's home
		d := j(h, ".gemini")
		t.Instructions = filepath.Join(d, "GEMINI.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "settings.json"), Format: fmtGemini}
		t.Skills = filepath.Join(d, "skills")
	case "agy":
		d := j(h, ".gemini", "config")
		t.Instructions = filepath.Join(d, "GEMINI.md")
		t.MCP = &mcpFile{Path: filepath.Join(d, "mcp_config.json"), Format: fmtAntigravity}
		t.Skills = filepath.Join(d, "skills")
	case "crush":
		// its crush.json is the distro's ~/.config/crush's (agent.crushIn),
		// CRUSH.md and skills beside it
		d := filepath.Dir(a.Path)
		t.Instructions = filepath.Join(d, "CRUSH.md")
		t.MCP = &mcpFile{Path: a.Path, Format: fmtCrush}
		t.Skills = filepath.Join(d, "skills")
		t.SkillsAlso = []string{"claude@" + strings.SplitN(a.ID, "@", 2)[1]}
	case "cline":
		// beside its providers.json; the distro's CLINE_MCP_SETTINGS_PATH
		// isn't read
		t.MCP = &mcpFile{Path: filepath.Join(filepath.Dir(a.Path), "cline_mcp_settings.json"), Format: fmtCline}
		t.Skills = filepath.Join(a.Dir, "skills")
	case "codebuddy":
		t.MCP = &mcpFile{Path: codebuddyMCPIn(a.Dir, h), Format: fmtClaude}
		t.Skills = filepath.Join(a.Dir, "skills")
		t.Instructions = firstThere(a.Dir, "CODEBUDDY.md", "CODEBUDDY.mdc")
	case "kimi":
		// its folder is the distro's (agent.kimiDir); the new Kimi Code's
		// AGENTS.md is in it. The skills it shares with other agents (the
		// distro's ~/.agents/skills) aren't given here
		t.MCP = &mcpFile{Path: filepath.Join(a.Dir, "mcp.json"), Format: fmtKimi}
		if filepath.Base(a.Dir) == ".kimi-code" {
			t.Instructions = filepath.Join(a.Dir, "AGENTS.md")
		}
	case "goose":
		// its config.yaml is the distro's ~/.config/goose's; the skills it
		// reads first are the distro's ~/.agents/skills, which aren't
		// given here
		t.Instructions = filepath.Join(filepath.Dir(a.Path), ".goosehints")
		t.MCP = &mcpFile{Path: a.Path, Format: fmtGoose}
	default:
		return wslOwnFolder(a)
	}
	wslOpened(t)
	wslStored(t, h)
	if t.MCP != nil {
		// a Windows program isn't given to it (mcpFile.side)
		t.MCP.WSL, t.MCP.Distro, t.MCP.Home = true, a.WSL, linuxHome(h)
	}
	return t
}

// wslOpen is path, one of t's in a WSL distro, where magpie can open it
// (agent.WSLOpen): below a link the distro has, where the link leads,
// since nothing below one can be opened through \\wsl.localhost. A link
// that couldn't be resolved leaves path as it is and t unreached.
func wslOpen(t *Target, path string) string {
	if path == "" {
		return ""
	}
	if err := t.Agent.Unreached(); err != nil && t.Unreached == nil {
		t.Unreached = err
	}
	o, err := agent.WSLOpen(t.Agent, path)
	if err != nil {
		if t.Unreached == nil {
			t.Unreached = err
		}
		return path
	}
	return o
}

// wslOpened opens each of t's paths where magpie can (wslOpen): a link
// below the agent's folder, its skills folder one, as well as the folder.
func wslOpened(t *Target) {
	for _, p := range []*string{&t.Instructions, &t.Override, &t.Skills, &t.Settings} {
		*p = wslOpen(t, *p)
	}
	if t.MCP != nil {
		t.MCP.Path = wslOpen(t, t.MCP.Path)
		for _, ps := range [][]string{t.MCP.Also, t.MCP.Extra} {
			for i := range ps {
				ps[i] = wslOpen(t, ps[i])
			}
		}
	}
}

// ownFolder are the agents that keep all the library gives them in their
// own folder (a.Dir, or beside a.Path), read from no variable of this
// machine's and from nothing under its home: one magpie found in a distro
// has that folder at the distro's home already, so its target there is
// the one targetOf makes on this machine, given copies (#1323).
var ownFolder = map[string]bool{
	"hermes": true, "omp": true, "opencode": true, "mimocode": true, "droid": true,
	"grok": true, "commandcode": true, "atomcode": true, "qoder": true, "qoder-cn": true,
	"minimax-code": true, "fx": true, "dsh": true, "qwen": true,
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
	t.Agent, t.Copy = a, false
	wslOpened(t)
	wslStored(t, a.Home)
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
// magpie sets up: known by the folder they keep their settings in
// (Claude Desktop), or by their program alone (Kiro CLI).
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
	out := []*agent.Agent{
		{ID: "claude-desktop", Name: "Claude Desktop", Icon: "claude-color", Dir: dir, Path: filepath.Join(dir, "claude_desktop_config.json")},
	}
	// Kiro CLI (#4769) is no agent magpie wires a model into, but the
	// library can give it skills, MCP servers and instructions. It is
	// found by the kiro-cli program alone — PATH, ~/.local/bin, Kiro
	// CLI.app, where provider.KiroExecutable looks — never by ~/.kiro,
	// which Kiro Crew and the Kiro IDE keep too. Bin is the path itself,
	// so a copy the installer put where magpie's PATH doesn't reach is
	// found as it was (Detected looks a Bin up by PATH alone). It is
	// listed when kiro-cli isn't found too, and then not Detected, so not
	// a target: held still lists one uninstalled with what magpie gave it,
	// for that to be unticked and taken out of ~/.kiro
	bin := provider.KiroExecutable()
	if bin == "" {
		bin = "kiro-cli"
	}
	out = append(out, &agent.Agent{ID: "kiro-cli", Name: "Kiro CLI", Icon: "kiro-color", Bin: bin})
	return out
}

// Targets are the agents on this machine that magpie can give any of the
// three to, in the order the rest of magpie lists them.
func Targets() []*Target { return targets(false) }

// everyTarget is Targets with the agents gone for their CLI too: a folder
// an uninstall left behind is still that agent's own, which a project or
// an instruction set is never kept in.
func everyTarget() []*Target { return targets(true) }

func targets(gone bool) []*Target {
	out, _ := targetsAsleep(gone)
	return out
}

// targetsAsleep is targets, and the agents in WSL distros that aren't
// running now: none is a target, as its files can't be opened without
// starting the distro, but the page lists them (asleepView). Each running
// one's places are kept for when it is stopped (keepShape).
func targetsAsleep(gone bool) ([]*Target, []*agent.Agent) {
	var out []*Target
	var asleep []*agent.Agent
	own := map[string]bool{}
	for _, a := range apps() {
		own[a.ID] = true
	}
	for _, a := range agent.All() {
		// the agents here, and a folder an agent shares with its vendor's
		// other app when only that app is here: the Cursor editor reads
		// ~/.cursor's mcp.json and skills with no cursor-agent installed
		if !a.Detected() && !a.SharedHere() {
			continue
		}
		// an app magpie also sets up as an agent (Claude Desktop) keeps its
		// MCP servers where apps says, once
		if own[a.ID] {
			continue
		}
		// an uninstall that left the folder behind (#2177, #2173): the
		// Agents page calls the agent gone and offers none of its settings,
		// since nothing reads them; the library gives nothing to it either
		if !gone && a.CLIMissing() {
			continue
		}
		if isAsleep(a) {
			asleep = append(asleep, a)
			noteUp(a.WSL, false)
			continue
		}
		if t := targetOf(a); t != nil && t.open() {
			out = append(out, t)
		}
	}
	for _, a := range apps() {
		if !a.Detected() && !a.SharedHere() {
			continue
		}
		if t := targetOf(a); t != nil && t.open() {
			out = append(out, t)
		}
	}
	readsWith(out)
	for _, t := range out {
		if t.Agent.WSL != "" {
			keepShape(t)
		}
	}
	return out, asleep
}

// held are the agents not among have (Targets) — uninstalled, or gone for
// their CLI — that the library still gives something, or that hold a skill,
// server or hook magpie put there: each is listed on the rows of what it
// has, for that to be unticked and taken out of it, and on no other row
// (@jiahao_jayden on X: an agent that isn't installed was on every row). A
// sync gives them nothing new (Gone).
func (l *Library) held(have []*Target) []*Target {
	in := map[string]bool{}
	for _, t := range have {
		in[t.Agent.ID] = true
	}
	has := func(id string) bool {
		if a := l.Applied[id]; a != nil && (len(a.Skills) > 0 || len(a.MCP) > 0 || len(a.Hooks) > 0) {
			return true
		}
		return slices.ContainsFunc(l.Skills, func(s *Skill) bool { return s.gives(id) }) ||
			slices.ContainsFunc(l.MCP, func(s *Server) bool { return slices.Contains(s.Agents, id) }) ||
			slices.ContainsFunc(l.Hooks, func(h *Hook) bool { return slices.Contains(h.Agents, id) })
	}
	var out []*Target
	for _, a := range append(apps(), agent.All()...) {
		if in[a.ID] || a.WSL != "" || !has(a.ID) {
			continue
		}
		in[a.ID] = true
		if t := targetOf(a); t != nil && t.open() {
			t.Gone = true
			out = append(out, t)
		}
	}
	return out
}

// readsShared are the agents that read ~/.agents/skills as well as their
// own folder (each one's docs or source): Kimi Code's own is
// ~/.kimi-code/skills besides it (#4379). OmO's engine, senpi, a fork of
// Pi from after Pi 0.54 (its oldest on npm is 0.74.0), reads it as Pi
// does (core/package-manager.js, userAgentsSkillsDir, @code-yeongyu/senpi
// 2026.5.13).
var readsShared = []string{"codex", "gemini", "opencode", "deveco", "crush", "dsh", "commandcode", "devin", "droid", "cline", "grok", "grok-app", "fx", "alma", "kimi", "omo"}

// Pi reads ~/.agents/skills from 0.54.0 (2026-02-19, its CHANGELOG: "Added
// default skill auto-discovery for .agents/skills locations … global
// skills from ~/.agents/skills"; userAgentsSkillsDir in
// dist/core/package-manager.js, @earendil-works/pi-coding-agent 1.5.0),
// and omp from 11.0.0 (src/discovery/agents.ts, "Load skills from
// ~/.agents/skills"; 10.6.2 has no such provider). A Pi or omp whose
// version isn't known is taken for one before, as magpie took every Pi:
// it is given its own link, which a Pi from 0.68 reads once with the
// shared folder's (it dedupes skills by their real path).
const (
	piSharedSince  = "0.54.0"
	ompSharedSince = "11.0.0"
)

// ompVersion is the version of the omp on PATH, "" when not known.
var ompVersion = func(a *agent.Agent) string { return a.InstalledVersion() }

// since is whether version v, when known, is at or after first.
func since(v, first string) bool { return v != "" && !agent.Newer(first, v) }

// readsSharedDir is whether the agent reads ~/.agents/skills besides its
// own folder, so a skill kept there reaches it whatever is ticked.
func (t *Target) readsSharedDir() bool {
	return t.SharedToo || slices.Contains(readsShared, t.Agent.ID)
}

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
			if real[j] == real[i] || real[j] == shared && t.readsSharedDir() {
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
		t.Instructions, t.Override, t.Note, t.Profile = "", "", "", false
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
	for _, x := range apps() {
		if strings.EqualFold(x.ID, q) || strings.EqualFold(x.Name, q) {
			a = x
		}
	}
	app := a != nil
	if a == nil {
		var err error
		if a, err = agent.Find(q); err != nil {
			return "", err
		}
	}
	what := map[string]string{"instructions": "instructions", "mcp": "MCP servers", "skills": "skills"}[kind]
	// an app found by its program alone (Kiro CLI) that isn't here: told
	// so, not recorded and then given nothing, as Targets leaves it out
	if app && a.Bin != "" && !a.Detected() {
		return "", fmt.Errorf("%s isn't installed here; nothing reads its %s until it is", a.Name, what)
	}
	// gone for its CLI, as Targets leaves it out (#2254): told so, not
	// recorded and then given nothing ("every agent already has it")
	if a.CLIMissing() {
		return "", fmt.Errorf("%s's CLI isn't installed here; nothing reads its %s until it is installed again%s", a.Name, what, takenBy(kind, what))
	}
	t := targetOf(a)
	var has bool
	if t == nil && isAsleep(a) {
		// one magpie saw it give instructions, MCP servers or skills a
		// place to is recorded, and given them when the distro next runs
		// (Woken)
		if sh := shapeOf(a); sh != nil && (kind == "instructions" && sh.Instructions != "" || kind == "mcp" && sh.MCP != "" || kind == "skills" && sh.Skills != "") {
			return a.ID, nil
		}
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
	case a.Plugin != "":
		return fmt.Sprintf("%s's plugin (%s) says no place for %s: its agent.library.%s names none", a.Name, plugin.Name(a.Plugin), what, kind)
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

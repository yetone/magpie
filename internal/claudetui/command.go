package claudetui

// Command is the hidden subcommand that runs it.
const Command = "claude-tui-helper"

// TrustEnv, "1", has the shim tell Claude Code yes when it asks whether
// to trust the folder it runs in: the bridge sets it for magpie's own
// folder, and for a caller's in an account folder of magpie's, whose
// Claude Code no one else runs. Otherwise the question is the user's to
// answer in their own Claude Code, and the run ends, saying so.
const TrustEnv = "MAGPIE_CLAUDE_TUI_TRUST"

// BypassEnv, "1", has the shim tell Claude Code yes when it asks to accept
// running in Bypass Permissions mode (a user's claude that is a wrapper
// adding --dangerously-skip-permissions, or an account whose default mode
// it is; #5042). Claude Code records the acceptance in its config folder's
// settings.json, for every run after, so the bridge sets it only for an
// account folder of magpie's, whose Claude Code no one else runs. In the
// account Claude Code is signed in to the acceptance is its user's to give,
// and the run ends at once, saying so.
const BypassEnv = "MAGPIE_CLAUDE_TUI_BYPASS"

// DeadlineEnv holds, in Unix milliseconds, when a one-off run's caller
// (a /usage read, a test's "hi") stops waiting for it. The helper ends
// then on its own, saying on its stderr what Claude Code hadn't done and
// what it last drew. Killed at the caller's deadline instead, it said
// nothing, and the reading's error was its exit code alone, "exit status
// 1" on Windows (#4830).
const DeadlineEnv = "MAGPIE_CLAUDE_TUI_BY"

// Untrusted begins what the helper says, on its stderr, as it ends over a
// folder it wasn't told to trust: the bridge then runs the turn in a
// folder of magpie's.
const Untrusted = "Claude Code asks whether to trust the folder"

// EndedEarly begins what the helper says as it ends over a Claude Code
// that ended itself before it showed its prompt, as one that can't load
// the session it was told to go on with does. Every other way the
// interactive mode doesn't come up (no prompt in ReadyWait, a sign-in or
// a question only its user answers) is the same for a new Claude Code
// told the whole conversation: the bridge tries one again only on this.
const EndedEarly = "Claude Code ended before it showed its prompt"

// WSLEnv holds, as JSON (WSL), how to run a Claude Code that is in a WSL
// distro: the helper, on Windows, runs wsl.exe in its pseudo console.
const WSLEnv = "MAGPIE_CLAUDE_TUI_WSL"

// WSL is a Claude Code in a WSL distro, as the helper runs it.
type WSL struct {
	// Args are wsl.exe's arguments up to Claude Code's own, its path last
	Args []string `json:"args"`
	// Mount is where the distro mounts Windows' drives ("/mnt/"): an
	// image's path is pasted as Claude Code there reads it
	Mount string `json:"mount"`
	// Projects is Claude Code's projects folder, as Windows reads it; ""
	// with Distro set when Windows can't, or its home is unknown: the
	// transcript is then read inside the distro (wslread.go)
	Projects string `json:"projects,omitempty"`
	// Distro is the distro, as wsl.exe -d names it
	Distro string `json:"distro,omitempty"`
}

// MCPCommand is the hidden subcommand of magpie's tool server, the MCP
// server a run's --mcp-config names "magpie" (claudebridge.RunMCP). Its
// arguments are the callback's file, the tools' file and, for the
// interactive mode, a file it writes once Claude Code has listed its
// tools: the first message waits for it (MinhuWang on X: a first message
// sent before then had none of the agent's tools).
const MCPCommand = "claude-mcp-helper"

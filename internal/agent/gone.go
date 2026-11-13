package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yetone/magpie/internal/claudecode"
	"github.com/yetone/magpie/internal/proc"
)

// An agent uninstalled outside magpie leaves its folder behind (~/.dsh,
// ~/.hermes/config.yaml, ~/.claude), and detection, which goes by that
// folder, still lists it. Such an agent is shown as its CLI gone, with
// nothing of its settings offered, since nothing would read them (#2177,
// mengyianx; #2173, SkyTodInfi: Hermes listed by its config.yaml alone).
// Only where magpie can tell the folder holds nothing a program still
// reads: an agent whose folder an app, an editor's extension or another
// program keeps too is gone only when none of those is here either.

// cliOnly are the agents with no install command magpie offers whose
// folder only their CLI keeps, and whose installer puts it where
// cliHeld looks: Hermes' install.sh clones itself into <home>/hermes-agent
// and links ~/.local/bin/hermes to it.
var cliOnly = map[string]bool{"hermes": true}

// goneOS is the system cliHeld judges for; a var so a test on a Mac can
// judge for Windows.
var goneOS = runtime.GOOS

// cliHeld reports whether something here is the agent, or reads its
// folder as the agent does, so the folder is no leftover; known is false
// when magpie can't tell, which never counts as gone.
func (a *Agent) cliHeld() (held, known bool) {
	if a.Bin != "" && (a.binsOwn(binAt(a.Bin)) || a.binsOwn(proc.FindTool(a.Bin))) {
		if a.ID != "goose" {
			return true, true
		}
		// pressly's goose, a Go program, is not Block's (see goose's detect)
		for _, bin := range []string{binAt(a.Bin), proc.FindTool(a.Bin)} {
			if bin != "" && !goProgram(bin) {
				return true, true
			}
		}
	}
	if a.HasApp() {
		return true, true
	}
	if a.sign != nil && a.sign() {
		return true, true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, false
	}
	// where the vendor's own installer puts it, off PATH until a new shell
	var at []string
	switch a.ID {
	case "claude":
		at = []string{filepath.Join(home, ".claude", "local", exe("claude"))}
		// the Claude Code magpie downloaded for a machine with none (a
		// server's, claudecode.Install): magpie's own runs make ~/.claude,
		// and `magpie claude` starts it, so the folder is no leftover. A
		// systemd service's Agents page said Claude's CLI was not found
		// while that Claude Code answered its requests (Wing8Aqua on X).
		if p, _ := claudecode.Downloaded(); p != "" {
			at = append(at, p)
		}
	case "opencode":
		at = []string{filepath.Join(home, ".opencode", "bin", exe("opencode"))}
	case "codex":
		at = []string{filepath.Join(home, ".codex", "packages", "standalone")}
	case "hermes":
		at = []string{filepath.Join(a.Dir, "hermes-agent")}
		// a profile magpie connects (hermesActive): the checkout is in
		// the root its profiles are under
		if filepath.Base(filepath.Dir(a.Dir)) == "profiles" {
			at = append(at, filepath.Join(filepath.Dir(filepath.Dir(a.Dir)), "hermes-agent"))
		}
	}
	for _, p := range at {
		if _, err := os.Stat(p); err == nil {
			return true, true
		}
	}
	// On Windows the programs that read a shared folder can't all be
	// told: a Store app, an editor run portable or with its extensions
	// moved off ~\.vscode, a CLI on a PATH only a shell profile sets (fnm).
	// Taking ~\.claude and ~\.codex for leftovers there hid both agents'
	// settings, "CLI not found", from a user whose Claude Code and Codex
	// v0.1.1231 listed (#1482, edussss). There a shared folder whose CLI
	// isn't found is never taken for gone. A folder an agent's desktop app
	// reads is one too: DeepSeek Harness Desktop installs to any folder
	// and puts its dsh.cmd in one the user picks, and taking ~\.dsh for a
	// leftover hid dsh's settings from v0.1.1239 (#2300, yangkai2; #2304,
	// wlj521: 未找到 CLI, no model to pick).
	_, hasApp := desktopApps[a.ID]
	if goneOS == "windows" && (cliShared[a.ID] || hasApp) {
		return false, false
	}
	if !cliShared[a.ID] {
		return false, true
	}
	// the others that keep a shared folder: editors' extensions, desktop
	// apps, and the IDEs and editors whose agents bring their own copy of
	// the program
	switch a.ID {
	case "claude":
		if extensionHere(home, "anthropic.claude-code") || appHere("Claude.app") {
			return true, true
		}
		return false, !jetBrainsHere(home) && !zedAgentsHere(home)
	case "codex":
		if extensionHere(home, "openai.chatgpt") {
			return true, true
		}
		return false, !jetBrainsHere(home) && !zedAgentsHere(home)
	case "cline":
		if extensionHere(home, "saoudrizwan.claude-dev") {
			return true, true
		}
		return false, !jetBrainsHere(home)
	case "opencode":
		// its desktop app: known by its bundle on a Mac only
		if runtime.GOOS != "darwin" {
			return false, false
		}
		return appHere("OpenCode.app", "OpenCode Beta.app", "OpenCode Dev.app"), true
	case "goose":
		// Goose Desktop: known by its bundle on a Mac only
		if runtime.GOOS != "darwin" {
			return false, false
		}
		return appHere("Goose.app"), true
	}
	// Gemini CLI's folder is Antigravity's and Gemini Code Assist's too,
	// and Gemini is listed only by its program (gemini.go)
	return false, false
}

// extensionHere reports whether an editor here has the extension id
// installed: VS Code's ~/.vscode/extensions/<id>-<version>, and the same
// in each fork's (~/.cursor, ~/.windsurf, ~/.vscode-oss, ~/.trae, …) and
// in VS Code Server's.
func extensionHere(home, id string) bool {
	es, err := os.ReadDir(home)
	if err != nil {
		return false
	}
	for _, e := range es {
		if !strings.HasPrefix(e.Name(), ".") {
			continue
		}
		xs, err := os.ReadDir(filepath.Join(home, e.Name(), "extensions"))
		if err != nil {
			continue
		}
		for _, x := range xs {
			if n := strings.ToLower(x.Name()); x.IsDir() && strings.HasPrefix(n, id+"-") {
				return true
			}
		}
	}
	return false
}

// appHere reports whether one of the bundles names is in a Mac's app
// folders, or a folder of theirs one level down (inAppFolders).
func appHere(names ...string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	for _, p := range inAppFolders(appFolders(), names...) {
		if isDir(p) {
			return true
		}
	}
	return false
}

// jetBrainsHere reports whether a JetBrains IDE has kept its settings here:
// its AI Assistant's agents (Claude Agent, Codex) and Cline's plugin read
// the agents' folders, with a program of their own.
func jetBrainsHere(home string) bool {
	var dirs []string
	switch runtime.GOOS {
	case "darwin":
		dirs = []string{filepath.Join(home, "Library", "Application Support", "JetBrains")}
	default:
		dirs = []string{filepath.Join(home, ".config", "JetBrains"), filepath.Join(home, ".local", "share", "JetBrains")}
	}
	for _, d := range dirs {
		if isDir(d) {
			return true
		}
	}
	return false
}

// zedAgentsHere reports whether Zed has fetched an external agent: its
// Claude Code and Codex adapters run their own copy of the program, which
// reads ~/.claude or ~/.codex.
func zedAgentsHere(home string) bool {
	var d string
	switch runtime.GOOS {
	case "darwin":
		d = filepath.Join(home, "Library", "Application Support", "Zed")
	default:
		d = filepath.Join(home, ".local", "share", "zed")
	}
	return isDir(filepath.Join(d, "external_agents"))
}

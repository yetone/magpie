package agent

// The agents magpie knows that aren't on this machine, with the commands
// their vendors give to install them (#727): on a new computer the Agents
// page said only "Install Claude Code, Codex, Gemini CLI, OpenCode…", and
// which command installs each was left to be looked up. magpie shows the
// commands to copy and run in a terminal; it doesn't run them itself, as an
// install needs what a new machine may not have yet (Node.js for npm,
// Homebrew), and a global npm prefix that may want sudo.

import (
	"runtime"
)

// InstallCmd is one way to install an agent's CLI.
type InstallCmd struct {
	// Via is what runs it: script (the vendor's installer, in a shell),
	// powershell (the vendor's installer, on Windows), brew or npm
	Via     string `json:"via"`
	Command string `json:"command"`
}

// Install is an agent not on this machine, and how to install it.
type Install struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Icon     string       `json:"icon,omitempty"`
	Commands []InstallCmd `json:"commands"`
}

// vendorInstall is the installer each vendor's own docs give first, where
// it isn't a package manager's — Claude Code's (code.claude.com/docs/en/setup),
// Codex's (github.com/openai/codex's README) and OpenCode's
// (opencode.ai/docs), the ones whose own updaters cliSpecs knows by where
// they put the binary — and the Homebrew cask and formula the READMEs name
// (Codex's, Gemini CLI's).
var vendorInstall = map[string]func(goos string) []InstallCmd{
	"claude": func(goos string) []InstallCmd {
		if goos == "windows" {
			return []InstallCmd{{Via: "powershell", Command: "irm https://claude.ai/install.ps1 | iex"}}
		}
		return []InstallCmd{{Via: "script", Command: "curl -fsSL https://claude.ai/install.sh | bash"}}
	},
	"opencode": func(goos string) []InstallCmd {
		if goos == "windows" {
			return nil
		}
		return []InstallCmd{{Via: "script", Command: "curl -fsSL https://opencode.ai/install | bash"}}
	},
	"codex": func(goos string) []InstallCmd {
		switch goos {
		case "windows":
			return []InstallCmd{{Via: "powershell", Command: `powershell -ExecutionPolicy ByPass -c "irm https://chatgpt.com/codex/install.ps1 | iex"`}}
		case "darwin":
			return []InstallCmd{{Via: "script", Command: "curl -fsSL https://chatgpt.com/codex/install.sh | sh"}, {Via: "brew", Command: "brew install --cask codex"}}
		}
		return []InstallCmd{{Via: "script", Command: "curl -fsSL https://chatgpt.com/codex/install.sh | sh"}}
	},
	"gemini": func(goos string) []InstallCmd {
		if goos == "windows" {
			return nil
		}
		return []InstallCmd{{Via: "brew", Command: "brew install gemini-cli"}}
	},
}

// installCommands is how to install the agent with the given id on goos:
// the vendor's installer first, then npm's package, the one cliSpecs
// updates; nil for one magpie knows no command for.
func installCommands(id, goos string) []InstallCmd {
	var out []InstallCmd
	if f := vendorInstall[id]; f != nil {
		out = append(out, f(goos)...)
	}
	if spec, ok := cliSpecs[id]; ok && len(spec.npm) > 0 {
		out = append(out, InstallCmd{Via: "npm", Command: "npm install -g " + spec.npm[0]})
	}
	return out
}

// Installs is every agent magpie knows a command for that isn't on this
// machine, in the Agents page's order.
func Installs() []Install {
	return installsOf(All(), runtime.GOOS)
}

func installsOf(all []*Agent, goos string) []Install {
	out := []Install{}
	for _, a := range all {
		if a.WSL != "" || a.Detected() {
			continue
		}
		cmds := installCommands(a.ID, goos)
		if len(cmds) == 0 {
			continue
		}
		out = append(out, Install{ID: a.ID, Name: a.Name, Icon: a.Icon, Commands: cmds})
	}
	return out
}

package agent

// The agents magpie knows that aren't on this machine, with the commands
// their vendors give to install them (#727): on a new computer the Agents
// page said only "Install Claude Code, Codex, Gemini CLI, OpenCode…", and
// which command installs each was left to be looked up. magpie shows the
// commands to copy and run in a terminal; it doesn't run them itself, as an
// install needs what a new machine may not have yet (Node.js for npm,
// Homebrew), and a global npm prefix that may want sudo.
//
// Where no Node.js is found, each npm command installs it first (#727,
// Sun1090: "curl xxx && npm xxxx"): nvm's installer and its LTS Node on a
// Mac or Linux, as nodejs.org's download page gives, winget's Node.js LTS
// on Windows; the npm install follows in the same line.

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/yetone/magpie/internal/proc"
)

// InstallCmd is one way to install an agent's CLI.
type InstallCmd struct {
	// Via is what runs it: script (the vendor's installer, in a shell),
	// powershell (the vendor's installer, on Windows), brew or npm
	Via     string `json:"via"`
	Command string `json:"command"`
	// Node is how the command installs Node.js before npm, where none is
	// here: nvm (Linux), nvm-mac (a Mac, where nvm's installer wants the
	// Xcode Command Line Tools) or winget (Windows); "" when it doesn't
	Node string `json:"node,omitempty"`
}

// Install is an agent not on this machine, and how to install it.
type Install struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Icon     string       `json:"icon,omitempty"`
	Commands []InstallCmd `json:"commands"`
	// Missing: the agent's settings are here but its CLI isn't (CLIMissing),
	// so it is listed on the Agents page and offered here again too
	Missing bool `json:"missing,omitempty"`
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
// updates, after Node.js's own install when node is false; nil for one
// magpie knows no command for.
func installCommands(id, goos string, node bool) []InstallCmd {
	var out []InstallCmd
	if f := vendorInstall[id]; f != nil {
		out = append(out, f(goos)...)
	}
	if spec, ok := cliSpecs[id]; ok && len(spec.npm) > 0 {
		out = append(out, npmInstall(spec.npm[0], goos, node))
	}
	return out
}

// nvmVersion is the nvm release whose installer the commands fetch
// (github.com/nvm-sh/nvm/releases/latest).
const nvmVersion = "v0.40.8"

// npmInstall is the command installing pkg with npm on goos, with Node.js
// installed first when node is false.
//
// On a Mac or Linux: nvm's installer, then nvm.sh read into the shell the
// command runs in (the installer adds it to the profile, which only the
// terminals opened after read), its LTS Node, then npm. NVM_DIR is set
// and its folder made, as the installer would go by $XDG_CONFIG_HOME/nvm
// where that is set, and nvm.sh couldn't be found then. nvm's Node is the
// user's own, so the global install wants no sudo.
//
// On Windows, in PowerShell: winget's Node.js LTS, then PATH read again
// from the registry (the installer adds Node's folder there, not to this
// terminal), then npm.cmd — npm alone is npm.ps1 there, which PowerShell's
// default execution policy refuses to run. Each step is parted by ";", as
// Windows PowerShell 5.1 has no "&&".
func npmInstall(pkg, goos string, node bool) InstallCmd {
	plain := "npm install -g " + pkg
	switch {
	case node:
		return InstallCmd{Via: "npm", Command: plain}
	case goos == "windows":
		return InstallCmd{Via: "npm", Node: "winget", Command: "winget install -e --id OpenJS.NodeJS.LTS --accept-source-agreements --accept-package-agreements; " +
			"$env:Path = [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User'); " +
			"npm.cmd install -g " + pkg}
	}
	how := "nvm"
	if goos == "darwin" {
		how = "nvm-mac"
	}
	return InstallCmd{Via: "npm", Node: how, Command: `export NVM_DIR="$HOME/.nvm" && mkdir -p "$NVM_DIR" && ` +
		"t=$(mktemp) && curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/" + nvmVersion + `/install.sh -o "$t" && ` +
		`bash "$t" && . "$NVM_DIR/nvm.sh" && nvm install --lts && ` + plain}
}

// nodeHere says whether this machine has Node.js's npm: on PATH, in one
// of the folders a user's tools go in (nvm's, fnm's, mise's, volta's,
// Homebrew's, …) or, on Windows, in the PATH a terminal opened now has
// and in Node.js's installer's own folder.
func nodeHere() bool {
	if _, err := exec.LookPath("npm"); err == nil {
		return true
	}
	dirs := proc.UserBinDirs()
	if runtime.GOOS == "windows" {
		dirs = append(dirs, proc.LoginPath()...)
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			dirs = append(dirs, filepath.Join(pf, "nodejs"))
		}
	}
	return npmIn(dirs, runtime.GOOS)
}

// npmIn says whether one of dirs has npm in it.
func npmIn(dirs []string, goos string) bool {
	name := "npm"
	if goos == "windows" {
		name = "npm.cmd"
	}
	return slices.ContainsFunc(dirs, func(d string) bool {
		return d != "" && isFile(filepath.Join(d, name))
	})
}

// Installs is every agent magpie knows a command for that isn't on this
// machine, in the Agents page's order.
func Installs() []Install {
	return installsOf(All(), runtime.GOOS, nodeHere())
}

func installsOf(all []*Agent, goos string, node bool) []Install {
	out := []Install{}
	for _, a := range all {
		missing := a.CLIMissing()
		if a.WSL != "" || a.Detected() && !missing {
			continue
		}
		cmds := installCommands(a.ID, goos, node)
		if len(cmds) == 0 {
			continue
		}
		out = append(out, Install{ID: a.ID, Name: a.Name, Icon: a.Icon, Commands: cmds, Missing: missing})
	}
	return out
}

// cliShared are the agents whose folder an app or an editor's extension
// of theirs keeps too, with a CLI of its own inside or none: ~/.codex (the
// Codex app), ~/.claude (Claude Desktop's Code, the editors' extensions),
// ~/.gemini (Antigravity, Gemini Code Assist), OpenCode's (its desktop
// app), ~/.cline (Cline's extensions), Goose's (Goose Desktop). Their
// folder without the CLI is no sign it was uninstalled.
var cliShared = map[string]bool{"codex": true, "claude": true, "gemini": true, "opencode": true, "cline": true, "goose": true}

// CLIMissing reports an agent that is here by its settings alone: its
// folder or config is, its command-line program isn't — not on PATH nor
// where users' tools go (proc.FindTool), as after an uninstall that left
// ~/.dsh behind (#843). Only for an agent that is its CLI and that magpie
// knows an install command for, so the Agents page can offer it again; one
// whose desktop app is installed (HasApp) is that app's settings, which need
// no CLI (DeepSeek Harness Desktop, star on Discord).
func (a *Agent) CLIMissing() bool {
	if a.WSL != "" || a.detect != nil || a.Bin == "" || cliShared[a.ID] {
		return false
	}
	if a.HasApp() {
		return false
	}
	if len(installCommands(a.ID, runtime.GOOS, true)) == 0 {
		return false
	}
	return a.Detected() && proc.FindTool(a.Bin) == ""
}

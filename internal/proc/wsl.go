package proc

import (
	"errors"
	"os/exec"
	"strings"

	"github.com/yetone/magpie/internal/settings"
)

// ErrWSLOff is what a wsl.exe start ends with, unrun, while Settings'
// Detect agents in WSL is off.
var ErrWSLOff = errors.New("not run: Detect agents in WSL is off in Settings")

// wslOff refuses cmd when it is wsl.exe and Settings' Detect agents in WSL
// is off (#1264). Every command magpie starts is made here, so this is the
// one place that holds for all of them: a request's Claude Code stand-in
// and classifier asked wsl.exe which distros run past the gates in agent
// and wslrun, and on a computer without WSL Windows' own wsl.exe answers
// each ask with a console window offering to install it, one per request
// (#4872).
func wslOff(cmd *exec.Cmd, name string) {
	if isWSL(name) && settings.Shared().NoWSLAgents {
		cmd.Err = ErrWSLOff
	}
}

// isWSL is whether name is wsl.exe, by name or path, in any case.
func isWSL(name string) bool {
	base := name[strings.LastIndexAny(name, `/\`)+1:]
	return strings.EqualFold(base, "wsl.exe") || strings.EqualFold(base, "wsl")
}

package fasttools

import "os/exec"

// ownGroup: on Windows proc.CommandContext ends the command's whole tree
// with its context already.
func ownGroup(*exec.Cmd) {}

// shellArgs run a command line in PowerShell, as Antigravity runs its own
// commands on Windows.
func shellArgs(line string) (string, []string) {
	return "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", line}
}

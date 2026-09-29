package proc

import (
	"context"
	"os/exec"
	"strings"
)

// listCommand asks WMI, through PowerShell, for the processes named
// prefix*, one "pid command line" line each; a process whose command line
// isn't readable (another user's) prints its id alone.
func listCommand(ctx context.Context, prefix string) *exec.Cmd {
	filter := ""
	if prefix != "" {
		filter = ` -Filter "Name like '` + strings.ReplaceAll(prefix, "'", "''") + `%'"`
	}
	return CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command",
		`Get-CimInstance Win32_Process`+filter+` | ForEach-Object { "$($_.ProcessId) $($_.CommandLine)" }`)
}

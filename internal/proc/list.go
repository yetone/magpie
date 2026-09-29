package proc

import (
	"context"
	"strconv"
	"strings"
)

// A Process is one running on this computer, as the system lists it: its
// id and its command line, arguments joined by spaces.
type Process struct {
	PID  int
	Args string
}

// List lists the running processes (Windows: only those named prefix*,
// since asking for all is slow there; elsewhere prefix is ignored). An
// error says the system couldn't be asked, not that nothing runs.
func List(ctx context.Context, prefix string) ([]Process, error) {
	out, err := listCommand(ctx, prefix).Output()
	if err != nil {
		return nil, err
	}
	return parseList(string(out)), nil
}

// parseList reads lines of "pid command line", as ps -o pid=,args= and the
// PowerShell listing print them; a line without a pid is skipped.
func parseList(out string) []Process {
	var ps []Process
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		id, args, _ := strings.Cut(line, " ")
		pid, err := strconv.Atoi(id)
		if err != nil || pid <= 0 {
			continue
		}
		ps = append(ps, Process{PID: pid, Args: strings.TrimSpace(args)})
	}
	return ps
}

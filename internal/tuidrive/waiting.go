package tuidrive

import (
	"path/filepath"
	"strings"
	"time"
)

// Running is a command the agent runs, and how long it has run.
type Running struct {
	Age  time.Duration
	Args string
}

// Program is the name of the program args run, and of the script an
// interpreter runs: never the rest of its arguments, which can carry a
// secret.
func Program(args string) string {
	f := strings.Fields(args)
	if len(f) == 0 {
		return "a command"
	}
	name := filepath.Base(f[0])
	switch strings.TrimRight(name, "0123456789.") {
	case "sh", "bash", "zsh", "dash", "node", "bun", "python", "ruby", "perl":
		if len(f) > 1 && !strings.HasPrefix(f[1], "-") {
			name += " " + filepath.Base(f[1])
		}
	}
	return name
}

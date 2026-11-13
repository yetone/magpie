package library

import (
	"fmt"
	"maps"
	"slices"
)

// A synced command server can be started its own way on this computer
// (#3836, #6426; jinpwng on X: the same server on a Mac and in WSL, each
// with its own program path). Its name, agents, note and tools stay the
// one server every computer has; only how this computer starts it is its
// own.
//
//   - Command, Args and Env are this computer's, so every reader (the
//     agents' files, projects, the router, the check) runs what this
//     computer runs with no change of its own.
//   - Shared holds the synced start. Collect carries Shared in their place
//     and never the field itself, so another computer gets what it had.
//   - Put keeps this computer's start: what comes in for the server goes
//     into Shared. A server that comes in as a URL takes the server back to
//     what came in, since there is no command to start.
//   - Turning it off puts the synced start back (ServerHere with none).

// Start is how a command server is started: its program, the program's
// arguments and its environment.
type Start struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// startOf is a server's own start, copied.
func startOf(s *Server) *Start {
	return &Start{Command: s.Command, Args: slices.Clone(s.Args), Env: maps.Clone(s.Env)}
}

// carriedStart is the server as a carried library has it: the synced
// start in place of this computer's own.
func carriedStart(s *Server) *Server {
	c := *s
	if s.Shared != nil {
		c.Command, c.Args, c.Env = s.Shared.Command, slices.Clone(s.Shared.Args), maps.Clone(s.Shared.Env)
	}
	c.Shared = nil
	return &c
}

// keepStart gives a server that came in this computer's own start, when
// this computer had one for the server by its name: what came in becomes
// the synced start.
func keepStart(c, here *Server) {
	c.Shared = nil
	if here == nil || here.Shared == nil || c.Transport != "stdio" {
		return
	}
	c.Shared = startOf(c)
	c.Command, c.Args, c.Env = here.Command, slices.Clone(here.Args), maps.Clone(here.Env)
}

// ServerHere starts a synced command server with cmd (a program and its
// arguments) on this computer alone, the other computers keeping the
// synced one; with no cmd it goes back to the synced one.
func ServerHere(name string, cmd []string) (*Result, error) {
	return change(func(l *Library) error {
		s := l.server(name)
		switch {
		case s == nil:
			return fmt.Errorf("no server called %s", name)
		case len(cmd) == 0:
			if s.Shared != nil {
				s.Command, s.Args, s.Env = s.Shared.Command, s.Shared.Args, s.Shared.Env
				s.Shared = nil
			}
			return nil
		case s.Transport != "stdio":
			return fmt.Errorf("%s is reached by URL: only a command server is started its own way here", name)
		case s.Local:
			return fmt.Errorf("%s is kept to this computer already: its command is this computer's alone", name)
		}
		n := *s
		if n.Shared == nil {
			n.Shared = startOf(s)
		}
		n.Command, n.Args = cmd[0], slices.Clone(cmd[1:])
		if err := n.check(); err != nil {
			return err
		}
		*s = n
		return nil
	})
}

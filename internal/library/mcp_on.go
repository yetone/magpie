package library

import (
	"fmt"
	"slices"
)

// ServerOn turns a server off for everyone, or on again as it was
// (swang20201 on X: a server in the Library could only be removed). Off,
// no agent is given it, nor a project's, nor an app at the MCP router's
// address, and the router ends it; the agents it had are kept (Paused),
// with its command or address, env, headers, tools turned off and
// magpie's sign-in to it, which stay in the library. On, those agents get
// it back, none when it had none (it is the router's alone again). A
// plugin's server is the same switch as its row on the Plugins page.
func ServerOn(name string, on bool) (*Result, error) {
	return change(func(l *Library) error {
		s := l.server(name)
		if s == nil {
			return fmt.Errorf("no server called %s", name)
		}
		if !on {
			if !s.Hidden {
				s.Hidden, s.Paused, s.Agents = true, s.Agents, []string{}
			}
			return nil
		}
		if !s.Hidden {
			return nil
		}
		if len(s.Agents) == 0 {
			s.Agents = slices.Compact(slices.Sorted(slices.Values(s.Paused)))
		}
		s.Hidden, s.Paused = false, nil
		return nil
	})
}

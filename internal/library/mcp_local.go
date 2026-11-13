package library

import (
	"fmt"
	"slices"
)

// A server can be kept to this computer (Server.Local; huhuy72219405 on
// X: "有些mcp属于本地的，同步之后其他设备不应该读取到"). It is for a server
// that runs a program, or reads a folder, only this computer has.
//
//   - Collect leaves it out, so a backup or a sync carries it to no other
//     computer. Turning it on takes the server out of what is carried, so
//     the other computers let go of theirs at their next sync, as of any
//     server taken out; the flag itself is never carried.
//   - Put keeps it, whatever comes in: a bundle without it doesn't take it
//     away, and another computer's server by its name doesn't take its
//     place. That one is kept in Away, given to no agent here, and carried
//     on as it came, so a sync from here doesn't take it from the computers
//     that have it.
//   - Once no server here by that name is kept to this computer (it was
//     removed, renamed or shared again), settleAway lets go of Away's: the
//     server here shared again goes in its place, and with none here the
//     other computers' comes in, as a sync would bring it.

// carriedServers are the servers a carried library has: the library's,
// but those kept to this computer, and the other computers' by their
// names.
func (l *Library) carriedServers() []*Server {
	out := []*Server{}
	for _, s := range l.MCP {
		if !s.Local {
			out = append(out, s)
		}
	}
	return append(out, l.Away...)
}

// putServers makes the library's servers those a carried library has,
// but for the ones kept to this computer, which stay as they are.
func (l *Library) putServers(in []*Server) {
	own := map[string]bool{}
	servers := []*Server{}
	here := map[string]*Server{}
	for _, s := range l.MCP {
		here[s.Name] = s
		if s.Local {
			own[s.Name] = true
			servers = append(servers, s)
		}
	}
	l.Away = nil
	for _, s := range in {
		c := *s
		c.Local = false // only this computer says what is its own
		c.Bridge = ""   // and which of its providers a server is
		c.Agents = slices.Sorted(slices.Values(orNone(s.Agents)))
		// this computer's own start stays, what came in its synced one
		// (mcp_here.go)
		keepStart(&c, here[c.Name])
		if own[c.Name] {
			l.Away = append(l.Away, &c)
		} else {
			servers = append(servers, &c)
		}
	}
	l.MCP = servers
}

// settleAway lets go of each server in Away whose name no server kept to
// this computer has any more. One shared here by that name is the one
// carried from now on; with none, the other computers' comes in.
func (l *Library) settleAway() {
	away := l.Away[:0:0]
	for _, a := range l.Away {
		switch s := l.server(a.Name); {
		case s != nil && s.Local:
			away = append(away, a)
		case s == nil:
			l.MCP = append(l.MCP, a)
		}
	}
	l.Away = away
	if len(l.Away) == 0 {
		l.Away = nil
	}
}

// ServerLocal keeps a server to this computer, or lets a sync carry it
// again.
func ServerLocal(name string, on bool) (*Result, error) {
	return change(func(l *Library) error {
		s := l.server(name)
		if s == nil {
			return fmt.Errorf("no server called %s", name)
		}
		s.Local = on
		if on {
			s.Shared = nil // its command is this computer's alone now
		}
		return nil
	})
}

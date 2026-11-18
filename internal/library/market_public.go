package library

import "slices"

// PublicServers is the library's servers that the market lists from what
// ships with magpie (the featured ones and the registry's popular ones),
// each by its market id, given to at least one agent: what the day's
// report names of the user's MCP servers, for the market's counts of who
// uses each (internal/stats). A server of the user's own, one found only
// by a search of the registry, and one no agent is given are never in it,
// nor is anything of a server but its id: no name, URL, command or key.
func PublicServers() []string {
	l, err := loadLocked()
	if err != nil {
		return nil
	}
	return l.publicServers(append(slices.Clone(featured), popularServers()...))
}

func (l *Library) publicServers(listed []MarketServer) []string {
	byKey := map[string]string{}
	for i := range listed {
		k := serverKey(&listed[i].server)
		// a bare command names no package, so it could be anyone's server
		if k == "" || k == listed[i].server.Command {
			continue
		}
		if _, ok := byKey[k]; !ok {
			byKey[k] = listed[i].ID
		}
	}
	out := []string{}
	for _, s := range l.MCP {
		if len(s.Agents) == 0 {
			continue
		}
		if id := byKey[serverKey(s)]; id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

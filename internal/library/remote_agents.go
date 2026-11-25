package library

import (
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
)

// An agent on another computer can use this magpie's MCP router at its own
// address, gateway.HubMCPPath<agent>, as a local one is given it: bitxeno303
// on X ran magpie in Docker and DeepSeek Harness on their own computer,
// and /mcp/magpie/hub/dsh served nothing, since nothing could be ticked
// for an agent the container doesn't have. An agent magpie knows by its id,
// connecting from another computer (gateway.hubMeet: not loopback, not a
// web page) to an address that is not an agent's on this one, is recorded
// as a remote agent (Library.RemoteAgents). The first time, if no server
// is ticked for it yet, it is given every server on (not Hidden) that goes
// through the router: what any other app is given at gateway.HubMCPAll.
// From then the Library lists it with the agents that take MCP servers
// (AgentView.Remote), and its chips on each server say what it has. magpie
// writes nothing for it: it reads the router's address from its own
// config on its own computer.

// hereFor is how long an id Met passed by (an agent found on this
// computer, or no agent magpie knows) is passed by again before Met looks
// again: Targets reads every agent's folders, and agent.All every agent.
const hereFor = 10 * time.Minute

var (
	hereMu   sync.Mutex
	hereSeen = map[string]time.Time{}
)

func (theHub) Met(agentID string) { metRemote(agentID) }

// metRemote records id as a remote agent (Library.RemoteAgents), giving it
// the router's servers the first time, unless it isn't an agent magpie
// knows, is one on this computer, or is recorded already.
func metRemote(id string) {
	// an agent in a WSL distro (codex@wsl:Ubuntu) and a profile
	// (hermes#coder) are this computer's own
	if id == "" || strings.ContainsAny(id, "@#/") {
		return
	}
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil || !l.MCPHub || slices.Contains(l.RemoteAgents, id) {
		return
	}
	if passedBy(id) {
		return
	}
	if knownAgent(id) == nil || targetByID(id) != nil {
		hereMu.Lock()
		hereSeen[id] = time.Now()
		hereMu.Unlock()
		return
	}
	if _, err := change(func(l *Library) error {
		l.meetRemote(id)
		return nil
	}); err != nil {
		log.Printf("library: recording %s, connected to the MCP router from another computer: %v", id, err)
	}
}

// meetRemote records id as a remote agent in l, and gives it the router's
// servers when no server is ticked for it yet.
func (l *Library) meetRemote(id string) {
	if slices.Contains(l.RemoteAgents, id) {
		return
	}
	l.RemoteAgents = slices.Sorted(slices.Values(append(l.RemoteAgents, id)))
	if slices.ContainsFunc(l.MCP, func(s *Server) bool { return slices.Contains(s.Agents, id) }) {
		return // ticked for it already: the user's, or a sync's
	}
	for _, s := range l.MCP {
		if !s.Hidden && inHub(s, id) {
			s.Agents = slices.Sorted(slices.Values(append(s.Agents, id)))
		}
	}
}

// knownAgent is the agent, or app, whose id is id exactly: never one by an
// alias or a prefix, as Find would take.
func knownAgent(id string) *agent.Agent {
	for _, a := range append(agent.All(), apps()...) {
		if a.ID == id && a.WSL == "" {
			return a
		}
	}
	return nil
}

// passedBy is whether Met passed id by less than hereFor ago.
func passedBy(id string) bool {
	hereMu.Lock()
	defer hereMu.Unlock()
	at, ok := hereSeen[id]
	return ok && time.Since(at) < hereFor
}

// remoteViews are l's remote agents as the page lists them, but those it
// lists already (an agent installed here since, or gone with what magpie
// gave it): each takes any server through the router.
func (l *Library) remoteViews(listed []AgentView) []AgentView {
	var out []AgentView
	for _, id := range l.RemoteAgents {
		if slices.ContainsFunc(listed, func(v AgentView) bool { return v.ID == id }) {
			continue
		}
		a := knownAgent(id)
		if a == nil {
			continue
		}
		v := AgentView{ID: id, Name: a.Name, Icon: a.Icon, MCP: gateway.HubMCPPath + id, Remote: true}
		if l.MCPHub && slices.ContainsFunc(l.MCP, func(s *Server) bool { return slices.Contains(s.Agents, id) && inHub(s, id) }) {
			v.Hub = "on"
		}
		out = append(out, v)
	}
	return out
}

// isRemote is whether id is a remote agent here (Takes, reachNow).
func isRemote(id string) bool {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	return err == nil && slices.Contains(l.RemoteAgents, id)
}

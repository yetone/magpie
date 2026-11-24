package library

import (
	"fmt"
	"slices"
)

// Magpie Artifacts (gateway's artifacts_mcp.go) is given to every agent
// that can take it, once:
//
//   - The first Sync after it shipped adds it to the library, ticked for
//     every agent whose MCP config takes a command (adoptArtifacts), and
//     records that it did (ArtifactsOffered): a user who removes it, or
//     unticks an agent, keeps it that way.
//   - A library that has it already, added by hand, is left as it is.
//   - Claude Code given it leaves out its own Artifact tool, whose pages
//     are its claude.ai account's and don't open after magpie switches
//     that account: a deny rule beside magpie's others (syncToolRules,
//     claudeArtifactTool), taken back when the server goes. A user's own
//     rule for Artifact, allow or deny, is theirs.

// ArtifactsServerName is the name Magpie Artifacts' server is written
// under, the name the gateway's own artifacts server goes by (ownMCP).
const ArtifactsServerName = "magpie-artifacts"

// artifactsKey is Magpie Artifacts' server as serverKey gives it, and
// artifactPublish its tool that makes a page (gateway's
// artifactPublishTool).
const (
	artifactsKey    = "magpie mcp artifacts"
	artifactPublish = "publish_artifact"
)

// claudeArtifactTool is Claude Code's own tool for artifacts, as its
// permission rules name it.
const claudeArtifactTool = "Artifact"

// artifactsServer is Magpie Artifacts: pages an agent publishes for the
// user, kept on this computer with every version, through `magpie mcp
// artifacts`, which passes each call to the gateway's ArtifactsMCPPath
// with the folder the agent started it in.
func artifactsServer() MarketServer {
	return localServer(ArtifactsServerName, "Magpie Artifacts", "Magpie", "https://usemagpie.ai/favicon.png", "https://usemagpie.ai",
		"Pages your agents make for you (reports, charts, diagrams, prototypes, small apps), kept on this computer with every version and opened in your browser, whichever account made them.", magpieCommand, []string{"mcp", "artifacts"})
}

// offerArtifacts is whether Sync gives Magpie Artifacts to the agents;
// the package's tests that count servers turn it off.
var offerArtifacts = true

// adoptArtifacts adds Magpie Artifacts once; true when it changed the
// library.
func (l *Library) adoptArtifacts() bool {
	if l.ArtifactsOffered || !offerArtifacts {
		return false
	}
	l.ArtifactsOffered = true
	m := artifactsServer()
	s := m.server
	s.Args = slices.Clone(s.Args)
	selfCommand(&s)
	if l.haveServer(&s) != "" {
		return true
	}
	s.Agents = []string{}
	for _, t := range Targets() {
		if t.MCP != nil && t.MCP.supports(&s) == nil && !slices.Contains(s.Agents, t.Agent.ID) {
			s.Agents = append(s.Agents, t.Agent.ID)
		}
	}
	slices.Sort(s.Agents)
	base := s.Name
	for i := 2; l.server(s.Name) != nil; i++ {
		s.Name = fmt.Sprintf("%s-%d", base, i)
	}
	l.MCP = append(l.MCP, &s)
	if l.Icons == nil {
		l.Icons = map[string]string{}
	}
	l.Icons[serverKey(&s)] = m.Icon
	return true
}

// artifactsGiven is whether agent id has Magpie Artifacts: ticked on it,
// and written into its config (given) or reached through the hub.
func (l *Library) artifactsGiven(id string, given []string) bool {
	for _, s := range l.MCP {
		if s == nil || serverKey(s) != artifactsKey || !slices.Contains(s.Agents, id) {
			continue
		}
		if slices.Contains(given, s.Name) || (l.MCPHub && inHub(s, id)) {
			return true
		}
	}
	return false
}

// ArtifactsAgents is the agents Magpie Artifacts is ticked on, sorted,
// for Work › Artifacts' empty page to say who can make one; none when the
// server is off or gone.
func ArtifactsAgents() ([]string, error) {
	l, err := loadLocked()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range l.MCP {
		if s == nil || s.Hidden || serverKey(s) != artifactsKey {
			continue
		}
		for _, a := range s.Agents {
			if !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

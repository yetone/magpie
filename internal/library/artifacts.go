package library

import "slices"

// Magpie Artifacts (gateway's artifacts_mcp.go) is the user's to add:
//
//   - Nothing gives it to an agent unasked. It is in the Library's market
//     (featured), and Work › Artifacts' empty page leads there.
//   - From 11-24 to 11-25 the first Sync added it, ticked for every agent
//     whose MCP config takes a command, and recorded ArtifactsOffered.
//     Users asked why a server and a deny rule appeared that they never
//     chose, so the first Sync after that takes back the one it added,
//     once (withdrawArtifacts, ArtifactsWithdrawn): still under its name
//     and ticked on every agent it was given to. One the user changed
//     (an agent unticked, renamed) or added again later is theirs.
//   - Claude Code given it leaves out its own Artifact tool, whose pages
//     are its claude.ai account's and don't open after magpie switches
//     that account: a deny rule beside magpie's others (syncToolRules,
//     claudeArtifactTool), taken back when the server goes. A user's own
//     rule for Artifact, allow or deny, is theirs. The market's text says
//     so, for the user to know before they add it.

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
		"Pages your agents make for you (reports, charts, diagrams, prototypes, small apps), kept on this computer with every version and opened in your browser, whichever account made them. Claude Code given it uses it in place of its own Artifact tool.", magpieCommand, []string{"mcp", "artifacts"})
}

// withdrawArtifacts takes back, once, the Magpie Artifacts the first
// Sync of 11-24 added unasked (ArtifactsOffered): the server under its
// name, ticked on every agent that adding ticked. True when it changed
// the library.
func (l *Library) withdrawArtifacts() bool {
	if !l.ArtifactsOffered || l.ArtifactsWithdrawn {
		return false
	}
	l.ArtifactsWithdrawn = true
	s := l.server(ArtifactsServerName)
	if s == nil || serverKey(s) != artifactsKey {
		return true
	}
	for _, t := range Targets() {
		if t.MCP != nil && t.MCP.supports(s) == nil && !slices.Contains(s.Agents, t.Agent.ID) {
			return true // an agent unticked: the user's now
		}
	}
	_ = removeServer(l, ArtifactsServerName)
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

package gateway

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"github.com/yetone/magpie/internal/settings"
)

// The router's tools on demand (ArvinCharl on X, 11-04: MCP tools fill
// the context). An agent that searches its MCP tools itself, loading one
// only when its model asks for it, is given every tool: Claude Code
// (ENABLE_TOOL_SEARCH, agent/claude.go), Claude Desktop, whose Code tab is
// Claude Code, Codex (supports_search_tool, codexcat.go), Pi, whose MCP
// extension lists a server's tools only when they are searched for, Droid,
// which defers every MCP tool to its ToolSearch, and omp, which mounts
// them as xd:// devices named in a catalog. Any other agent sends every
// tool it is given, with its whole inputSchema, on every request, so past
// hubOnDemandOver tools it is given two in their place: find_tools, which
// gives the tools that match what the model looks for with their
// parameters, and call_tool, which runs one by name. find_tools' own
// description names every server and its tools (hubFindSay), since a model
// picks a tool by what tools/list says and not every agent shows the
// instructions (Droid shows none); the instructions name them too
// (hubDetails). settings' RouterAllTools turns it off.

// hubFindTool and hubCallTool are the two tools. A tool of the hub's is
// <server>__<tool>, so neither is ever one of its tools' names.
const (
	hubFindTool = "find_tools"
	hubCallTool = "call_tool"
)

// hubOnDemandOver is how many tools an agent is given whole at most: a
// few tools cost less than a search before each call.
const hubOnDemandOver = 10

// hubSearchesItself are the agents that load MCP tools only when searched
// for, by their id without a WSL distro (claude@wsl:Ubuntu is claude).
// Droid (0.231) defers every MCP tool but GitHub's and agent-browser's to
// its ToolSearch, which matches the names and descriptions it is given, and
// omp (18.9) mounts every MCP tool as an xd:// device, its schema read only
// when asked for: given find_tools in their place, neither showed its
// model which tools were behind it (tao5010 on Discord, 11-04).
var hubSearchesItself = map[string]bool{"claude": true, "claude-desktop": true, "codex": true, "pi": true, "droid": true, "omp": true}

// hubFindFull is how many of find_tools' matches come with their
// parameters, and hubFindMore how many more it names after them.
const (
	hubFindFull = 5
	hubFindMore = 15
)

// hubOnDemand is whether agent is given find_tools and call_tool in place
// of its n tools. The address for any other app ("") lists every tool:
// which app connects there isn't known.
func hubOnDemand(agent string, n int) bool {
	if agent == "" || n <= hubOnDemandOver {
		return false
	}
	id, _, _ := strings.Cut(HubAgentOf(agent), "@")
	if hubSearchesItself[id] {
		return false
	}
	return !settings.Shared().RouterAllTools
}

// hubToolCount is how many tools ss have, as the instructions know them.
func hubToolCount(ss []HubServer) int {
	n := 0
	for _, s := range ss {
		n += len(s.Tools)
	}
	return n
}

// hubOnDemandSay is what the instructions add when the tools are on demand.
const hubOnDemandSay = " Its tools are given on demand: " + hubFindTool + " gives a tool's description and parameters, and " + hubCallTool + " runs it by name."

// hubFindSayMax is how many characters of find_tools' description name the
// servers behind it and their tools (hubFindSay), sent with every request
// of an agent given its tools on demand: about the cost of three tools'
// schemas, in place of them all.
const hubFindSayMax = 1200

// hubFindHow is what find_tools does, after what there is to find.
const hubFindHow = "Finds the tools of the MCP servers behind this one that match query: words of what you want done, or a tool's name. Gives each match's name, description and parameters (inputSchema); run one with " + hubCallTool + ". An empty query names every tool."

// hubFindSay is find_tools' description: first a line naming each server
// with what it is for (HubLine; an agent that lists a tool by its first
// line, as omp's catalog does, shows that much), then what find_tools
// does, then each server's tools (hubDetails) in hubFindSayMax characters.
// A model given only find_tools and call_tool learns from it what there is
// to find: told only how to search, a model asked for a picture of a cat
// searched three times and stopped (tao5010 on Discord, 11-04).
func hubFindSay(ss []HubServer) string {
	line := HubLine(ss)
	if line == "" {
		return hubFindHow
	}
	return strings.TrimRight(line+"\n"+hubFindHow+"\n"+hubDetails(ss, hubFindSayMax), "\n")
}

// hubOnDemandSpecs are the two tools an agent is given in place of its
// tools, ss's, their descriptions after where, as each tool's are.
func hubOnDemandSpecs(where string, ss []HubServer) []any {
	pre := ""
	if where != "" {
		pre = "[" + where + "] "
	}
	return []any{
		map[string]any{
			"name":        hubFindTool,
			"description": pre + hubFindSay(ss),
			"inputSchema": json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Words of what you want done, or a tool's name; empty names every tool"},"server":{"type":"string","description":"Only this server's tools"}}}`),
		},
		map[string]any{
			"name":        hubCallTool,
			"description": pre + "Runs a tool of the MCP servers behind this one, by the name " + hubFindTool + " gives it (<server>__<tool>), with arguments as its inputSchema says.",
			"inputSchema": json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"The tool's name, <server>__<tool>"},"arguments":{"type":"object","description":"The tool's arguments, as its inputSchema says"}},"required":["name"]}`),
		},
	}
}

// hubSpecOf is t as tools/list gives it: its own spec, named as the hub
// names it, with an inputSchema, its description after where.
func hubSpecOf(t hubTool, where string) map[string]json.RawMessage {
	spec := make(map[string]json.RawMessage, len(t.Spec)+1)
	for k, v := range t.Spec {
		spec[k] = v
	}
	name, _ := json.Marshal(t.name)
	spec["name"] = name
	if s, ok := spec["inputSchema"]; !ok || !json.Valid(s) {
		spec["inputSchema"] = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	if where != "" {
		var d string
		json.Unmarshal(spec["description"], &d)
		d, _ = strings.CutPrefix(d, "["+where+"]")
		d = strings.TrimSpace("[" + where + "] " + strings.TrimSpace(d))
		spec["description"], _ = json.Marshal(d)
	}
	return spec
}

// hubDesc is t's description as its server gives it.
func hubDesc(t hubTool) string {
	var d string
	json.Unmarshal(t.Spec["description"], &d)
	return d
}

// hubWords are s's words, lower case, split at anything not a letter or
// digit (create_issue is create and issue).
func hubWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// hubScore is how well t matches query's words: a word in its name
// counts three, in its description one, and the whole query in its name
// five more.
func hubScore(t hubTool, query string, words []string) int {
	name := strings.ToLower(t.name)
	desc := strings.ToLower(hubDesc(t))
	n := 0
	for _, w := range words {
		switch {
		case strings.Contains(name, w):
			n += 3
		case strings.Contains(desc, w):
			n++
		}
	}
	if q := strings.ToLower(strings.TrimSpace(query)); q != "" && n > 0 && (strings.Contains(name, q) || strings.EqualFold(t.Tool, q)) {
		n += 5
	}
	return n
}

// hubMatches are ts best matching query first, those that don't left out.
func hubMatches(ts []hubTool, query string) []hubTool {
	words := hubWords(query)
	type scored struct {
		t hubTool
		n int
	}
	var out []scored
	for _, t := range ts {
		if n := hubScore(t, query, words); n > 0 {
			out = append(out, scored{t, n})
		}
	}
	slices.SortStableFunc(out, func(a, b scored) int { return cmp.Compare(b.n, a.n) })
	ms := make([]hubTool, len(out))
	for i, s := range out {
		ms[i] = s.t
	}
	return ms
}

// hubOfServer are ts of the server named server, as the user or the hub
// names it, in any case; "" is every tool.
func hubOfServer(ts []hubTool, server string) []hubTool {
	server = strings.TrimSpace(server)
	if server == "" {
		return ts
	}
	var out []hubTool
	for _, t := range ts {
		if strings.EqualFold(t.Server, server) || strings.EqualFold(unsafeToolChars.ReplaceAllString(t.Server, "-"), server) {
			out = append(out, t)
		}
	}
	return out
}

// hubNameLines name ts, one a line, each with the first clause of what it
// does.
func hubNameLines(ts []hubTool) string {
	var b strings.Builder
	for _, t := range ts {
		b.WriteString("- " + t.name)
		if c := hubToolClause(hubDesc(t)); c != "" {
			b.WriteString(": " + c)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// hubFind answers find_tools: the matches with their parameters, then
// more by name; with no query, or none matching, every tool by name.
func hubFind(ts []hubTool, args json.RawMessage, where string) any {
	var p struct {
		Query  string `json:"query"`
		Server string `json:"server"`
	}
	json.Unmarshal(args, &p)
	ts = hubOfServer(ts, p.Server)
	if len(ts) == 0 {
		if p.Server != "" {
			return toolResult("No server called "+p.Server+" is behind this one. Call "+hubFindTool+" without server for every tool.", true)
		}
		return toolResult("No MCP server behind this one has a tool now.", true)
	}
	if strings.TrimSpace(p.Query) == "" {
		return toolResult("Every tool, by name; "+hubFindTool+" with its name gives its parameters:\n"+hubNameLines(ts), false)
	}
	ms := hubMatches(ts, p.Query)
	if len(ms) == 0 {
		return toolResult("No tool matches \""+p.Query+"\". Every tool, by name:\n"+hubNameLines(ts), false)
	}
	full := ms[:min(len(ms), hubFindFull)]
	specs := make([]any, 0, len(full))
	for _, t := range full {
		spec := hubSpecOf(t, "")
		specs = append(specs, map[string]json.RawMessage{"name": spec["name"], "description": spec["description"], "inputSchema": spec["inputSchema"]})
	}
	b, _ := json.MarshalIndent(specs, "", " ")
	out := "Run one with " + hubCallTool + ", its name and arguments as its inputSchema says.\n" + string(b)
	if more := ms[len(full):min(len(ms), hubFindFull+hubFindMore)]; len(more) > 0 {
		out += "\nAlso matching (" + hubFindTool + " with a name gives its parameters):\n" + hubNameLines(more)
	}
	return toolResult(out, false)
}

// hubCallArgs is call_tool's arguments: the tool's name, and its own
// arguments, an object; given as a string holding one, as some models
// send it, the object it holds.
func hubCallArgs(args json.RawMessage) (name string, inner json.RawMessage, err error) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", nil, err
	}
	inner = p.Arguments
	var s string
	if json.Unmarshal(inner, &s) == nil {
		if s = strings.TrimSpace(s); s == "" {
			inner = nil
		} else if json.Valid([]byte(s)) {
			inner = json.RawMessage(s)
		}
	}
	return strings.TrimSpace(p.Name), inner, nil
}

// hubCallNamed is the tool call_tool names: as tools/call finds it
// (hubToolNamed), else the one tool of that name on its server, in any
// case.
func hubCallNamed(ctx context.Context, ts []hubTool, agent, name string) (hubTool, bool) {
	if t, ok := hubToolNamed(ctx, ts, agent, name); ok {
		return t, true
	}
	var found []hubTool
	for _, t := range ts {
		if strings.EqualFold(t.name, name) || strings.EqualFold(t.Tool, name) {
			found = append(found, t)
		}
	}
	if len(found) != 1 {
		return hubTool{}, false
	}
	return found[0], true
}

// hubCallByName answers call_tool: the tool it names run as tools/call runs
// it, or, for a name no server gives, the tools close to it.
func (s *Server) hubCallByName(r *http.Request, agent string, ts []hubTool, args json.RawMessage, meta hubCallMeta) any {
	name, inner, err := hubCallArgs(args)
	if err != nil {
		return toolResult(hubCallTool+" takes {\"name\": \"<server>__<tool>\", \"arguments\": {…}}: "+err.Error(), true)
	}
	if name == "" {
		return toolResult(hubCallTool+" needs the name of the tool to run; "+hubFindTool+" gives the tools' names.", true)
	}
	t, ok := hubCallNamed(r.Context(), ts, agent, name)
	if !ok {
		why := "No tool is called " + name + "."
		if ms := hubMatches(ts, name); len(ms) > 0 {
			why += " Close to it:\n" + hubNameLines(ms[:min(len(ms), hubFindFull)])
		} else {
			why += " " + hubFindTool + " gives the tools' names."
		}
		return toolResult(why, true)
	}
	return s.hubCall(r, agent, t, inner, meta)
}

package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/sessions"
	"github.com/yetone/magpie/internal/settings"
)

// The MCP hub (#2679): with the Library's switch on, magpie is the one MCP
// server an agent is given, at HubMCPPath<agent>, and it hands each call
// on to the Library server behind it, as Composio does. Its tools are
// those of the Library servers ticked for that agent, each named
// <server>__<tool>; the servers themselves (a command started once for
// every agent, a URL reached with magpie's sign-in) are the library's to
// run (library/mcp_hub.go), which sets MCPHub. It is guarded as
// magpie's other MCP servers are (serveMCP), and each call is held to the
// calling gateway key's limit (reserveFor). Tools only, for now: no
// resources or prompts.

// HubMCPPath is where the hub is served, the agent's id after it.
const HubMCPPath = "/mcp/magpie/hub/"

// HubMCPAll is the hub for any other app (Cursor, VS Code, a script): one
// address with every server in the Library, ticked for an agent or not.
// It is the agent "" to Hub.
const HubMCPAll = "/mcp/magpie/hub"

// HubTool is one tool a Library server lists, as the hub gives it.
type HubTool struct {
	Server string // the Library server's name
	Tool   string // the tool's name on that server
	// Spec is the tool as the server listed it: its description,
	// inputSchema and the rest go to the agent as they are, its name
	// replaced
	Spec map[string]json.RawMessage
	// Roots: the server writes into the agent's project (magpie-image),
	// so a call is made with the agent's folders (HubCaller)
	Roots bool
}

// HubCaller is who a call of a Roots tool is for: the folders the agent
// said are its, as file URIs (none for an agent on another machine, whose
// folders aren't here), and its User-Agent's product, the name the
// server's own requests go under.
//
// Session is which of the hub's callers the call is from (hubSession), for
// a server that keeps something per caller: Magpie Browser's tab, which
// two agents behind one router would otherwise share, the router running
// one `magpie mcp browser` for all of them.
type HubCaller struct {
	Roots   []string
	Client  string
	Session string
}

// The _meta keys a call of magpie's image server carries its caller in:
// the roots, a list of file URIs (an empty one when the agent has none
// here), and the client; and the one its result says the model it drew
// with in (provider/model), for the hub's row of the call.
const (
	HubRootsMeta  = "magpie/roots"
	HubClientMeta = "magpie/client"
	HubModelMeta  = "magpie/model"
	// HubSessionMeta is the _meta key a call carries its caller's session
	// in (HubCaller.Session), to Magpie Browser and to another magpie's
	// router, which passes it on as its own caller's
	HubSessionMeta = "magpie/session"
)

type hubCallerKey struct{}

// WithHubCaller is ctx carrying caller c.
func WithHubCaller(ctx context.Context, c HubCaller) context.Context {
	return context.WithValue(ctx, hubCallerKey{}, c)
}

// HubCallerOf is the caller a hub call's ctx carries.
func HubCallerOf(ctx context.Context) HubCaller {
	c, _ := ctx.Value(hubCallerKey{}).(HubCaller)
	return c
}

// Hub is the servers behind the hub: the library's.
type Hub interface {
	// On is whether the Library's switch is on: off, the hub lists no
	// tool and runs none
	On() bool
	// Tools are the tools of the servers ticked for agent (every server
	// in the Library for agent "", HubMCPAll's), but those turned off; a
	// server that couldn't be reached by ctx's end is left out
	Tools(ctx context.Context, agent string) []HubTool
	// Servers are the servers behind agent's tools, by name, each with
	// what it is for where magpie knows it; it starts none of them
	Servers(agent string) []HubServer
	// Call runs one tool of server for agent, and is its result as the
	// server gave it; an error is why it couldn't be run
	Call(ctx context.Context, agent, server, tool string, args json.RawMessage) (json.RawMessage, error)
	// Stop ends every server the hub started
	Stop()
	// Prune ends each server the hub started that the Library no longer
	// gives: the switch turned off, the server removed or edited, here or
	// by another magpie (the window, the CLI)
	Prune()
}

// HubServer is a server behind the hub as an agent is told of it: About
// is what it is for, "" when magpie doesn't know.
type HubServer struct {
	Name, About string
	// Title is the name the server gives itself (initialize's
	// serverInfo.title), "" when it gives none: another magpie's router
	// says "Magpie MCP hub · <its name>"
	Title string
	// Tools are its tools as it last listed them, each with what its
	// description says first; Known is whether magpie has listed them for
	// the server as it is now
	Tools []HubToolNote
	Known bool
	// Bridge: the server is another magpie's router, which the library
	// gave a server of its own for being a Remote magpie
	// (library/bridge_mcp.go). A request from another magpie's router
	// (RouterHeader) isn't given it, so two magpies that have each other
	// never ask each other round.
	Bridge bool
	// HereOnly: the server acts as the user of this computer beyond what
	// its tools' arguments say: Magpie Browser (every site the user's
	// browser is signed in to), magpie's image server (it draws with this
	// magpie's own token, past a gateway key's models, accounts and
	// spend), and another magpie's router (which passes a call on to its
	// own). It is given only to a caller on this computer (HubVisitor),
	// Magpie Browser also to a keyed one elsewhere (Keyed).
	HereOnly bool
	// Program: the server is a program run on this computer, with the
	// user's rights (a command, or a tools plugin), not an HTTP server
	// elsewhere. A gateway key held to some models, accounts or a spend
	// limit isn't given it from another computer.
	Program bool
	// Keyed: the server is Magpie Browser, whose browsers are each this
	// computer's or a gateway key's (browser.go): given to this computer
	// and to another computer's caller with a key, and calls go to that
	// key's browsers only.
	Keyed bool
}

// HubVisitor is who a hub request is from, for what it is given of the
// servers behind it (HubServer's HereOnly and Program): Away, another
// computer, a tunnel or a web page; Held, by a gateway key held to some
// models, accounts or a spend limit. A caller on this computer is given
// every server. Key is the gateway key an Away visitor came with, whose
// browsers it is given (HubServer's Keyed).
type HubVisitor struct {
	Away, Held bool
	Key        string
}

// Gives is whether v is given s.
func (v HubVisitor) Gives(s HubServer) bool {
	if !v.Away {
		return true
	}
	if s.Keyed {
		return v.Key != ""
	}
	return !s.HereOnly && !(v.Held && s.Program)
}

type hubVisitorKey struct{}

// WithHubVisitor is ctx a hub request of v.
func WithHubVisitor(ctx context.Context, v HubVisitor) context.Context {
	return context.WithValue(ctx, hubVisitorKey{}, v)
}

// HubVisitorOf is who ctx's hub request is from; a ctx with none (the
// gateway's own calls) is this computer's.
func HubVisitorOf(ctx context.Context) HubVisitor {
	v, _ := ctx.Value(hubVisitorKey{}).(HubVisitor)
	return v
}

// hubVisitor is who r is from (HubVisitor).
func hubVisitor(r *http.Request) HubVisitor {
	away := !local(r) || fromBrowser(r)
	v := HubVisitor{Away: away, Held: away && nativeHeld(access.Caller(r.Context()))}
	if !local(r) && !fromBrowser(r) {
		v.Key = access.Caller(r.Context()).KeyID
	}
	return v
}

// RouterHeader marks a request of magpie's own router to another magpie's
// (FromRouter).
const RouterHeader = "X-Magpie-Router"

type fromRouterKey struct{}

// WithRouter is ctx a request of another magpie's router.
func WithRouter(ctx context.Context) context.Context {
	return context.WithValue(ctx, fromRouterKey{}, true)
}

// FromRouter is whether ctx is a request of another magpie's router: the
// servers behind this one that are a Remote magpie's router (HubServer's
// Bridge) aren't listed or run for it.
func FromRouter(ctx context.Context) bool {
	on, _ := ctx.Value(fromRouterKey{}).(bool)
	return on
}

// hubServersOf are the servers behind agent's tools, as the request of
// ctx is given them.
func hubServersOf(ctx context.Context, agent string) []HubServer {
	// a copy: the hub's own list isn't cut
	ss := slices.Clone(MCPHub.Servers(agent))
	if FromRouter(ctx) {
		ss = slices.DeleteFunc(ss, func(s HubServer) bool { return s.Bridge })
	}
	v := HubVisitorOf(ctx)
	return slices.DeleteFunc(ss, func(s HubServer) bool { return !v.Gives(s) })
}

// hubServes is whether the hub gives another magpie's router a server.
func hubServes() bool {
	return MCPHub != nil && MCPHub.On() && slices.ContainsFunc(MCPHub.Servers(""), func(s HubServer) bool { return !s.Bridge })
}

// HubToolNote is one tool of a server behind the hub, as the instructions
// name it: its name on that server, and the first clause of its
// description.
type HubToolNote struct{ Name, About string }

// MCPHub is set by the library; nil, the hub has no server.
var MCPHub Hub

// The hub's budgets: tools/list waits for the servers this long (a server
// still starting is left out, and listed once it has), and a call this
// long, under the 60s an MCP client waits for an answer, for the agent to
// be told which server didn't answer rather than its own timeout.
var (
	hubListWait = 40 * time.Second
	hubCallWait = 50 * time.Second
)

const hubBody = 4 << 20

const errHubOff = "magpie's MCP hub is off, so it runs no server's tools: turn it on in magpie's Library → MCP"

func (s *Server) hubMCP(w http.ResponseWriter, r *http.Request) {
	agent := r.PathValue("agent")
	if r.Header.Get(RouterHeader) != "" {
		r = r.WithContext(WithRouter(r.Context()))
	}
	r = r.WithContext(WithHubVisitor(r.Context(), hubVisitor(r)))
	serveMCP(w, r, "magpie's MCP hub", "magpie", hubBody, func(r *http.Request, m mcpMessage) (any, map[string]any) {
		return s.hubMCPCall(r, agent, m)
	})
}

// hubTool is one tool as the hub names it.
type hubTool struct {
	HubTool
	name string
}

// hubSep is between a server's name and its tool's in the hub's names.
const hubSep = "__"

// hubNamed names ts <server>__<tool>, in the characters MCP clients take
// and 64 of them at most, a name two would share told apart by a number.
func hubNamed(ts []HubTool) []hubTool {
	out := make([]hubTool, 0, len(ts))
	seen := map[string]bool{}
	for _, t := range ts {
		base := unsafeToolChars.ReplaceAllString(t.Server, "-") + hubSep + unsafeToolChars.ReplaceAllString(t.Tool, "-")
		name := cutName(base, 64)
		for i := 2; seen[name]; i++ {
			suf := "_" + strconv.Itoa(i)
			name = cutName(base, 64-len(suf)) + suf
		}
		seen[name] = true
		out = append(out, hubTool{HubTool: t, name: name})
	}
	return out
}

func hubTools(ctx context.Context, agent string) []hubTool {
	if MCPHub == nil || !MCPHub.On() {
		return nil
	}
	// a server the caller isn't given (HubVisitor) is neither listed nor,
	// as calls are found in this list (hubToolNamed), run
	v, kept := HubVisitorOf(ctx), map[string]bool{}
	for _, s := range MCPHub.Servers(agent) {
		if !v.Gives(s) {
			kept[s.Name] = true
		}
	}
	ctx, cancel := context.WithTimeout(ctx, hubListWait)
	defer cancel()
	ts := slices.DeleteFunc(slices.Clone(MCPHub.Tools(ctx, agent)), func(t HubTool) bool { return kept[t.Server] })
	return hubNamed(ts)
}

// hubLineMax is how much of the instructions' first line an agent is sure
// to show its model: Pi shows a server as that line, cut at 250 characters
// (extensions/mcp, MAX_SERVER_DESCRIPTION_CHARS), and an agent that lists
// the router's tools only once they are searched for knows of the servers
// behind it from this line alone (a Pi asked about a GitHub repository
// cloned it instead of asking DeepWiki, 11-01).
const hubLineMax = 250

// hubSayMax is how long the instructions are at most, in characters: half
// the 4096 Claude Code keeps of a server's instructions, so a model given
// several routers reads a few lines of each. The lines that describe each
// server take what the rest leaves.
const hubSayMax = 2000

// hubAbout is the instructions: HubLine, then here (what the router says
// of where it is, when it has a name), then a line for each server with
// what it is for and its tools (hubDetails), then tail, within hubSayMax.
// With no server it is tail, then here.
func hubAbout(ss []HubServer, here, tail string) string {
	if len(ss) == 0 {
		if here != "" {
			return tail + " " + here
		}
		return tail
	}
	out := HubLine(ss) + "\n"
	if here != "" {
		out += here + "\n"
	}
	return out + hubDetails(ss, hubSayMax-runeLen(out)-runeLen(tail)) + tail
}

// hubDetails is a line for each server that magpie knows anything of,
// within max characters in all: its name, its own title, what it is for,
// and its tools, each with the first clause of its description. A line
// too long for its share says its tools by name only, then as many of
// them as fit; the room a short line leaves goes to the others.
func hubDetails(ss []HubServer, max int) string {
	order := make([]int, 0, len(ss))
	full := make([]string, len(ss))
	for i, s := range ss {
		if full[i] = hubServerLine(s, 1<<20); full[i] != "" {
			order = append(order, i)
		}
	}
	slices.SortStableFunc(order, func(a, b int) int { return runeLen(full[a]) - runeLen(full[b]) })
	lines := make([]string, len(ss))
	left := max
	for j, i := range order {
		share := left/(len(order)-j) - len("\n")
		if l := hubServerLine(ss[i], share); l != "" {
			lines[i] = l
			left -= runeLen(l) + len("\n")
		}
	}
	var out strings.Builder
	for _, l := range lines {
		if l != "" {
			out.WriteString(l + "\n")
		}
	}
	return out.String()
}

// hubServerLine is s's line in at most room characters: "- name (title):
// what it is for. Tools: a (what a does), b.", "" when magpie knows
// nothing of it or not even its name fits.
func hubServerLine(s HubServer, room int) string {
	head := "- " + s.Name
	if t := oneLine(s.Title); t != "" && !strings.EqualFold(t, s.Name) {
		head += " (" + t + ")"
	}
	about := strings.TrimSuffix(oneLine(s.About), ".")
	if about == "" && len(s.Tools) == 0 {
		return ""
	}
	if runeLen(head)+len(": .") > room {
		return ""
	}
	say := func(about string, tools []string, more int) string {
		var parts []string
		if about != "" {
			parts = append(parts, about)
		}
		switch {
		case len(tools) == 0 && more > 0:
			parts = append(parts, strconv.Itoa(more)+" tools")
		case more > 0:
			parts = append(parts, "Tools: "+strings.Join(tools, ", ")+", and "+strconv.Itoa(more)+" more")
		case len(tools) > 0:
			parts = append(parts, "Tools: "+strings.Join(tools, ", "))
		}
		return head + ": " + strings.Join(parts, ". ") + "."
	}
	names := make([]string, len(s.Tools))
	described := make([]string, len(s.Tools))
	for i, t := range s.Tools {
		names[i] = t.Name
		described[i] = t.Name
		if a := hubToolClause(t.About); a != "" {
			described[i] += " (" + a + ")"
		}
	}
	for _, tools := range [][]string{described, names} {
		if l := say(about, tools, 0); runeLen(l) <= room {
			return l
		}
	}
	// too long: what it is for in half the room at most, then as many
	// names as fit, the rest counted
	if about != "" {
		if n := room/2 - runeLen(head) - len(": ."); n < 12 {
			about = ""
		} else {
			about = strings.TrimSuffix(hubCut(about, n), "…")
		}
	}
	for kept := len(names); kept >= 0; kept-- {
		if l := say(about, names[:kept], len(names)-kept); runeLen(l) <= room {
			return l
		}
	}
	if about != "" {
		return hubCut(say(about, nil, 0), room)
	}
	return ""
}

// hubToolClause is what a tool's description says first, in a few words:
// a router's own "[name] " before it left out, since the line names the
// server already.
func hubToolClause(d string) string {
	d = oneLine(d)
	if strings.HasPrefix(d, "[") {
		if i := strings.Index(d, "] "); i > 0 {
			d = d[i+2:]
		}
	}
	return hubCut(hubClause(d), 80)
}

// oneLine is s with every run of spaces and line breaks one space.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// HubLine is one line naming the servers behind the hub, each with what
// it is for cut to fit hubLineMax: "" when there is none.
func HubLine(ss []HubServer) string {
	if len(ss) == 0 {
		return ""
	}
	const head = "Tools of "
	const room = hubLineMax - len(".")
	short := make([]string, len(ss))
	for i, s := range ss {
		// another magpie's router says "Tools of a (…), b.": the line
		// says "Tools of" once
		short[i] = hubClause(strings.TrimPrefix(oneLine(s.About), head))
	}
	line := func(abouts []string, kept int) string {
		parts := make([]string, 0, kept+1)
		for i, s := range ss[:kept] {
			if abouts[i] != "" {
				parts = append(parts, s.Name+" ("+abouts[i]+")")
			} else {
				parts = append(parts, s.Name)
			}
		}
		if n := len(ss) - kept; n > 0 {
			parts = append(parts, "and "+strconv.Itoa(n)+" more")
		}
		return head + strings.Join(parts, ", ")
	}
	// the names first: as many as fit, the rest counted
	none := make([]string, len(ss))
	kept := len(ss)
	for kept > 0 && runeLen(line(none, kept)) > room {
		kept--
	}
	// what's left goes to the abouts, the short ones whole, the rest an
	// equal share, cut at a word; a share too small to say anything is none
	abouts := make([]string, len(ss))
	left := room - runeLen(line(none, kept))
	order := make([]int, 0, kept)
	for i := range kept {
		if short[i] != "" {
			order = append(order, i)
		}
	}
	slices.SortStableFunc(order, func(a, b int) int { return runeLen(short[a]) - runeLen(short[b]) })
	for j, i := range order {
		share := left/(len(order)-j) - len(" ()")
		if share < 12 {
			continue
		}
		abouts[i] = hubCut(short[i], share)
		left -= runeLen(abouts[i]) + len(" ()")
	}
	return line(abouts, kept) + "."
}

// hubClause is the first part of a description: up to its first comma,
// colon, semicolon or full stop outside brackets, on one line, so another
// magpie's "Tools of a (what a is for, …), b." isn't cut inside a's.
func hubClause(s string) string {
	s = oneLine(s)
	depth := 0
	for i, c := range s {
		switch {
		case c == '(' || c == '[':
			depth++
		case (c == ')' || c == ']') && depth > 0:
			depth--
		case depth == 0 && i > 0 && (c == ',' || c == ':' || c == ';' || c == '.' && strings.HasPrefix(s[i:], ". ")):
			return strings.TrimSuffix(s[:i], ".")
		}
	}
	return strings.TrimSuffix(s, ".")
}

// hubCut is s in at most n characters, cut at a word with an ellipsis.
func hubCut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	c := string(r[:n-1])
	if i := strings.LastIndex(c, " "); i > 0 {
		c = c[:i]
	}
	return strings.TrimRight(c, " ,") + "…"
}

func runeLen(s string) int { return len([]rune(s)) }

// hubDescribeWait is how long initialize waits for the tools of servers
// magpie hasn't listed yet (one just added, edited or signed in to), to
// name them in its instructions: the agent's tools/list, which comes next,
// waits for the same servers anyway. One not listed by then is named
// without its tools, and with them to the next agent that connects.
var hubDescribeWait = 5 * time.Second

// hubServersFor are the servers behind agent's tools, as the instructions
// describe them, their tools listed first when magpie hasn't yet.
func hubServersFor(ctx context.Context, agent string) []HubServer {
	ss := hubServersOf(ctx, agent)
	if !MCPHub.On() || !slices.ContainsFunc(ss, func(s HubServer) bool { return !s.Known }) {
		return ss
	}
	wait, cancel := context.WithTimeout(ctx, hubDescribeWait)
	defer cancel()
	MCPHub.Tools(wait, agent)
	return hubServersOf(ctx, agent)
}

func (s *Server) hubMCPCall(r *http.Request, agent string, m mcpMessage) (any, map[string]any) {
	switch m.Method {
	case "initialize":
		say := "The tools of the MCP servers in magpie's Library that are switched on for this agent, each named <server>__<tool>. Add servers, or switch them on for an agent, in magpie's Library → MCP."
		if agent == "" {
			say = "The tools of every MCP server in magpie's Library, each named <server>__<tool>. Add servers in magpie's Library → MCP."
		}
		title, here := "Magpie MCP hub", ""
		if w := hubWhere(r); w != "" {
			here = "This router: " + w + ". Each of its tools' descriptions starts with [" + w + "]; when another MCP server gives the same tools, choose between them by this."
			title += " · " + w
		}
		var ss []HubServer
		if MCPHub != nil {
			ss = hubServersFor(r.Context(), agent)
		}
		if hubOnDemand(agent, hubToolCount(ss)) {
			say += hubOnDemandSay
		}
		say = hubAbout(ss, here, say)
		if MCPHub == nil || !MCPHub.On() {
			say = errHubOff
		}
		return map[string]any{
			"protocolVersion": mcpVersion(m),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "magpie", "title": title, "version": Version},
			"instructions":    say,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		ts := hubTools(r.Context(), agent)
		where := hubWhere(r)
		if hubOnDemand(agent, len(ts)) {
			// hubTools ran, so each server's tools are as just listed
			return map[string]any{"tools": hubOnDemandSpecs(where, hubServersOf(r.Context(), agent))}, nil
		}
		list := make([]any, 0, len(ts))
		for _, t := range ts {
			list = append(list, hubSpecOf(t, where))
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      hubCallMeta     `json:"_meta"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, map[string]any{"code": -32602, "message": "invalid params: " + err.Error()}
		}
		if MCPHub == nil || !MCPHub.On() {
			return toolResult(errHubOff, true), nil
		}
		ts := hubTools(r.Context(), agent)
		// the two tools an agent given its tools on demand calls them by
		// (hub_ondemand.go), taken whatever it was given: what it lists
		// can change between its list and its call
		switch p.Name {
		case hubFindTool:
			return hubFind(ts, p.Arguments, hubWhere(r)), nil
		case hubCallTool:
			return s.hubCallByName(r, agent, ts, p.Arguments, p.Meta), nil
		}
		if t, ok := hubToolNamed(r.Context(), ts, agent, p.Name); ok {
			return s.hubCall(r, agent, t, p.Arguments, p.Meta), nil
		}
		if agent == "" {
			return nil, map[string]any{"code": -32602, "message": "unknown tool " + p.Name + ": no MCP server in magpie's Library gives it"}
		}
		return nil, map[string]any{"code": -32602, "message": "unknown tool " + p.Name + ": no MCP server switched on for " + agent + " in magpie's Library gives it"}
	}
	return nil, map[string]any{"code": -32601, "message": "method not found: " + m.Method}
}

// hubToolNamed is the tool name calls: one ts lists, or else a tool of a
// server switched on for agent that its name starts with, <server>__<tool>,
// which the server itself is asked for. An agent lists the tools once, and
// a server's list can change after: magpie updated, Magpie Browser's
// browser_tabs became the browser tool's tabs action, and it still takes
// the old name (7e201ce20). A server whose list failed just now is asked
// the same way, and either says why it can't run the tool.
func hubToolNamed(ctx context.Context, ts []hubTool, agent, name string) (hubTool, bool) {
	for _, t := range ts {
		if t.name == name {
			return t, true
		}
	}
	var found []hubTool
	for _, sv := range hubServersOf(ctx, agent) {
		tool, ok := strings.CutPrefix(name, unsafeToolChars.ReplaceAllString(sv.Name, "-")+hubSep)
		if !ok || tool == "" {
			continue
		}
		t := hubTool{HubTool: HubTool{Server: sv.Name, Tool: tool}, name: name}
		for _, l := range ts {
			if l.Server == sv.Name {
				t.Roots = l.Roots
				break
			}
		}
		found = append(found, t)
	}
	// two servers of one name in the hub's characters: which is meant isn't known
	if len(found) != 1 {
		return hubTool{}, false
	}
	return found[0], true
}

// hubCall runs t for the caller r is: its session, and for a tool that
// reads them, its workspaces and client (HubCaller).
func (s *Server) hubCall(r *http.Request, agent string, t hubTool, args json.RawMessage, meta hubCallMeta) any {
	c := HubCaller{Session: hubSession(r, agent, meta)}
	if v := hubVisitor(r); v.Away && v.Key != "" {
		// its browser is its key's, whatever session it names (browser.go)
		c.Session = keyedSession(v.Key, c.Session)
	}
	if t.Roots {
		c.Roots, c.Client = hubRoots(r, meta), hubClient(r)
	}
	r = r.WithContext(WithHubCaller(r.Context(), c))
	return s.hubMCPRun(r, agent, t, args)
}

// hubMCPRun runs one tool on its server, held to the calling gateway key's
// limit. A server that is down or doesn't answer in time is an error
// result naming it, never a wait past the client's.
func (s *Server) hubMCPRun(r *http.Request, agent string, t hubTool, args json.RawMessage) any {
	// the request log names the Library's server, and the agent the hub is mounted for
	use := mcpUseOf(r.Context())
	use.on(t.Server)
	use.as(agent)
	use.named(t.name)
	release, err := reserveFor(access.Caller(r.Context()), int64(len(args)), "", limitClock())
	if err != nil {
		return toolResult(err.Error(), true)
	}
	defer release()
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	ctx, cancel := context.WithTimeout(r.Context(), hubCallWait)
	defer cancel()
	res, err := MCPHub.Call(ctx, agent, t.Server, t.Tool, args)
	if err != nil {
		why := err.Error()
		if ctx.Err() != nil && r.Context().Err() == nil {
			why = "it didn't answer in " + hubCallWait.String()
		}
		return toolResult("The MCP server "+t.Server+" couldn't run "+t.Tool+": "+why, true)
	}
	if t.Roots {
		// magpie's image server draws through the gateway on its own
		// connection, so its result names the model for this call's row
		var m struct {
			Meta struct {
				Model string `json:"magpie/model"`
			} `json:"_meta"`
		}
		json.Unmarshal(res, &m)
		use.usedRef(m.Meta.Model)
	}
	return res
}

// hubCallMeta is what of a tool call's _meta the hub reads: the thread
// Codex names its calls by, and the workspaces its turn metadata names
// (the git repositories it works in, keyed by their folder).
type hubCallMeta struct {
	ThreadID string `json:"threadId"`
	// Session is the caller's session as another magpie's router passed
	// it on (HubSessionMeta)
	Session string `json:"magpie/session"`
	Turn    struct {
		Workspaces map[string]json.RawMessage `json:"workspaces"`
	} `json:"x-codex-turn-metadata"`
}

// hubSession is which caller of the hub r is: the session another
// magpie's router names, which is its own caller's (that router reaches
// this one on one connection for all of them), else the session this
// client was given at initialize (serveMCP), else none, and the server
// then keeps one thing for every caller, as before clients were told
// apart. A caller can name any session, but each is only a tab of the
// user's browser, which a caller can pick by its id anyway.
func hubSession(r *http.Request, agent string, meta hubCallMeta) string {
	if v := boundedSession(meta.Session); v != "" {
		return v
	}
	if v := boundedSession(r.Header.Get("Mcp-Session-Id")); v != "" {
		return "hub-" + agent + "-" + v
	}
	return ""
}

// newMCPSession is a session id for a client of one of magpie's MCP
// servers.
func newMCPSession() string { return "magpie-" + rand.Text() }

// hubWhere is what the router says of itself to the agent of r, for an
// agent given the same tools by two routers (one in China, one in Los
// Angeles) to tell them apart: the user's own words (settings'
// RouterNote, set under the router's switch), else this computer's name
// to an agent on another computer. An agent on this computer with no
// words set is told nothing: there it is the router at hand, and the
// computer's name stays off its prompts.
func hubWhere(r *http.Request) string {
	if n := settings.CleanRouterNote(settings.Shared().RouterNote); n != "" {
		return n
	}
	if !local(r) {
		return hostName()
	}
	return ""
}

// HostName is this computer's name, what the router says of itself to an
// agent on another computer when the user set no words of their own.
func HostName() string { return hostName() }

// hubRootsWait is how long the agent is given to say its roots.
var hubRootsWait = 3 * time.Second

// hubRoots are the folders of the agent calling, as file URIs: what it
// answers roots/list with, asked on the call's own stream (Claude Code
// answers), else the workspaces of Codex's turn the call names, else the
// folder of the Codex thread it names (Codex declares no roots, and
// answers none). An agent on another machine has none here.
func hubRoots(r *http.Request, meta hubCallMeta) []string {
	if !local(r) {
		return nil
	}
	var roots []string
	if m, err := askClient(r.Context(), "roots/list", nil, hubRootsWait); err == nil && len(m.Result) > 0 {
		var res struct {
			Roots []struct {
				URI string `json:"uri"`
			} `json:"roots"`
		}
		json.Unmarshal(m.Result, &res)
		for _, root := range res.Roots {
			if strings.HasPrefix(root.URI, "file:") {
				roots = append(roots, root.URI)
			}
		}
	}
	if len(roots) == 0 {
		// what the call says itself first; a thread's rollout is read
		// only when it says no workspace (a folder that isn't a repository)
		for _, dir := range slices.Sorted(maps.Keys(meta.Turn.Workspaces)) {
			if filepath.IsAbs(dir) {
				roots = append(roots, fileURI(dir))
			}
		}
	}
	if len(roots) == 0 && meta.ThreadID != "" {
		if dir := sessions.CodexThreadCwd(meta.ThreadID); dir != "" {
			roots = append(roots, fileURI(dir))
		}
	}
	return roots
}

// fileURI is the file:// URI of folder dir: file:///C:/p for C:\p.
func fileURI(dir string) string {
	p := filepath.ToSlash(dir)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// hubClient is the product a request's User-Agent starts with
// (claude-code/2.1.314, codex-mcp-client/0.165.0).
func hubClient(r *http.Request) string {
	c, _, _ := strings.Cut(strings.TrimSpace(r.Header.Get("User-Agent")), " ")
	return c
}

// hubPruneEvery is how often the gateway ends the servers the hub no
// longer gives. An agent given the servers' own entries again never asks
// the hub, so it is looked for, not waited for.
var hubPruneEvery = 5 * time.Second

// keepHubPruned prunes the hub's servers while ctx lasts.
func keepHubPruned(ctx context.Context) {
	tick := time.NewTicker(hubPruneEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if MCPHub != nil {
				MCPHub.Prune()
			}
		}
	}
}

// StopHub ends the servers the hub started: magpie is stopping.
func StopHub() {
	if MCPHub != nil {
		MCPHub.Stop()
	}
}

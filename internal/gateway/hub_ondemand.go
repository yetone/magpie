package gateway

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
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
// them as xd:// devices named in a catalog, and Antigravity and agy, which
// list an MCP tool by name only and read its schema file when it is called
// for. Any other agent sends every
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
// Antigravity (agy 1.5.12) loads MCP tools lazily: its prompt names each
// server's tools and its own call tool for a lazy MCP tool says "Read the
// tool's schema file", one JSON file per tool under
// ~/.gemini/antigravity-cli/mcp/<server>/ (the binary's McpToolConfig has
// enable_lazy_load and SplitEnabledToolsByEagerLazy; a server marks a tool
// always loaded with `_meta` "antigravity/alwaysLoad"). Given find_tools in
// their place it searched twice (izhidigua on X, 11-19).
// The Library's Tools on demand row names them from HubFindsItself, so its
// words can't leave one out again (they named four of the six, 11-18).
var hubSearchers = []string{"claude", "claude-desktop", "codex", "pi", "droid", "omp", "agy", "antigravity"}

var hubSearchesItself = func() map[string]bool {
	m := map[string]bool{}
	for _, id := range hubSearchers {
		m[id] = true
	}
	return m
}()

// HubFindsItself is the agents given every tool whole because they search
// their MCP tools themselves (hubSearchesItself), by id, in the order the
// Library names them.
func HubFindsItself() []string { return slices.Clone(hubSearchers) }

// HubOnDemandOver is hubOnDemandOver, for the Library to say.
const HubOnDemandOver = hubOnDemandOver

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
// digit (create_issue is create and issue), and where a script written
// without spaces (hubSpaceless) meets one that isn't (截图screenshot is
// 截图 and screenshot).
func hubWords(s string) []string {
	var out []string
	var b strings.Builder
	spaceless := false
	for _, r := range strings.ToLower(s) {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
			continue
		}
		if b.Len() > 0 && hubSpaceless(r) != spaceless {
			out = append(out, b.String())
			b.Reset()
		}
		spaceless = hubSpaceless(r)
		b.WriteRune(r)
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// hubSpaceless is whether r is of a script written without spaces between
// its words: Chinese, Japanese and Korean (whose words carry their
// particles).
func hubSpaceless(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// hubStop are words of a query that say nothing of what a tool does.
var hubStop = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "to": true, "for": true, "in": true, "on": true, "at": true,
	"by": true, "with": true, "and": true, "or": true, "from": true, "into": true, "is": true, "are": true, "be": true,
	"it": true, "its": true, "this": true, "that": true, "my": true, "me": true, "i": true, "you": true, "your": true,
	"some": true, "please": true, "tool": true, "tools": true, "can": true, "do": true, "how": true, "what": true,
}

// hubStopRunes are characters of a spaceless query that say nothing of
// what a tool does, where hubSpacelessKeys cuts it: Chinese pronouns,
// particles and measure words (帮我截个图 is 截 and 图), and Japanese
// particles (スクリーンショットを撮る). Only those that seldom start or
// end a word a tool is described with: 请 is in 请求, 地 in 地图, so
// neither is one.
var hubStopRunes = func() map[rune]bool {
	m := map[rune]bool{}
	for _, r := range "我你他她它们的了吗呢吧啊呀么个帮把给这那一をはがのにでとへも" {
		m[r] = true
	}
	return m
}()

// hubSpacelessKeys are the key words of w, a word of a spaceless script:
// w cut at hubStopRunes, each piece one key word (帮我截个图 is 截 and 图),
// but a lone character that isn't Han (a kana says nothing alone).
func hubSpacelessKeys(w string) []string {
	var out []string
	for _, piece := range strings.FieldsFunc(w, func(r rune) bool { return hubStopRunes[r] }) {
		if rs := []rune(piece); len(rs) > 1 || unicode.Is(unicode.Han, rs[0]) {
			out = append(out, piece)
		}
	}
	return out
}

// hubPhraseIn is whether w, a key word of a spaceless script, is in text:
// a character is, and a longer one has at least half of its runs of two
// characters there. Its words aren't known, but 屏幕截图 and 截取屏幕 share
// 屏幕, and of 发送飞书消息's five runs, 发送, 飞书 and 消息 are words a
// description says and 送飞 and 书消 are not.
func hubPhraseIn(w, text string) bool {
	rs := []rune(w)
	if len(rs) == 1 {
		return strings.Contains(text, w)
	}
	in := 0
	for i := 0; i+1 < len(rs); i++ {
		if strings.Contains(text, string(rs[i:i+2])) {
			in++
		}
	}
	return in*2 >= len(rs)-1
}

// hubKeyWords are query's words that say what is wanted: hubWords but
// the stop words and single letters, a spaceless word as
// hubSpacelessKeys, each once.
func hubKeyWords(query string) []string {
	var out []string
	add := func(w string) {
		if !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	for _, w := range hubWords(query) {
		if r := []rune(w); hubSpaceless(r[0]) {
			for _, k := range hubSpacelessKeys(w) {
				add(k)
			}
			continue
		}
		if hubStop[w] || len(w) < 2 {
			continue
		}
		add(w)
	}
	return out
}

// hubNonASCII is whether s has a letter not in ASCII (an em dash or a
// curly quote in an English description isn't one).
func hubNonASCII(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r > unicode.MaxASCII && unicode.IsLetter(r) }) >= 0
}

// hubWordIn is whether w is one of words, or, for a word of four letters
// or more, the start of one (repo is repository's, open is opens'), or one
// of four letters or more is w's start (issues has issue). A word of a
// spaceless script is as hubPhraseIn finds it; another not in ASCII is
// anywhere in text.
func hubWordIn(w string, words []string, text string) bool {
	if hubSpaceless([]rune(w)[0]) {
		return hubPhraseIn(w, text)
	}
	if hubNonASCII(w) {
		return strings.Contains(text, w)
	}
	for _, x := range words {
		if x == w || len(w) >= 4 && strings.HasPrefix(x, w) || len(x) >= 4 && strings.HasPrefix(w, x) {
			return true
		}
	}
	return false
}

// hubMatch is how well t matches query: how many of its key words are in
// t's router name or its server's (in names) and how many more in its
// description only, of keys, the key words that count for t.
// exact is the query being t's name, or its own.
type hubMatch struct {
	t                  hubTool
	names, descs, keys int
	exact              bool
}

// hubMatchOf is t's hubMatch for query, with keys its key words. A key
// word not in ASCII counts only for a tool whose name, server or
// description has such letters: an English description never has 截图,
// and a query of 截图 screenshot is screenshot to it (Passings_z on X,
// 11-19: an agent asking in Chinese found none of the tools there were).
func hubMatchOf(t hubTool, query string, keys []string) hubMatch {
	m := hubMatch{t: t}
	q := strings.TrimSpace(query)
	m.exact = q != "" && (strings.EqualFold(t.name, q) || strings.EqualFold(t.Tool, q))
	name := strings.ToLower(t.name + " " + t.Server)
	desc := strings.ToLower(hubDesc(t))
	nw, dw := hubWords(name), hubWords(desc)
	own := hubNonASCII(name) || hubNonASCII(desc)
	for _, w := range keys {
		if !own && hubNonASCII(w) {
			continue
		}
		m.keys++
		switch {
		case hubWordIn(w, nw, name):
			m.names++
		case hubWordIn(w, dw, desc):
			m.descs++
		}
	}
	return m
}

// clears is whether m is a match at all (sahuaja3669 on X, 11-18: a
// router mustn't hand over a tool of a near name that does something
// else): the query is its name, or it has at least two thirds of the
// key words that count for it, one of them in its name unless it has
// them all. A word or two of a longer ask, in a description, is not what
// was asked for.
func (m hubMatch) clears() bool {
	if m.exact {
		return true
	}
	got := m.names + m.descs
	return m.keys > 0 && got*3 >= m.keys*2 && (m.names > 0 || got == m.keys)
}

// score orders the matches: the query as a name first, then a word in
// the name counting three and in the description one.
func (m hubMatch) score() int {
	n := 3*m.names + m.descs
	if m.exact {
		n += 100
	}
	return n
}

// hubMatches are ts that match query (hubMatch.clears), best first: none
// when no tool does what it asks.
func hubMatches(ts []hubTool, query string) []hubTool {
	keys := hubKeyWords(query)
	var out []hubMatch
	for _, t := range ts {
		if m := hubMatchOf(t, query, keys); m.clears() {
			out = append(out, m)
		}
	}
	slices.SortStableFunc(out, func(a, b hubMatch) int { return cmp.Compare(b.score(), a.score()) })
	ms := make([]hubTool, len(out))
	for i, m := range out {
		ms[i] = m.t
	}
	return ms
}

// hubNear are ts whose names share a word with name, most first: the
// tools a mistyped name (create_isue) may have meant.
func hubNear(ts []hubTool, name string) []hubTool {
	keys := hubKeyWords(name)
	var out []hubMatch
	for _, t := range ts {
		m := hubMatchOf(t, name, keys)
		if m.descs = 0; m.names > 0 {
			out = append(out, m)
		}
	}
	slices.SortStableFunc(out, func(a, b hubMatch) int { return cmp.Compare(b.score(), a.score()) })
	ms := make([]hubTool, len(out))
	for i, m := range out {
		ms[i] = m.t
	}
	return ms
}

// hubServersSaid is the most tools hubServerCounts names of a server
// when naming them all would be longer than hubSayMax.
const hubServersSaid = 8

// hubServerCounts name ts' servers, in their order, each with how many
// tools it has and their own names: what there is, said as what there is
// and not as a match. Every tool is named when that is hubSayMax
// characters at most, else the first hubServersSaid of each server's, and
// all is false. Naming only some, a model looking for one of the others
// had no way to it (Passings_z on X, 11-19).
func hubServerCounts(ts []hubTool) (said string, all bool) {
	var names []string
	of := map[string][]string{}
	for _, t := range ts {
		if of[t.Server] == nil {
			names = append(names, t.Server)
		}
		of[t.Server] = append(of[t.Server], t.Tool)
	}
	say := func(most int) (string, bool) {
		all := true
		parts := make([]string, len(names))
		for i, s := range names {
			tools := of[s]
			n := strconv.Itoa(len(tools)) + " tools"
			if len(tools) == 1 {
				n = "1 tool"
			}
			said := strings.Join(tools[:min(len(tools), most)], ", ")
			if len(tools) > most {
				said += ", …"
				all = false
			}
			parts[i] = s + " (" + n + ": " + said + ")"
		}
		return strings.Join(parts, "; "), all
	}
	if said, all := say(len(ts)); runeLen(said) <= hubSayMax {
		return said, all
	}
	return say(hubServersSaid)
}

// hubNotFound is find_tools' answer when no tool matches query: that none
// was found, every server with its tools (hubServerCounts), and how to
// look again; when the query has words not in ASCII and no tool has such
// letters, that the tools are named and described in English, since a
// query in another language can match none of them.
func hubNotFound(ts []hubTool, query string) string {
	said, all := hubServerCounts(ts)
	out := "No tool was found for \"" + query + "\": no tool's name or description has what it asks. Don't call a tool that only sounds close. " +
		"The servers and their tools: " + said + ". "
	if !all {
		out += hubFindTool + " with an empty query names every tool. "
	}
	if hubNonASCII(query) && !slices.ContainsFunc(ts, func(t hubTool) bool { return hubNonASCII(t.name + " " + t.Server + " " + hubDesc(t)) }) {
		out += "The tools are named and described in English: " + hubFindTool + " with English words looks again."
	} else {
		out += hubFindTool + " with other words, a server or a tool's name looks again."
	}
	return out
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
// more by name; with no query every tool by name, and with none matching
// that no tool was found, and the servers.
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
		return toolResult(hubNotFound(ts, p.Query), false)
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
		if ms := hubNear(ts, name); len(ms) > 0 {
			why += " Close to it:\n" + hubNameLines(ms[:min(len(ms), hubFindFull)])
		} else {
			why += " " + hubFindTool + " gives the tools' names."
		}
		return toolResult(why, true)
	}
	return s.hubCall(r, agent, t, inner, meta)
}

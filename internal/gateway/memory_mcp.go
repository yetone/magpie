package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	// search by meaning, when switched on (memory.Semantic)
	_ "github.com/yetone/magpie/internal/memembed"
	"github.com/yetone/magpie/internal/memory"
	"github.com/yetone/magpie/internal/usage"
)

// magpie's memory (internal/memory) is an MCP server every agent can mount,
// so what one agent learns of the user, another knows: memory_search,
// memory_save and memory_forget, Streamable HTTP at MemoryMCPPath, served
// as SearchMCPPath is (serveMCP), to the agents on this computer alone.
// The Library gives it as `magpie mcp memory`, which passes each message on with the folder the
// agent started it in (MemoryCwdHeader) and the client's name from its
// initialize (MemoryClientHeader); behind the hub, the call's _meta names
// the agent's roots and client (HubRootsMeta, HubClientMeta).
//
// A memory is kept for everyone (global), for one project (the git
// repository the agent works in, by its remote, so every clone and
// worktree shares it) or for one agent. Searches look in the global ones,
// the agent's own and the project's.

// MemoryMCPPath is where the memory MCP server is served.
const MemoryMCPPath = "/mcp/magpie/memory"

// MemoryCwdHeader and MemoryClientHeader are the folder `magpie mcp
// memory` was started in and the client name the agent gave at initialize.
const (
	MemoryCwdHeader    = "X-Magpie-Cwd"
	MemoryClientHeader = "X-Magpie-Mcp-Client"
)

const (
	memorySearchTool = "memory_search"
	memorySaveTool   = "memory_save"
	memoryForgetTool = "memory_forget"
)

const memoryServerName = "magpie-memory"

func (s *Server) memoryMCP(w http.ResponseWriter, r *http.Request) {
	// what is known about this computer's user, and what goes into every
	// agent's instructions here (the digest): only an agent on this
	// computer reads or writes it, as the hub gives it (HereOnly). Another
	// computer's gateway key, which may be a friend's, and a web page,
	// even one Settings lists, are turned away.
	if !local(r) || fromBrowser(r) {
		http.Error(w, "magpie's memory MCP server answers the agents on this computer alone", http.StatusForbidden)
		return
	}
	serveMCP(w, r, "magpie's memory", memoryServerName, 1<<20, s.memoryMCPCall)
}

var memoryKinds = strings.Join(memory.Kinds, ", ")

func memoryMCPTools() []any {
	return []any{
		map[string]any{
			"name":  memorySearchTool,
			"title": "Search memory",
			"description": "Searches what is remembered from earlier conversations with the user, in any agent: their preferences, rules, facts about them and their projects, how things are done. " +
				"Search before asking the user something they may have told you before, and when starting work in a project. With no query, lists the memories.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "What to look for, in a few words"},
					"scope": map[string]any{"type": "string", "enum": []string{"all", "project", "global"}, "description": "project: this project's memories; global: those for every project; all (default): both, and yours"},
					"kind":  map[string]any{"type": "string", "description": "Only memories of this kind: " + memoryKinds},
					"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Only memories with every one of these tags"},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "default": 10},
				},
			},
			"annotations": map[string]any{"readOnlyHint": true, "openWorldHint": false},
		},
		map[string]any{
			"name":  memorySaveTool,
			"title": "Save memory",
			"description": "Remembers one thing for later conversations, in every agent: what the user asks you to remember, a preference or rule they state, a correction they make, a fact about them or the project that will still hold next time. " +
				"One fact per memory, written so it reads on its own. Don't save what is in the code or its history, a task's progress, or secrets. Saving what is already remembered merges into it; with id, changes that memory.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":  map[string]any{"type": "string", "description": "The memory, one self-contained fact or instruction"},
					"kind":  map[string]any{"type": "string", "enum": memory.Kinds, "description": "profile: who the user is; preference: how they like things; rule: what must or must not be done; reference: where something is; fact: something true of a project; procedure: how something is done; episode: a decision or event worth recalling"},
					"scope": map[string]any{"type": "string", "enum": []string{"project", "global", "agent"}, "description": "project: holds only in this project (default for facts, references, procedures and episodes); global: holds everywhere (default for profile, preference and rule); agent: only for you"},
					"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "A few words to find it by (a topic, a tool, a language)"},
					"why":   map[string]any{"type": "string", "description": "Why it holds, when the user said, so it can be judged later"},
					"id":    map[string]any{"type": "string", "description": "A memory's id, to change that memory rather than save a new one"},
				},
				"required": []string{"text"},
			},
			"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false},
		},
		map[string]any{
			"name":        memoryForgetTool,
			"title":       "Forget memory",
			"description": "Sets a memory aside when the user says it is wrong or no longer holds. It is kept for the user to look at, but no longer searched or given to agents.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{"id": map[string]any{"type": "string", "description": "The memory's id, from memory_search"}},
				"required":   []string{"id"},
			},
			"annotations": map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": true, "openWorldHint": false},
		},
	}
}

// memoryMCPCall answers one request: its result, or its JSON-RPC error.
func (s *Server) memoryMCPCall(r *http.Request, m mcpMessage) (any, map[string]any) {
	switch m.Method {
	case "initialize":
		rememberMemoryClient(r, m)
		return map[string]any{
			"protocolVersion": mcpVersion(m),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": memoryServerName, "title": "Magpie Memory", "version": Version},
			"instructions":    "Memory shared by every agent the user works with. Search it before asking the user something they may have said before; save what they ask to be remembered, and preferences, rules and corrections they state; forget what they say no longer holds.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": memoryMCPTools()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      json.RawMessage `json:"_meta"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, map[string]any{"code": -32602, "message": "invalid params: " + err.Error()}
		}
		var a memoryArgs
		if len(p.Arguments) > 0 {
			if err := json.Unmarshal(p.Arguments, &a); err != nil {
				return toolResult("the arguments don't read: "+err.Error(), true), nil
			}
		}
		c := memoryCallerOf(r, p.Meta)
		var said string
		var err error
		switch p.Name {
		case memorySearchTool:
			said, err = memorySearch(c, a)
		case memorySaveTool:
			said, err = memorySave(c, a)
		case memoryForgetTool:
			said, err = memoryForget(a)
		default:
			return nil, map[string]any{"code": -32602, "message": "unknown tool " + p.Name + ": this server has " + memorySearchTool + ", " + memorySaveTool + " and " + memoryForgetTool}
		}
		if err != nil {
			return toolResult(err.Error(), true), nil
		}
		return toolResult(said, false), nil
	}
	return nil, map[string]any{"code": -32601, "message": "method not found: " + m.Method}
}

type memoryArgs struct {
	Query string    `json:"query"`
	Text  string    `json:"text"`
	Kind  string    `json:"kind"`
	Scope string    `json:"scope"`
	Tags  flexTags  `json:"tags"`
	Why   string    `json:"why"`
	ID    string    `json:"id"`
	Limit flexCount `json:"limit"`
}

// flexTags are tags as a model may write them: a list, or one string of
// them split at commas.
type flexTags []string

func (t *flexTags) UnmarshalJSON(b []byte) error {
	var l []string
	if json.Unmarshal(b, &l) == nil {
		*t = l
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return nil // anything else is no tags
	}
	*t = strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '，' })
	return nil
}

// flexCount is a count as a model may write it: a number, or one in a
// string; 0 for anything else.
type flexCount int

func (c *flexCount) UnmarshalJSON(b []byte) error {
	if n := searchCountArg(b); n != nil {
		*c = flexCount(*n)
	}
	return nil
}

// memoryCaller is who calls: the agent, by magpie's id for it ("" when
// it can't be told), and the project it works in (nil when none).
type memoryCaller struct {
	agent   string
	project *memory.Project
	dir     string
}

// memoryCallerOf is the caller of r: the agent its key or User-Agent
// names, else the client the hub's _meta or `magpie mcp memory` names;
// the project of the folder the hub's _meta names, else the folder `magpie
// mcp memory` runs in, else the one the agent says when asked (roots/list)
// or the Codex turn names. An agent on another computer has no folder
// here.
func memoryCallerOf(r *http.Request, rawMeta json.RawMessage) memoryCaller {
	var meta hubCallMeta
	var extra struct {
		Roots  *[]string `json:"magpie/roots"`
		Client string    `json:"magpie/client"`
	}
	if len(rawMeta) > 0 {
		json.Unmarshal(rawMeta, &meta)
		json.Unmarshal(rawMeta, &extra)
	}
	// the client the hub or `magpie mcp memory` names first: their own
	// requests go under magpie's key and a Go User-Agent
	c := memoryCaller{}
	for _, name := range []string{extra.Client, r.Header.Get(MemoryClientHeader)} {
		if c.agent == "" && strings.TrimSpace(name) != "" {
			c.agent = memoryAgent(usage.AgentOf(name))
		}
	}
	if c.agent == "" {
		c.agent = memoryAgent(callerOf(r).agent)
	}
	if c.agent == "" {
		// the client the session's initialize named (#6392): Codex's
		// HTTP client says nothing of itself but there
		c.agent = memoryClients.of(r.Header.Get("Mcp-Session-Id"))
	}
	if !local(r) {
		return c
	}
	var dirs []string
	if extra.Roots != nil {
		// behind the hub: its roots, none when the agent has none
		dirs = rootDirs(*extra.Roots)
	} else if d := strings.TrimSpace(r.Header.Get(MemoryCwdHeader)); d != "" {
		dirs = []string{d}
	} else {
		dirs = rootDirs(hubRoots(r, meta))
	}
	for _, d := range dirs {
		if p := memory.ProjectOf(d); p != nil {
			c.project, c.dir = p, d
			break
		}
	}
	return c
}

// memoryAgent is id when it names an agent: not magpie itself, nor an
// HTTP library (curl, Go's), which says nothing of which agent; else "".
func memoryAgent(id string) string {
	l := strings.ToLower(strings.TrimSpace(id))
	if strings.HasPrefix(l, "codex") {
		// codex-mcp-client, codex-tui, codex_cli_rs: Codex, as `magpie
		// mcp memory` and the sessions know it
		return "codex"
	}
	if l == "" || l == "magpie" || l == "other" || l == "go-http-client" || usage.IsLibrary(l) {
		return ""
	}
	return id
}

// rootDirs are the folders of file URIs.
func rootDirs(uris []string) []string {
	out := []string{}
	for _, u := range uris {
		p, err := url.Parse(u)
		if err != nil || p.Scheme != "file" || p.Path == "" {
			continue
		}
		d := filepath.FromSlash(p.Path)
		if len(d) > 2 && d[0] == filepath.Separator && d[2] == ':' {
			d = d[1:] // file:///C:/p is C:\p
		}
		out = append(out, d)
	}
	return out
}

// scopes are the scopes a search for c looks in.
func (c memoryCaller) scopes(which string) ([]string, error) {
	proj := memory.ScopeOf(c.project)
	switch strings.ToLower(strings.TrimSpace(which)) {
	case "", "all":
		out := []string{memory.Global}
		if proj != "" {
			out = append(out, proj)
		}
		if c.agent != "" {
			out = append(out, "agent:"+c.agent)
			if c.agent == "codex" {
				// kept under Codex's MCP client name before it was Codex
				out = append(out, "agent:codex-mcp-client")
			}
		}
		return out, nil
	case "global":
		return []string{memory.Global}, nil
	case "project":
		if proj == "" {
			return nil, memoryNoProject()
		}
		return []string{proj}, nil
	}
	return nil, fmt.Errorf("scope %q isn't all, project or global", which)
}

func memoryNoProject() error {
	return searchMCPError("there is no project here: the folder you work in isn't known to the memory server. Use scope global, or work in a project folder")
}

func memorySearch(c memoryCaller, a memoryArgs) (string, error) {
	scopes, err := c.scopes(a.Scope)
	if err != nil {
		return "", err
	}
	q := memory.Query{Text: a.Query, Scopes: scopes, Tags: a.Tags, Limit: min(max(int(a.Limit), 0), 50)}
	if q.Limit == 0 {
		q.Limit = 10
	}
	if k := strings.ToLower(strings.TrimSpace(a.Kind)); k != "" {
		if !slices.Contains(memory.Kinds, k) {
			return "", fmt.Errorf("kind %q isn't one of %s", a.Kind, memoryKinds)
		}
		q.Kinds = []string{k}
	}
	hits, err := memory.Search(q)
	if err != nil {
		return "", fmt.Errorf("memory can't be read now: %v", err)
	}
	if len(hits) == 0 {
		if strings.TrimSpace(a.Query) == "" {
			return "Nothing is remembered here yet.", nil
		}
		return "Nothing remembered matches " + quotedQuery(a.Query) + ".", nil
	}
	var b strings.Builder
	for _, h := range hits {
		b.WriteString(memoryLine(h.Memory, c))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), nil
}

// memoryLine is one memory as an agent reads it: its id, kind, where it
// holds, tags, text and why.
func memoryLine(m *memory.Memory, c memoryCaller) string {
	where := "everywhere"
	switch {
	case strings.HasPrefix(m.Scope, "agent:"):
		where = "only for you"
	case strings.HasPrefix(m.Scope, "project:"):
		where = "this project"
		if s := memory.ScopeOf(c.project); s != m.Scope {
			where = "project " + strings.TrimPrefix(m.Scope, "project:")
		}
	}
	l := fmt.Sprintf("[%s] %s, %s", m.ID, m.Kind, where)
	if len(m.Tags) > 0 {
		l += ", #" + strings.Join(m.Tags, " #")
	}
	if m.Pinned {
		l += ", pinned"
	}
	l += ", " + memoryStamp(m.Updated) + ": " + strings.Join(strings.Fields(m.Text), " ")
	if m.Why != "" {
		l += " (why: " + strings.Join(strings.Fields(m.Why), " ") + ")"
	}
	return l
}

func quotedQuery(s string) string { b, _ := json.Marshal(strings.TrimSpace(s)); return string(b) }

func memorySave(c memoryCaller, a memoryArgs) (string, error) {
	in := memory.Input{}
	text := strings.TrimSpace(a.Text)
	if text == "" && a.ID == "" {
		return "", searchMCPError("memory_save needs the text to remember")
	}
	if text != "" {
		in.Text = &text
	}
	kind := strings.ToLower(strings.TrimSpace(a.Kind))
	if kind != "" {
		in.Kind = &kind
	}
	if a.Tags != nil {
		tags := []string(a.Tags)
		in.Tags = &tags
	}
	if w := strings.TrimSpace(a.Why); w != "" {
		in.Why = &w
	}
	which := strings.ToLower(strings.TrimSpace(a.Scope))
	if which == "" && a.ID == "" {
		which = "global"
		if c.project != nil && !slices.Contains([]string{"", memory.Profile, memory.Preference, memory.Rule}, kind) {
			which = "project"
		}
	}
	var scope string
	switch which {
	case "":
	case "global":
		scope = memory.Global
	case "project":
		if c.project == nil {
			return "", memoryNoProject()
		}
		scope = memory.ScopeOf(c.project)
	case "agent":
		if c.agent == "" {
			return "", searchMCPError("the memory server can't tell which agent you are, so it can't keep a memory for you alone: use scope global or project")
		}
		scope = "agent:" + c.agent
	default:
		return "", fmt.Errorf("scope %q isn't project, global or agent", a.Scope)
	}
	if scope != "" {
		in.Scope = &scope
	}
	if id := strings.TrimSpace(a.ID); id != "" {
		m, err := memory.Edit(id, in, false)
		if err != nil {
			return "", err
		}
		return "Changed: " + memoryLine(m, c), nil
	}
	if kind == "" {
		k := memory.Fact
		in.Kind = &k
	}
	src := &memory.Source{Agent: c.agent, Dir: c.dir}
	var proj *memory.Project
	if scope == memory.ScopeOf(c.project) {
		proj = c.project
	}
	m, added, err := memory.Save(in, memory.FromAgent, src, proj)
	if err != nil {
		return "", err
	}
	if !added {
		return "Already remembered, merged: " + memoryLine(m, c), nil
	}
	return "Remembered: " + memoryLine(m, c), nil
}

func memoryForget(a memoryArgs) (string, error) {
	id := strings.TrimSpace(a.ID)
	if id == "" {
		return "", searchMCPError("memory_forget needs the memory's id, from memory_search")
	}
	m, err := memory.Forget(id)
	if err != nil {
		return "", err
	}
	return "Forgotten: [" + m.ID + "] " + strings.Join(strings.Fields(m.Text), " "), nil
}

// memoryStamp is when a memory was last changed, in the reader's own zone
// with its offset (2026-11-11T04:30+08:00). A date alone, taken in UTC,
// put a memory on the day before or after the one the user lived
// (Norethcy on X). Memories saved before this are stored in UTC ("…Z")
// and are shown the same way.
func memoryStamp(t time.Time) string { return t.In(memoryZone()).Format("2006-01-02T15:04Z07:00") }

// memoryZone is the zone memoryStamp writes in: time.Local, a variable for
// tests, which must never swap time.Local itself (every goroutine still
// running reads it, so the swap is a race).
var memoryZone = func() *time.Location { return time.Local }

// memoryClients are the agents the clients of the memory server named at
// initialize, by Mcp-Session-Id: a client of the HTTP route says who it is
// there alone, and its calls carry no User-Agent of an agent's (#6392).
var memoryClients = &sessionClients{m: map[string]sessionClient{}}

type sessionClient struct {
	agent string
	at    time.Time
}

type sessionClients struct {
	mu sync.Mutex
	m  map[string]sessionClient
}

const (
	memoryClientsKept = 1024
	memoryClientsFor  = 24 * time.Hour
)

func (c *sessionClients) set(sid, agent string) {
	if sid == "" || agent == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if len(c.m) >= memoryClientsKept {
		for k, v := range c.m {
			if now.Sub(v.at) > memoryClientsFor {
				delete(c.m, k)
			}
		}
		for k := range c.m {
			if len(c.m) < memoryClientsKept {
				break
			}
			delete(c.m, k)
		}
	}
	c.m[sid] = sessionClient{agent, now}
}

func (c *sessionClients) of(sid string) string {
	if sid == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[sid].agent
}

// rememberMemoryClient keeps the client name an initialize gave under the
// session it was given (or sent on).
func rememberMemoryClient(r *http.Request, m mcpMessage) {
	var p struct {
		ClientInfo struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	agent := memoryAgent(usage.AgentOf(p.ClientInfo.Name))
	sid := r.Header.Get("Mcp-Session-Id")
	if x, _ := r.Context().Value(mcpExchangeKey{}).(*mcpExchange); x != nil && sid == "" {
		sid = x.w.Header().Get("Mcp-Session-Id")
	}
	memoryClients.set(sid, agent)
}

// RunClient names the agent behind a request to /mcp/run/<server>, the
// route only Codex is given: what its headers say, else Codex.
func RunClient(r *http.Request) string {
	if a := memoryAgent(agentOf(r)); a != "" {
		return a
	}
	return "codex"
}

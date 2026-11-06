package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/access"
	"github.com/yetone/magpie/internal/settings"
)

// fakeHub is the library's servers, as the gateway asks for them.
type fakeHub struct {
	mu    sync.Mutex
	on    bool
	tools map[string][]HubTool // by agent
	// servers are what Servers says, by agent
	servers map[string][]HubServer
	calls   []string
	call    func(ctx context.Context, server, tool string) (json.RawMessage, error)
}

func (f *fakeHub) On() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.on }

func (f *fakeHub) Tools(_ context.Context, agent string) []HubTool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tools[agent]
}

func (f *fakeHub) Servers(agent string) []HubServer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.servers[agent]
}

func (f *fakeHub) Call(ctx context.Context, agent, server, tool string, args json.RawMessage) (json.RawMessage, error) {
	f.mu.Lock()
	f.calls = append(f.calls, agent+" "+server+" "+tool+" "+string(args))
	call := f.call
	f.mu.Unlock()
	if call != nil {
		return call(ctx, server, tool)
	}
	return json.RawMessage(`{"content":[{"type":"text","text":"ran ` + server + ` ` + tool + `"}]}`), nil
}

func (f *fakeHub) Stop()  {}
func (f *fakeHub) Prune() {}

func (f *fakeHub) ran() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func hubSpec(name string) map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"name":        json.RawMessage(`"` + name + `"`),
		"description": json.RawMessage(`"Does ` + name + `."`),
		"inputSchema": json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`),
	}
}

func withHub(t *testing.T, f *fakeHub) {
	t.Helper()
	old := MCPHub
	MCPHub = f
	t.Cleanup(func() { MCPHub = old })
}

func hubPost(h http.Handler, agent, addr, body string, hdr ...string) *httptest.ResponseRecorder {
	return hubPostAt(h, HubMCPPath+agent, addr, body, hdr...)
}

func hubPostAt(h http.Handler, path, addr, body string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.RemoteAddr, r.Host = addr, "127.0.0.1:3425"
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

const here = "127.0.0.1:50000"

// The hub lists the tools of the servers ticked for the agent in its path,
// each <server>__<tool> in the characters a client takes, 64 at most, two
// of one name told apart, and runs each call on the server it is of.
func TestHubMCPListsAndRoutes(t *testing.T) {
	fresh(t)
	long := strings.Repeat("x", 70)
	f := &fakeHub{on: true, tools: map[string][]HubTool{
		"claude": {
			{Server: "github", Tool: "create_issue", Spec: hubSpec("create_issue")},
			{Server: "my server", Tool: "do.it", Spec: hubSpec("do.it")},
			{Server: "my.server", Tool: "do it", Spec: hubSpec("do it")},
			{Server: "fs", Tool: long, Spec: map[string]json.RawMessage{"name": json.RawMessage(`"` + long + `"`)}},
		},
		"codex": {{Server: "fs", Tool: "read", Spec: hubSpec("read")}},
	}}
	withHub(t, f)
	h := lanGuard(New().Handler())

	init := readMCP(t, hubPost(h, "claude", here, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))
	if init.Error != nil || init.Result.ServerInfo.Name != "magpie" || init.Result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("initialize: %+v", init)
	}
	m := readMCP(t, hubPost(h, "claude", here, toolsList))
	var names []string
	for _, x := range m.Result.Tools {
		names = append(names, x.Name)
		if len(x.Name) > 64 {
			t.Errorf("%q is past 64 characters", x.Name)
		}
	}
	want := []string{"github__create_issue", "my-server__do-it", "my-server__do-it_2", "fs__" + strings.Repeat("x", 60)}
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	if r := m.Result.Tools[0].InputSchema.Required; len(r) != 1 || r[0] != "q" {
		t.Errorf("schema not passed on: %v", r)
	}
	// a tool listed with no schema is given an empty one
	w := hubPost(h, "claude", here, toolsList)
	if !strings.Contains(w.Body.String(), `"inputSchema":{"type":"object","properties":{}}`) {
		t.Errorf("no default schema: %s", w.Body)
	}

	m = readMCP(t, hubPost(h, "claude", here, toolsCall("my-server__do-it_2", `{"q":"x"}`)))
	if m.Result.IsError || m.Result.Content[0].Text != "ran my.server do it" {
		t.Fatalf("call: %+v", m)
	}
	m = readMCP(t, hubPost(h, "codex", here, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fs__read"}}`))
	if m.Result.IsError || m.Result.Content[0].Text != "ran fs read" {
		t.Fatalf("codex's call: %+v", m)
	}
	// a tool the agent isn't given is unknown, even another agent's
	for _, c := range []struct{ agent, tool string }{{"codex", "github__create_issue"}, {"claude", "nope__x"}, {"gemini", "fs__read"}} {
		m := readMCP(t, hubPost(h, c.agent, here, toolsCall(c.tool, `{}`)))
		if m.Error == nil || m.Error.Code != -32602 || !strings.Contains(m.Error.Message, "unknown tool "+c.tool) {
			t.Errorf("%s's %s: %+v", c.agent, c.tool, m)
		}
	}
	if got := f.ran(); strings.Join(got, "|") != `claude my.server do it {"q":"x"}|codex fs read {}` {
		t.Errorf("ran %q", got)
	}
	if m := readMCP(t, hubPost(h, "claude", here, `{"jsonrpc":"2.0","id":4,"method":"resources/list"}`)); m.Error == nil || m.Error.Code != -32601 {
		t.Errorf("resources/list: %+v", m)
	}
}

// A tool name the agent listed before its server's list changed (Magpie
// Browser's browser_tabs, now the browser tool's tabs action) or while
// the server's list failed still goes to that server when it is switched
// on for the agent, for the server to run or refuse; another agent's
// server, or one no server's name starts, is still unknown.
func TestHubMCPCallsAToolNoLongerListed(t *testing.T) {
	fresh(t)
	f := &fakeHub{on: true,
		tools: map[string][]HubTool{
			"claude": {{Server: "magpie-browser", Tool: "browser", Spec: hubSpec("browser")}},
		},
		servers: map[string][]HubServer{
			"claude": {{Name: "magpie-browser"}, {Name: "my.fs"}},
			"codex":  {{Name: "github"}},
		},
	}
	withHub(t, f)
	h := lanGuard(New().Handler())
	for _, c := range []struct{ name, ran string }{
		{"magpie-browser__browser_tabs", "ran magpie-browser browser_tabs"},
		{"magpie-browser__browser", "ran magpie-browser browser"},
		// listed nothing just now, and its name in the hub's characters
		{"my-fs__read", "ran my.fs read"},
	} {
		m := readMCP(t, hubPost(h, "claude", here, toolsCall(c.name, `{"action":"tabs"}`)))
		if m.Error != nil || m.Result.IsError || m.Result.Content[0].Text != c.ran {
			t.Errorf("%s: %+v", c.name, m)
		}
	}
	for _, c := range []struct{ agent, name string }{{"claude", "github__create_issue"}, {"claude", "magpie-browser__"}, {"codex", "magpie-browser__browser_tabs"}, {"claude", "nope__x"}} {
		m := readMCP(t, hubPost(h, c.agent, here, toolsCall(c.name, `{}`)))
		if m.Error == nil || m.Error.Code != -32602 || !strings.Contains(m.Error.Message, "unknown tool "+c.name) {
			t.Errorf("%s's %s: %+v", c.agent, c.name, m)
		}
	}
	want := `claude magpie-browser browser_tabs {"action":"tabs"}|claude magpie-browser browser {"action":"tabs"}|claude my.fs read {"action":"tabs"}`
	if got := strings.Join(f.ran(), "|"); got != want {
		t.Errorf("ran %q, want %q", got, want)
	}
}

// HubMCPAll, the one address any other app is given, is the hub for agent
// "": every server in the Library, with its own instructions and its own
// word for a tool it doesn't have; an agent's path still lists only its own.
func TestHubMCPForAnyApp(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	f := &fakeHub{on: true, tools: map[string][]HubTool{
		"":       {{Server: "github", Tool: "create_issue", Spec: hubSpec("create_issue")}, {Server: "notes", Tool: "find", Spec: hubSpec("find")}},
		"claude": {{Server: "github", Tool: "create_issue", Spec: hubSpec("create_issue")}},
	}}
	withHub(t, f)
	h := lanGuard(New().Handler())

	w := hubPostAt(h, HubMCPAll, here, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if init := readMCP(t, w); init.Error != nil || init.Result.ServerInfo.Name != "magpie" {
		t.Fatalf("initialize: %+v", init)
	}
	if !strings.Contains(w.Body.String(), "The tools of every MCP server in magpie's Library") {
		t.Errorf("another app's instructions: %s", w.Body)
	}
	var names []string
	for _, x := range readMCP(t, hubPostAt(h, HubMCPAll, here, toolsList)).Result.Tools {
		names = append(names, x.Name)
	}
	if strings.Join(names, " ") != "github__create_issue notes__find" {
		t.Fatalf("another app's tools %v", names)
	}
	m := readMCP(t, hubPostAt(h, HubMCPAll, here, toolsCall("notes__find", `{"q":"x"}`)))
	if m.Result.IsError || m.Result.Content[0].Text != "ran notes find" {
		t.Fatalf("another app's call: %+v", m)
	}
	m = readMCP(t, hubPostAt(h, HubMCPAll, here, toolsCall("nope__x", `{}`)))
	if m.Error == nil || m.Error.Code != -32602 || m.Error.Message != "unknown tool nope__x: no MCP server in magpie's Library gives it" {
		t.Errorf("another app's unknown tool: %+v", m)
	}
	// claude's own path doesn't reach a server it isn't given
	m = readMCP(t, hubPost(h, "claude", here, toolsCall("notes__find", `{}`)))
	if m.Error == nil || !strings.Contains(m.Error.Message, "no MCP server switched on for claude") {
		t.Errorf("claude reached notes: %+v", m)
	}
	if got := f.ran(); strings.Join(got, "|") != ` notes find {"q":"x"}` {
		t.Errorf("ran %q", got)
	}
	// guarded as the agents' paths are: no web page, and no other computer
	// while magpie isn't shared
	if w := hubPostAt(h, HubMCPAll, here, toolsList, "Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors"); w.Code != http.StatusForbidden {
		t.Errorf("a foreign page's tools/list: %d %s", w.Code, w.Body)
	}
	if w := hubPostAt(h, HubMCPAll, "192.0.2.7:4000", toolsCall("notes__find", `{}`)); w.Code != http.StatusForbidden {
		t.Errorf("another computer, not shared: %d %s", w.Code, w.Body)
	}
	if got := f.ran(); len(got) != 1 {
		t.Errorf("ran for a request turned away: %q", got)
	}
}

// A server that fails, or doesn't answer in the hub's time, is an error
// result naming it, never a wait past the client's.
func TestHubMCPServerDown(t *testing.T) {
	fresh(t)
	f := &fakeHub{on: true, tools: map[string][]HubTool{"claude": {
		{Server: "db", Tool: "query", Spec: hubSpec("query")},
		{Server: "slow", Tool: "wait", Spec: hubSpec("wait")},
	}}}
	f.call = func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		if server == "db" {
			return nil, errors.New("it exited (5): Error: lost the database")
		}
		<-ctx.Done()
		return nil, errors.New("it didn't answer in time")
	}
	withHub(t, f)
	wait := hubCallWait
	hubCallWait = 200 * time.Millisecond
	t.Cleanup(func() { hubCallWait = wait })
	h := lanGuard(New().Handler())
	m := readMCP(t, hubPost(h, "claude", here, toolsCall("db__query", `{}`)))
	if !m.Result.IsError || m.Result.Content[0].Text != "The MCP server db couldn't run query: it exited (5): Error: lost the database" {
		t.Errorf("a server down: %+v", m)
	}
	start := time.Now()
	m = readMCP(t, hubPost(h, "claude", here, toolsCall("slow__wait", `{}`)))
	if !m.Result.IsError || m.Result.Content[0].Text != "The MCP server slow couldn't run wait: it didn't answer in 200ms" {
		t.Errorf("a server that doesn't answer: %+v", m)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("waited %v", d)
	}
}

// Off, the hub lists no tool and runs none, and says how to turn it on.
func TestHubMCPOff(t *testing.T) {
	fresh(t)
	f := &fakeHub{tools: map[string][]HubTool{"claude": {{Server: "fs", Tool: "read", Spec: hubSpec("read")}}}}
	withHub(t, f)
	h := lanGuard(New().Handler())
	if m := readMCP(t, hubPost(h, "claude", here, toolsList)); len(m.Result.Tools) != 0 || m.Error != nil {
		t.Errorf("off, listed %+v", m)
	}
	m := readMCP(t, hubPost(h, "claude", here, toolsCall("fs__read", `{}`)))
	if !m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "MCP hub is off") {
		t.Errorf("off, a call said %+v", m)
	}
	if len(f.ran()) != 0 {
		t.Errorf("off, ran %q", f.ran())
	}
	MCPHub = nil
	if m := readMCP(t, hubPost(h, "claude", here, toolsList)); len(m.Result.Tools) != 0 || m.Error != nil {
		t.Errorf("no hub, listed %+v", m)
	}
}

// The hub is guarded as magpie's other MCP servers are: another computer
// only while shared and with a key, and no web page — a sandboxed frame's
// null Origin, another site's, one on this computer, one rebound to
// 127.0.0.1 — even from this computer. No tool runs for one turned away.
func TestHubMCPGuarded(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	f := &fakeHub{on: true, tools: map[string][]HubTool{"claude": {{Server: "fs", Tool: "read", Spec: hubSpec("read")}}}}
	withHub(t, f)
	h := lanGuard(New().Handler())
	call := toolsCall("fs__read", `{"q":"x"}`)
	const there = "192.0.2.7:4000"
	if w := hubPost(h, "claude", there, call); w.Code != http.StatusForbidden {
		t.Fatalf("not shared: %d %s", w.Code, w.Body)
	}
	_, secrets := newCaller(t, "Laptop") // shares magpie
	if w := hubPost(h, "claude", there, call); w.Code != http.StatusUnauthorized {
		t.Fatalf("shared, no key: %d %s", w.Code, w.Body)
	}
	if w := hubPost(h, "claude", there, call, "Authorization", "Bearer sk-magpie-wrong"); w.Code != http.StatusUnauthorized {
		t.Fatalf("shared, a wrong key: %d %s", w.Code, w.Body)
	}
	for _, hdr := range [][]string{
		{"Origin", "null", "Sec-Fetch-Mode", "cors"},
		{"Origin", "https://evil.example", "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors"},
		{"Origin", "http://localhost:5173", "Sec-Fetch-Site", "same-site", "Sec-Fetch-Mode", "cors"},
	} {
		if w := hubPost(h, "claude", here, call, hdr...); w.Code != http.StatusForbidden {
			t.Errorf("a page %v: %d %s", hdr, w.Code, w.Body)
		}
	}
	r := httptest.NewRequest(http.MethodPost, HubMCPPath+"claude", strings.NewReader(call))
	r.RemoteAddr, r.Host = here, "rebind.attacker.example:3425"
	r.Header.Set("Origin", "http://rebind.attacker.example:3425")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a rebound page: %d %s", rec.Code, rec.Body)
	}
	if w := httptest.NewRecorder(); true {
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, HubMCPPath+"claude", nil))
		if w.Code == http.StatusOK {
			t.Errorf("a GET: %d", w.Code)
		}
	}
	if got := f.ran(); len(got) != 0 {
		t.Fatalf("ran for a request turned away: %q", got)
	}
	m := readMCP(t, hubPost(h, "claude", there, call, "Authorization", "Bearer "+secrets[0]))
	if m.Result.IsError || m.Result.Content[0].Text != "ran fs read" {
		t.Fatalf("shared, with its key: %+v", m)
	}
	m = readMCP(t, hubPost(h, "claude", here, call))
	if m.Result.IsError || len(f.ran()) != 2 {
		t.Fatalf("this computer: %+v, ran %q", m, f.ran())
	}
}

// A gateway key's limit holds each call: a key held to a limit nothing
// serves is refused, and the server isn't asked.
func TestHubMCPHeldToKeyLimit(t *testing.T) {
	fresh(t)
	f := &fakeHub{on: true, tools: map[string][]HubTool{"claude": {{Server: "fs", Tool: "read", Spec: hubSpec("read")}}}}
	withHub(t, f)
	withoutLimits(t)
	keys, _ := newCaller(t, "Capped")
	setLimit(t, keys[0].ID, &access.Limit{Period: "day", Tokens: 700})
	capped := access.Identity{KeyID: keys[0].ID, KeyName: "Capped", Limit: &access.Limit{Period: "day", Tokens: 700}}
	s := New()
	r := httptest.NewRequest("POST", HubMCPPath+"claude", nil).WithContext(access.WithIdentity(t.Context(), capped))
	res, rpcErr := s.hubMCPCall(r, "claude", mcpMessage{ID: json.RawMessage("1"), Method: "tools/call", Params: json.RawMessage(`{"name":"fs__read","arguments":{}}`)})
	b, _ := json.Marshal(res)
	if rpcErr != nil || !strings.Contains(string(b), `"isError":true`) || !strings.Contains(string(b), "Key limits plugin") {
		t.Errorf("a limited key with limits off: %s %v", b, rpcErr)
	}
	if len(f.ran()) != 0 {
		t.Errorf("ran %q", f.ran())
	}
}

// An agent that shows the router as its instructions' first line (Pi,
// cut at 250 characters) learns from that line which servers are behind
// it and what each is for: a Pi asked about a GitHub repository cloned
// it rather than ask the DeepWiki behind the router, told only "The tools
// of the MCP servers in magpie's Library" (11-01). Every server is named
// and what it is for cut to fit, then said whole below.
func TestHubMCPNamesItsServers(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	five := []HubServer{
		{Name: "context7", About: "Up-to-date documentation and code examples for any library, straight into the prompt."},
		{Name: "deepwiki", About: "Ask questions about any public GitHub repository and read its generated wiki."},
		{Name: "fetch", About: "Fetch a web page and hand it over as Markdown."},
		{Name: "notes"},
		{Name: "playwright", About: "Drive a real browser: open pages, click, type and read them through the accessibility tree."},
		{Name: "time", About: "The current time anywhere, and conversions between time zones."},
	}
	var many []HubServer
	for i := range 40 {
		many = append(many, HubServer{Name: fmt.Sprintf("server-%02d", i), About: "Does one thing well."})
	}
	f := &fakeHub{on: true, servers: map[string][]HubServer{"claude": five}}
	withHub(t, f)
	h := lanGuard(New().Handler())

	say, _, _ := whereReply(t, h, here)
	first, rest, _ := strings.Cut(say, "\n")
	if n := len([]rune(first)); n > 250 {
		t.Errorf("the first line is %d characters: %q", n, first)
	}
	for _, s := range five {
		if !strings.Contains(first, s.Name) {
			t.Errorf("the first line doesn't name %s: %q", s.Name, first)
		}
		if s.About != "" && !strings.Contains(rest, "- "+s.Name+": "+s.About+"\n") {
			t.Errorf("%s isn't said whole: %q", s.Name, rest)
		}
	}
	for _, w := range []string{"documentation", "GitHub repository", "web page", "real browser", "current time"} {
		if !strings.Contains(first, w) {
			t.Errorf("the first line doesn't say %q: %q", w, first)
		}
	}
	if !strings.Contains(rest, "each named <server>__<tool>") {
		t.Errorf("how its tools are named is gone: %q", say)
	}

	// too many to say each: as many names as fit, the rest counted
	f.mu.Lock()
	f.servers["claude"] = many
	f.mu.Unlock()
	say, _, _ = whereReply(t, h, here)
	first, _, _ = strings.Cut(say, "\n")
	if n := len([]rune(first)); n > 250 || !strings.Contains(first, "server-00") || !strings.Contains(first, " more") {
		t.Errorf("40 servers: %d characters, %q", n, first)
	}

	// none: as before
	f.mu.Lock()
	f.servers["claude"] = nil
	f.mu.Unlock()
	if say, _, _ = whereReply(t, h, here); !strings.HasPrefix(say, "The tools of the MCP servers") {
		t.Errorf("no server: %q", say)
	}
}

// whereReply is what an agent is told of the router: initialize's
// instructions and title, and each tool's description.
func whereReply(t *testing.T, h http.Handler, addr string, hdr ...string) (instructions, title string, descs []string) {
	t.Helper()
	var init struct {
		Result struct {
			Instructions string
			ServerInfo   struct{ Title string }
		}
	}
	if err := json.Unmarshal(hubPost(h, "claude", addr, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, hdr...).Body.Bytes(), &init); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Result struct {
			Tools []struct{ Description string }
		}
	}
	if err := json.Unmarshal(hubPost(h, "claude", addr, toolsList, hdr...).Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, x := range list.Result.Tools {
		descs = append(descs, x.Description)
	}
	return init.Result.Instructions, init.Result.ServerInfo.Title, descs
}

// Two routers serving the same tools (packinggoodsllc_07668 on Discord:
// one magpie in China, one in Los Angeles) are told apart by what each
// says of itself: the user's words (RouterNote) in initialize's
// instructions and title and before each tool's description, to every
// caller; with none, this computer's name to an agent on another
// computer, and nothing to one on this computer.
func TestHubMCPSaysWhere(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	old := hostName
	hostName = func() string { return "la-box" }
	t.Cleanup(func() { hostName = old })
	f := &fakeHub{on: true, tools: map[string][]HubTool{"claude": {
		{Server: "fetch", Tool: "fetch", Spec: hubSpec("fetch")},
		{Server: "fs", Tool: "read", Spec: map[string]json.RawMessage{"name": json.RawMessage(`"read"`)}},
	}}}
	withHub(t, f)
	h := lanGuard(New().Handler())
	const there = "192.0.2.7:4000"
	_, secrets := newCaller(t, "Laptop")
	auth := []string{"Authorization", "Bearer " + secrets[0]}

	// nothing set, this computer: as before
	say, title, descs := whereReply(t, h, here)
	if strings.Contains(say, "la-box") || title != "Magpie MCP hub" || strings.Join(descs, "|") != "Does fetch.|" {
		t.Fatalf("this computer, nothing set: %q %q %q", say, title, descs)
	}
	// nothing set, another computer: this computer's name
	say, title, descs = whereReply(t, h, there, auth...)
	if !strings.Contains(say, "This router: la-box.") || title != "Magpie MCP hub · la-box" || strings.Join(descs, "|") != "[la-box] Does fetch.|[la-box]" {
		t.Fatalf("another computer, nothing set: %q %q %q", say, title, descs)
	}
	// the user's words, to every caller, one line and once
	if err := settings.SetRouterNote("  Los Angeles\n (US)  "); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		addr string
		hdr  []string
	}{{here, nil}, {there, auth}} {
		say, title, descs = whereReply(t, h, c.addr, c.hdr...)
		if !strings.Contains(say, "This router: Los Angeles (US).") || title != "Magpie MCP hub · Los Angeles (US)" || strings.Join(descs, "|") != "[Los Angeles (US)] Does fetch.|[Los Angeles (US)]" {
			t.Fatalf("%s, set: %q %q %q", c.addr, say, title, descs)
		}
	}
	// a server whose descriptions say it already (another magpie's router
	// in this one's Library) isn't said twice
	f.mu.Lock()
	f.tools["claude"][0].Spec["description"] = json.RawMessage(`"[Los Angeles (US)] Does fetch."`)
	f.mu.Unlock()
	if _, _, descs = whereReply(t, h, here); descs[0] != "[Los Angeles (US)] Does fetch." {
		t.Fatalf("said twice: %q", descs)
	}
	// off, it says only that it's off
	f.mu.Lock()
	f.on = false
	f.mu.Unlock()
	if say, _, _ = whereReply(t, h, here); say != errHubOff {
		t.Fatalf("off: %q", say)
	}
}

// Each client that initializes is given a session id, and each call the
// hub runs is made with its caller's session (HubCaller.Session), so a
// server that keeps something per caller (Magpie Browser's tab) keeps it
// apart for two agents behind one router: the session another magpie's
// router names in _meta, which is its caller's, else the client's own; a
// client that sends none is told apart from no one, as before.
func TestHubMCPTellsCallersApart(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var got []string
	f := &fakeHub{on: true, tools: map[string][]HubTool{
		"claude": {{Server: "magpie-browser", Tool: "browser_tabs", Spec: hubSpec("browser_tabs")}},
	}, call: func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		mu.Lock()
		got = append(got, HubCallerOf(ctx).Session)
		mu.Unlock()
		return json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`), nil
	}}
	withHub(t, f)
	h := lanGuard(New().Handler())

	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
	a := hubPost(h, "claude", here, initialize).Header().Get("Mcp-Session-Id")
	b := hubPost(h, "claude", here, initialize).Header().Get("Mcp-Session-Id")
	if a == "" || b == "" || a == b {
		t.Fatalf("sessions given at initialize: %q, %q", a, b)
	}
	if again := hubPost(h, "claude", here, initialize, "Mcp-Session-Id", a).Header().Get("Mcp-Session-Id"); again != "" {
		t.Errorf("a client with a session was given another: %q", again)
	}
	call := toolsCall("magpie-browser__browser_tabs", `{}`)
	hubPost(h, "claude", here, call, "Mcp-Session-Id", a)
	hubPost(h, "claude", here, call, "Mcp-Session-Id", b)
	hubPost(h, "claude", here, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"magpie-browser__browser_tabs","arguments":{},"_meta":{"magpie/session":"hub-claude-far"}}}`, "Mcp-Session-Id", a)
	hubPost(h, "claude", here, call)
	mu.Lock()
	defer mu.Unlock()
	want := []string{"hub-claude-" + a, "hub-claude-" + b, "hub-claude-far", ""}
	if !slices.Equal(got, want) {
		t.Fatalf("callers %q, want %q", got, want)
	}
}

// The router's one address is what a headless magpie gives remote MCP
// clients (treeman_26897 on Discord: a server with no agents, the router
// turned on for other computers' apps). From another computer it takes a
// gateway key, as /v1 does, and nothing without one gets a tool run:
// not while magpie isn't shared, not with no Origin, a null one, another
// scheme's or a foreign site's, not under a rebound Host, and not through
// a proxy on this computer.
func TestHubMCPRemoteEntryNeedsKey(t *testing.T) {
	fresh(t)
	t.Setenv("MAGPIE_ADDR", "")
	t.Setenv("MAGPIE_PUBLIC_URL", "")
	t.Setenv("MAGPIE_TRUST_PROXY", "")
	f := &fakeHub{on: true, tools: map[string][]HubTool{"": {{Server: "notes", Tool: "find", Spec: hubSpec("find")}}}}
	withHub(t, f)
	h := lanGuard(New().Handler())
	call := toolsCall("notes__find", `{"q":"x"}`)
	const there = "192.0.2.7:4000"
	refused := func(what string, w *httptest.ResponseRecorder, want ...int) {
		t.Helper()
		for _, c := range want {
			if w.Code == c {
				return
			}
		}
		t.Errorf("%s: %d %s, want %v", what, w.Code, w.Body, want)
	}
	refused("not shared", hubPostAt(h, HubMCPAll, there, call), http.StatusForbidden)
	refused("not shared, a tools/list", hubPostAt(h, HubMCPAll, there, toolsList), http.StatusForbidden)
	_, secrets := newCaller(t, "Remote client") // shares magpie
	refused("no key, no Origin", hubPostAt(h, HubMCPAll, there, call), http.StatusUnauthorized)
	refused("no key, a tools/list", hubPostAt(h, HubMCPAll, there, toolsList), http.StatusUnauthorized)
	refused("a wrong key", hubPostAt(h, HubMCPAll, there, call, "Authorization", "Bearer sk-magpie-wrong"), http.StatusUnauthorized)
	for _, origin := range []string{"null", "chrome-extension://abcdefghijklmnop", "file://", "moz-extension://1234", "https://evil.example", "http://192.0.2.7:3425"} {
		hdr := []string{"Origin", origin, "Sec-Fetch-Site", "cross-site", "Sec-Fetch-Mode", "cors"}
		refused("no key, Origin "+origin+" from there", hubPostAt(h, HubMCPAll, there, call, hdr...), http.StatusUnauthorized, http.StatusForbidden)
		refused("Origin "+origin+" from here", hubPostAt(h, HubMCPAll, here, call, hdr...), http.StatusForbidden)
	}
	// a page whose own name was rebound to this computer: same-origin, so
	// its Origin is the Host or (as a browser's own marks show) none at all.
	// With none of a browser's marks it is an agent here calling by a name
	// of its own, which lanGuard lets by, as /v1
	for _, addr := range []string{here, there} {
		for _, marks := range [][]string{
			{"Origin", "http://rebind.attacker.example:3425", "Sec-Fetch-Site", "same-origin", "Sec-Fetch-Mode", "cors"},
			{"Sec-Fetch-Site", "same-origin", "Sec-Fetch-Mode", "cors"},
			{"User-Agent", "Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 Safari/605.1.15"},
		} {
			r := httptest.NewRequest(http.MethodPost, HubMCPAll, strings.NewReader(call))
			r.RemoteAddr, r.Host = addr, "rebind.attacker.example:3425"
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Accept", "application/json, text/event-stream")
			for i := 0; i+1 < len(marks); i += 2 {
				r.Header.Set(marks[i], marks[i+1])
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			refused(fmt.Sprintf("a rebound Host from %s %v", addr, marks), rec, http.StatusUnauthorized, http.StatusForbidden)
		}
	}
	// a proxy or tunnel on this computer passing another's request on
	refused("through a proxy here, no key", hubPostAt(h, HubMCPAll, here, call, "X-Forwarded-For", "203.0.113.9"), http.StatusUnauthorized, http.StatusForbidden)
	refused("through a tunnel here, no key", hubPostAt(h, HubMCPAll, here, call, "Cf-Connecting-IP", "203.0.113.9"), http.StatusUnauthorized, http.StatusForbidden)
	if got := f.ran(); len(got) != 0 {
		t.Fatalf("ran for a request turned away: %q", got)
	}
	// with its key: the tool runs, from there and through the proxy
	m := readMCP(t, hubPostAt(h, HubMCPAll, there, call, "Authorization", "Bearer "+secrets[0]))
	if m.Result.IsError || m.Result.Content[0].Text != "ran notes find" {
		t.Fatalf("with its key: %+v", m)
	}
	m = readMCP(t, hubPostAt(h, HubMCPAll, here, call, "X-Forwarded-For", "203.0.113.9", "Authorization", "Bearer "+secrets[0]))
	if m.Result.IsError || len(f.ran()) != 2 {
		t.Fatalf("through a proxy, with its key: %+v, ran %q", m, f.ran())
	}
}

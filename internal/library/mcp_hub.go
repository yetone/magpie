package library

// The MCP hub (#2679): with the Library's switch on (MCPHub), an agent
// that can reach a streamable HTTP server is given one, magpie's own, at
// gateway.HubMCPPath<agent>, in place of the library's servers ticked for
// it; the gateway lists their tools as one server's and hands each call on
// to the server it is of (gateway/hub_mcp.go). The servers are run here:
//
//   - a command is started once, for every agent, as the check starts it
//     (startServer: in home, with PWD, magpie's environment with the
//     login shell's PATH, the server's own variables with ${NAME}
//     expanded), the first time a tool of it is listed or called; it is
//     ended once no call has used it for hubIdle, and started again when
//     next used, or when it ended by itself. Its stdin is magpie's: a
//     magpie that dies without ending it closes it, which ends an MCP
//     server, and on Windows its job ends with magpie.
//   - a URL is reached as the check reaches it, with magpie's sign-in
//     (httpSession), or over its event stream (SSE).
//
// magpie's own image server saves into the calling agent's project, which
// a server started once for every agent can't ask its client for: the
// gateway asks the agent for its roots on each call (gateway.HubCaller)
// and the call carries them to it, in _meta (imageMeta). An agent in WSL
// is given it directly still, run in the distro, whose paths it takes. A
// server the user keeps direct (Server.Hub, #4382) is each agent's own
// too: a command that reads its project from the folder it starts in, or
// keeps a session's state. What a server lists is kept until it says its
// tools changed, or the server is edited.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yetone/magpie/internal/agent"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/mcpauth"
)

func init() { gateway.MCPHub = theHub{} }

// HubEntry is the name of the one entry the hub is given to an agent by.
const HubEntry = "magpie"

// hubIdle is how long a server the hub started is kept with no call.
var hubIdle = 10 * time.Minute

// SetMCPHub turns the hub on or off: on, each agent that can reach it is
// given magpie's one entry in place of its servers; off, their own entries
// again, and the servers the hub started are ended.
func SetMCPHub(on bool) (*Result, error) {
	res, err := change(func(l *Library) error {
		l.MCPHub = on
		return nil
	})
	if err == nil && !on {
		hubStopAll()
	}
	return res, err
}

// HubDirect is Server.Hub for a server the hub leaves out: each agent
// given it keeps its own entry, as with the hub off (#4382).
const HubDirect = "direct"

// inHub is whether the hub gives s to agent id: every server but one the
// user keeps direct (Server.Hub), and magpie's own image server to an
// agent in WSL, which is given it run in its distro (mcpFile.side) for the
// paths it saves to be the agent's.
func inHub(s *Server, id string) bool {
	return s.Hub != HubDirect && (!isSelf(s) || !strings.Contains(id, "@wsl:"))
}

// ServerHub shares a server through the hub, or keeps it each agent's own
// entry (direct); the agents are written again at once, so the entry it
// had the other way goes.
func ServerHub(name string, direct bool) (*Result, error) {
	return change(func(l *Library) error {
		s := l.server(name)
		if s == nil {
			return fmt.Errorf("no server called %s", name)
		}
		s.Hub = ""
		if direct {
			s.Hub = HubDirect
		}
		return nil
	})
}

// hubServerFor is the entry agent t is given for the hub, at the address
// the agent reaches the gateway at, with a key when it reaches it from
// another machine (a WSL distro under NAT, a computer on the LAN).
func (l *Library) hubServerFor(t *Target) *Server {
	base := strings.TrimRight(gatewayOf(t), "/")
	s := &Server{Name: HubEntry, Transport: "http", URL: base + gateway.HubMCPPath + url.PathEscape(t.Agent.ID)}
	if k := agent.KeyAt(base); k != gateway.Token {
		s.Headers = map[string]string{"Authorization": "Bearer " + k}
	}
	// Pi connects to a server it doesn't wait for (the hub's, reached by
	// codemode) while the first prompt is already written, so only a
	// description gives that prompt its line of what the hub serves
	// (extensions/mcp, serverSummary): told the router's instructions
	// alone, a Pi asked about a repository never saw DeepWiki (11-01)
	if t.MCP != nil && t.MCP.Format == fmtPiNative {
		s.about = gateway.HubLine(hubAbouts(l.hubServers(t.Agent.ID)))
	}
	return s
}

// HubLink is the link that adds the hub to app, at this computer's
// gateway (HubMCPAll, which needs no key here), through the install link
// its vendor documents: Cursor's cursor://anysphere.cursor-deeplink/mcp/install
// (docs.cursor.com, "MCP install links") and VS Code's vscode:mcp/install
// (code.visualstudio.com, "Add an MCP server": URL handler). Each app asks
// its user before it adds the server.
func HubLink(app string) (string, error) {
	u := strings.TrimRight(gateway.URL(), "/") + gateway.HubMCPAll
	switch app {
	case "cursor":
		cfg, _ := json.Marshal(map[string]string{"url": u})
		return "cursor://anysphere.cursor-deeplink/mcp/install?name=" + HubEntry + "&config=" + base64.StdEncoding.EncodeToString(cfg), nil
	case "vscode":
		cfg, _ := json.Marshal(map[string]string{"name": HubEntry, "type": "http", "url": u})
		return "vscode:mcp/install?" + url.QueryEscape(string(cfg)), nil
	}
	return "", fmt.Errorf("magpie has no install link for %q", app)
}

// Why an agent with the hub on keeps each server's own entry (AgentView's
// HubWhy).
const (
	hubNoRemote = "no-remote" // it reaches no server over HTTP (Claude Desktop)
	hubTaken    = "taken"     // a library server is called magpie
	hubOwn      = "own"       // the agent has an entry called magpie of its own
)

// hubFor is the hub's entry for t, or nil and why it isn't given one: the
// hub is off, the agent has none of the hub's servers, or it can't take
// the entry (why).
func (l *Library) hubFor(t *Target, a *Applied, entries map[string]map[string]any) (*Server, string) {
	if !l.MCPHub || t.MCP == nil {
		return nil, ""
	}
	id := t.Agent.ID
	if !slices.ContainsFunc(l.MCP, func(s *Server) bool { return slices.Contains(s.Agents, id) && inHub(s, id) }) {
		return nil, ""
	}
	hs := l.hubServerFor(t)
	if err := t.MCP.supports(hs); err != nil {
		return nil, hubNoRemote
	}
	if l.server(HubEntry) != nil {
		return nil, hubTaken
	}
	if _, ok := entries[HubEntry]; ok && !slices.Contains(a.MCP, HubEntry) {
		return nil, hubOwn
	}
	if err := t.MCP.foreign(entries[HubEntry]); err != nil {
		return nil, hubOwn
	}
	return hs, ""
}

// ---- what the gateway asks -------------------------------------------------

type theHub struct{}

func (theHub) On() bool {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	return err == nil && l.MCPHub
}

// hubServers are the servers the hub gives agent, by name: for "" (the
// address any other app is given), every server in the library.
func hubServers(agentID string) []*Server {
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil || !l.MCPHub {
		return nil
	}
	return l.hubServers(agentID)
}

// hubServers are the servers the hub gives agentID from l, by name.
func (l *Library) hubServers(agentID string) []*Server {
	var out []*Server
	for _, s := range l.MCP {
		if (agentID == "" || slices.Contains(s.Agents, agentID)) && inHub(s, agentID) {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (theHub) Servers(agentID string) []gateway.HubServer { return hubAbouts(hubServers(agentID)) }

// hubAbouts are ss as an agent is told of them.
func hubAbouts(ss []*Server) []gateway.HubServer {
	out := make([]gateway.HubServer, len(ss))
	for i, s := range ss {
		out[i] = hubServerOf(s)
	}
	return out
}

// hereOnly is whether s acts as this computer's user beyond what its
// tools' arguments say (gateway.HubServer's HereOnly): Magpie Browser and
// another magpie's router (keepsSession), and magpie's image server.
func hereOnly(s *Server) bool { return keepsSession(s) || isSelf(s) }

// visitorServers are ss without those the hub's caller of ctx isn't
// given (gateway.HubVisitor): the gateway leaves them out too, and this
// holds when it doesn't.
func visitorServers(ctx context.Context, ss []*Server) []*Server {
	v := gateway.HubVisitorOf(ctx)
	return slices.DeleteFunc(ss, func(s *Server) bool { return !v.Gives(hubServerOf(s)) })
}

func (theHub) Tools(ctx context.Context, agentID string) []gateway.HubTool {
	ss := visitorServers(ctx, hubServers(agentID))
	if gateway.FromRouter(ctx) {
		// another magpie's router: not given a Remote magpie's back
		ss = slices.DeleteFunc(ss, func(s *Server) bool { return s.Bridge != "" })
	}
	lists := make([][]gateway.HubTool, len(ss))
	var wg sync.WaitGroup
	for i, s := range ss {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var ts []map[string]json.RawMessage
			var says serverSays
			var err error
			if s.Plugin != "" {
				ts, err = pluginHubTools(ctx, s)
			} else {
				b := backendOf(s)
				ts, err = b.tools(ctx)
				says = b.said()
			}
			if err != nil {
				return
			}
			noteCard(s, says, ts)
			for _, t := range ts {
				var name string
				if json.Unmarshal(t["name"], &name) != nil || name == "" || slices.Contains(s.DisabledTools, name) {
					continue
				}
				lists[i] = append(lists[i], gateway.HubTool{Server: s.Name, Tool: name, Spec: t, Roots: isSelf(s)})
			}
		}()
	}
	wg.Wait()
	var out []gateway.HubTool
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

func (theHub) Call(ctx context.Context, agentID, server, tool string, args json.RawMessage) (json.RawMessage, error) {
	ss := hubServers(agentID)
	i := slices.IndexFunc(ss, func(s *Server) bool { return s.Name == server })
	if i >= 0 && !gateway.HubVisitorOf(ctx).Gives(hubServerOf(ss[i])) {
		return nil, errors.New("it acts as this computer's user, so magpie runs it for this computer's agents only")
	}
	if i < 0 && agentID == "" {
		return nil, errors.New("it isn't in magpie's Library")
	}
	if i < 0 {
		return nil, fmt.Errorf("it isn't switched on for %s in magpie's Library", agentID)
	}
	s := ss[i]
	if s.Bridge != "" && gateway.FromRouter(ctx) {
		return nil, errors.New("it is another magpie's router, which isn't run for a magpie's router")
	}
	if slices.Contains(s.DisabledTools, tool) {
		return nil, errors.New("the tool is turned off in magpie's Library")
	}
	var meta map[string]any
	if isSelf(s) {
		meta = imageMeta(gateway.HubCallerOf(ctx))
	}
	if c := gateway.HubCallerOf(ctx); c.Session != "" && keepsSession(s) {
		if meta == nil {
			meta = map[string]any{}
		}
		meta[gateway.HubSessionMeta] = c.Session
	}
	if s.Plugin != "" {
		return pluginHubCall(ctx, agentID, s, tool, args)
	}
	return backendOf(s).call(ctx, tool, args, meta)
}

// keepsSession is whether s is told which of the hub's callers a call is
// from: Magpie Browser, which works in a tab of each caller's own (one
// process serves every agent behind the hub, so its own session would be
// all of theirs), and another magpie's router, which passes it on to its
// Magpie Browser. No other server is sent it.
func keepsSession(s *Server) bool {
	if s.Remote() {
		u, err := url.Parse(s.URL)
		return err == nil && strings.HasPrefix(u.Path, gateway.HubMCPAll)
	}
	return len(s.Args) >= 2 && s.Args[0] == "mcp" && s.Args[1] == "browser"
}

// imageMeta is the _meta a call of magpie's image server is made with:
// the calling agent's roots, which it saves into, and its name, which its
// requests go under.
func imageMeta(c gateway.HubCaller) map[string]any {
	m := map[string]any{gateway.HubRootsMeta: append([]string{}, c.Roots...)}
	if c.Client != "" {
		m[gateway.HubClientMeta] = c.Client
	}
	return m
}

func (theHub) Stop() { hubStopAll() }

func (theHub) Prune() { hubPrune() }

// hubPrune ends each server the hub holds that the library no longer
// gives as it was started: the hub off, the server gone or edited. One
// ticked for no agent is still given to other apps (HubMCPAll), and ends
// when idle. A library that can't be read ends nothing.
func hubPrune() {
	hubPool.Lock()
	held := len(hubPool.m)
	hubPool.Unlock()
	if held == 0 {
		return
	}
	mu.Lock()
	l, err := load()
	mu.Unlock()
	if err != nil {
		return
	}
	given := map[string]string{}
	if l.MCPHub {
		for _, s := range l.MCP {
			if inHub(s, "") {
				given[s.Name] = hubKey(s)
			}
		}
	}
	var gone []*backend
	hubPool.Lock()
	for name, b := range hubPool.m {
		if k, ok := given[name]; !ok || k != b.key {
			gone = append(gone, b)
			delete(hubPool.m, name)
		}
	}
	hubPool.Unlock()
	for _, b := range gone {
		b.stop()
	}
}

// ---- the servers it runs -----------------------------------------------------

// hubPool is each server the hub uses, by name.
var hubPool = struct {
	sync.Mutex
	m map[string]*backend
}{m: map[string]*backend{}}

// hubKey is the server as it is run: an edit, or a sign-in since, is
// another to start.
func hubKey(s *Server) string {
	c := *s
	c.Agents, c.DisabledTools = nil, nil
	b, _ := json.Marshal(c)
	return fmt.Sprintf("%s|%v", b, s.Transport == "http" && mcpauth.SignedIn(s.Name, s.URL))
}

func backendOf(s *Server) *backend {
	key := hubKey(s)
	hubPool.Lock()
	defer hubPool.Unlock()
	b := hubPool.m[s.Name]
	if b != nil && b.key != key {
		go b.stop()
		b = nil
	}
	if b == nil {
		c := *s
		b = &backend{s: &c, key: key}
		hubPool.m[s.Name] = b
	}
	return b
}

// hubStopAll ends every server the hub started, and waits for them.
func hubStopAll() {
	hubPool.Lock()
	all := hubPool.m
	hubPool.m = map[string]*backend{}
	hubPool.Unlock()
	var wg sync.WaitGroup
	for _, b := range all {
		wg.Add(1)
		go func() { defer wg.Done(); b.stop() }()
	}
	wg.Wait()
}

// hubConn is a connection to a server the hub holds.
type hubConn interface {
	ask(ctx context.Context, method string, params any) (*rpcMsg, error)
	alive() bool
	close()
	// said is what the server said of itself in initialize's result
	said() serverSays
}

// backend is one server the hub uses: its connection while it has one,
// and the tools it last listed.
type backend struct {
	s   *Server
	key string

	mu      sync.Mutex
	conn    hubConn
	ready   chan struct{} // while it is being connected to
	err     error         // why the last connection failed
	list    []map[string]json.RawMessage
	listed  bool
	says    serverSays // what the server said of itself when it was connected to
	busy    int
	idle    *time.Timer
	stopped bool
}

var errHubStopped = errors.New("magpie stopped it")

// connect is the connection to the server, made when there is none: a
// command started, a URL initialized. A start goes on when ctx ends, for
// the next to use.
func (b *backend) connect(ctx context.Context) (hubConn, error) {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return nil, errHubStopped
	}
	if b.conn != nil && !b.conn.alive() {
		go b.conn.close()
		b.conn = nil
	}
	if b.conn != nil {
		c := b.conn
		b.mu.Unlock()
		return c, nil
	}
	if b.ready == nil {
		b.ready = make(chan struct{})
		go b.start(b.ready)
	}
	ready := b.ready
	b.mu.Unlock()
	select {
	case <-ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn != nil {
		return b.conn, nil
	}
	if b.err == nil {
		return nil, errHubStopped
	}
	return nil, b.err
}

func (b *backend) start(ready chan struct{}) {
	each := remoteStep
	if b.s.Transport == "stdio" {
		each = stdioStep
	}
	ctx, cancel := context.WithTimeout(context.Background(), each)
	defer cancel()
	c, err := dialHub(ctx, b.s, b.relist)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped && c != nil {
		go c.close()
		c = nil
	}
	b.conn, b.err, b.ready = c, err, nil
	if c != nil {
		b.says = c.said()
	}
	close(ready)
}

// said is what the server said of itself when it was last connected to.
func (b *backend) said() serverSays {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.says
}

// relist forgets the tools the server listed: it said they changed.
func (b *backend) relist() {
	b.mu.Lock()
	b.listed = false
	b.mu.Unlock()
}

// use and done count the calls using the server; it is ended hubIdle after
// the last.
func (b *backend) use() {
	b.mu.Lock()
	b.busy++
	if b.idle != nil {
		b.idle.Stop()
		b.idle = nil
	}
	b.mu.Unlock()
}

func (b *backend) done() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.busy--
	if b.busy > 0 || b.stopped {
		return
	}
	var t *time.Timer
	t = time.AfterFunc(hubIdle, func() {
		b.mu.Lock()
		if b.idle != t || b.busy > 0 || b.conn == nil {
			b.mu.Unlock()
			return
		}
		c := b.conn
		b.conn, b.idle = nil, nil
		b.mu.Unlock()
		c.close()
	})
	b.idle = t
}

// stop ends the server for good: it was edited, the hub turned off, or
// magpie is stopping.
func (b *backend) stop() {
	b.mu.Lock()
	b.stopped = true
	if b.idle != nil {
		b.idle.Stop()
		b.idle = nil
	}
	c := b.conn
	b.conn = nil
	b.mu.Unlock()
	if c != nil {
		c.close()
	}
}

// tools are the tools the server lists, every page of them.
func (b *backend) tools(ctx context.Context) ([]map[string]json.RawMessage, error) {
	b.use()
	defer b.done()
	b.mu.Lock()
	if b.listed {
		ts := b.list
		b.mu.Unlock()
		return ts, nil
	}
	b.mu.Unlock()
	c, err := b.connect(ctx)
	if err != nil {
		return nil, err
	}
	var all []map[string]json.RawMessage
	cursor := ""
	for page := 0; page < maxToolPages; page++ {
		var params any
		if cursor != "" {
			params = map[string]any{"cursor": cursor}
		}
		m, err := c.ask(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		if m.Error != nil {
			if m.Error.Code == -32601 || page > 0 {
				break
			}
			return nil, errors.New("tools/list: " + m.Error.Message)
		}
		var r struct {
			Tools      []map[string]json.RawMessage `json:"tools"`
			NextCursor string                       `json:"nextCursor"`
		}
		if err := json.Unmarshal(m.Result, &r); err != nil {
			return nil, errors.New("tools/list: " + err.Error())
		}
		all = append(all, r.Tools...)
		if r.NextCursor == "" || r.NextCursor == cursor {
			break
		}
		cursor = r.NextCursor
	}
	b.mu.Lock()
	b.list, b.listed = all, true
	b.mu.Unlock()
	return all, nil
}

// call runs one tool, with meta as its _meta when there is one, and is its
// result as the server gave it.
func (b *backend) call(ctx context.Context, tool string, args json.RawMessage, meta map[string]any) (json.RawMessage, error) {
	b.use()
	defer b.done()
	c, err := b.connect(ctx)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"name": tool, "arguments": args}
	if meta != nil {
		params["_meta"] = meta
	}
	m, err := c.ask(ctx, "tools/call", params)
	if err != nil {
		return nil, err
	}
	if m.Error != nil {
		return nil, fmt.Errorf("%s (%d)", m.Error.Message, m.Error.Code)
	}
	return m.Result, nil
}

// healthErr is a check's finding as the hub tells an agent.
func healthErr(h ServerHealth) error {
	var s string
	switch {
	case h.State == "auth":
		s = "it asks for a sign-in or a key: sign in to it, or give it its key, in magpie's Library"
	case h.Why == "notfound":
		s = "its command " + h.Detail + " wasn't found"
	case h.Why == "start":
		s = "it couldn't be started: " + h.Detail
	case h.Why == "exited":
		s = "it exited"
		if h.Code != 0 {
			s += fmt.Sprintf(" (%d)", h.Code)
		}
		if h.Detail != "" {
			s += ": " + h.Detail
		}
	case h.Why == "timeout":
		s = "it didn't answer in time"
		if h.Step != "" {
			s = "it didn't answer " + h.Step + " in time"
		}
	case h.Why == "http":
		s = "it answered " + h.Detail
	case h.Why == "refused" || h.Why == "unreachable":
		s = "it couldn't be reached: " + h.Detail
	case h.Why == "novar":
		s = h.Detail + " isn't set where magpie runs"
	default:
		s = h.Detail
	}
	return errors.New(s)
}

// dialHub connects to s, initialized, for the hub; changed is called when
// it says its tools changed.
func dialHub(ctx context.Context, s *Server, changed func()) (hubConn, error) {
	var c *streamConn
	var err error
	switch s.Transport {
	case "stdio":
		c, err = dialStdio(s, changed)
	case "sse":
		c, err = dialSSE(ctx, s, changed)
	default:
		hc, err := dialHTTP(ctx, s)
		if err != nil {
			return nil, err
		}
		return hc, nil
	}
	if err != nil {
		return nil, err
	}
	m, err := c.ask(ctx, "initialize", initParams())
	if err == nil && m.Error != nil {
		err = healthErr(*initError(m))
	}
	if err != nil {
		c.close()
		return nil, err
	}
	if err := c.send(ctx, initializedNote); err != nil {
		c.close()
		return nil, err
	}
	c.says = saysOf(m.Result)
	return c, nil
}

// waiters are the requests sent on a stream (a command's stdout, an SSE
// stream) waiting for their replies, by id.
type waiters struct {
	mu   sync.Mutex
	next int
	wait map[string]chan *rpcMsg
	gone error
}

func (w *waiters) open() (int, chan *rpcMsg, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gone != nil {
		return 0, nil, w.gone
	}
	w.next++
	ch := make(chan *rpcMsg, 1)
	if w.wait == nil {
		w.wait = map[string]chan *rpcMsg{}
	}
	w.wait[fmt.Sprint(w.next)] = ch
	return w.next, ch, nil
}

func (w *waiters) drop(id int) {
	w.mu.Lock()
	delete(w.wait, fmt.Sprint(id))
	w.mu.Unlock()
}

func (w *waiters) deliver(m *rpcMsg) {
	id := string(bytes.TrimSpace(m.ID))
	w.mu.Lock()
	ch := w.wait[id]
	delete(w.wait, id)
	w.mu.Unlock()
	if ch != nil {
		ch <- m
	}
}

// end fails every request waiting, and those after: the stream ended.
func (w *waiters) end(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.gone != nil {
		return
	}
	w.gone = err
	for id, ch := range w.wait {
		close(ch)
		delete(w.wait, id)
	}
}

func (w *waiters) err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.gone
}

// streamConn is a connection whose replies come on a stream: a command's
// stdout or an SSE stream. send writes a message to the server.
type streamConn struct {
	w       waiters
	send    func(ctx context.Context, b []byte) error
	stopped func()
	once    sync.Once
	says    serverSays
}

// read takes one message off the stream: a reply is its request's, a
// request of the server's own is answered (ping; nothing else is offered),
// a note that its tools changed has them listed again.
func (c *streamConn) read(b []byte, changed func()) {
	var m rpcMsg
	if json.Unmarshal(b, &m) != nil {
		// a line that isn't JSON-RPC is a server logging to the wrong
		// stream; agents pass over it too
		return
	}
	id := bytes.TrimSpace(m.ID)
	switch {
	case m.Method != "" && len(id) > 0 && string(id) != "null":
		reply := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id)}
		if m.Method == "ping" {
			reply["result"] = map[string]any{}
		} else {
			reply["error"] = map[string]any{"code": -32601, "message": "magpie's hub doesn't offer " + m.Method}
		}
		out, _ := json.Marshal(reply)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = c.send(ctx, out)
		}()
	case m.Method == "notifications/tools/list_changed":
		changed()
	case m.Method == "":
		c.w.deliver(&m)
	}
}

func (c *streamConn) ask(ctx context.Context, method string, params any) (*rpcMsg, error) {
	id, ch, err := c.w.open()
	if err != nil {
		return nil, err
	}
	defer c.w.drop(id)
	if err := c.send(ctx, request(id, method, params)); err != nil {
		if gone := c.w.err(); gone != nil {
			return nil, gone
		}
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, c.w.err()
		}
		return m, nil
	case <-ctx.Done():
		// the server can stop working on it
		note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": id, "reason": "the agent's call ended"}})
		go func() {
			c2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = c.send(c2, note)
		}()
		return nil, errors.New("it didn't answer in time")
	}
}

func (c *streamConn) alive() bool { return c.w.err() == nil }

func (c *streamConn) said() serverSays { return c.says }

func (c *streamConn) close() {
	c.once.Do(func() {
		c.w.end(errHubStopped)
		c.stopped()
	})
}

// dialStdio starts the command s runs, as the check starts it.
func dialStdio(s *Server, changed func()) (*streamConn, error) {
	// the hub's server lives until the hub ends it, not with a request
	tree, stdin, stdout, errs, stop, bad := startServer(context.Background(), s, "", "")
	if bad != nil {
		return nil, healthErr(*bad)
	}
	var exitErr error
	exited := make(chan struct{})
	go func() { exitErr = tree.Wait(); close(exited) }()
	var wmu sync.Mutex
	c := &streamConn{}
	c.send = func(_ context.Context, b []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		_, err := stdin.Write(append(b, '\n'))
		return err
	}
	c.stopped = func() {
		// asked to end first, as an agent leaving it does: its stdin
		// closed; then its tree, whatever it started
		stdin.Close()
		select {
		case <-exited:
		case <-time.After(time.Second):
		}
		select {
		case <-tree.Stop(2 * time.Second):
		case <-time.After(5 * time.Second):
		}
		stop()
	}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 16<<20)
		for sc.Scan() {
			c.read(sc.Bytes(), changed)
		}
		// its output ended: it is ending, and says why once it has
		select {
		case <-exited:
			c.w.end(healthErr(exitHealth(exitErr, errs)))
		case <-time.After(2 * time.Second):
			c.w.end(errors.New("it closed its output: " + errs.last()))
		}
	}()
	return c, nil
}

// dialSSE opens s's event stream, which names where to post to.
func dialSSE(ctx context.Context, s *Server, changed func()) (*streamConn, error) {
	hdr, bad := remoteHeaders(ctx, s)
	if bad != nil {
		return nil, healthErr(*bad)
	}
	stream, cancel := context.WithCancel(context.Background())
	// the stream outlives the dial, which only bounds its start
	unbind := context.AfterFunc(ctx, cancel)
	req, err := http.NewRequestWithContext(stream, http.MethodGet, s.URL, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "text/event-stream")
	res, err := checkClient.Do(req)
	if err != nil {
		cancel()
		return nil, healthErr(netHealth(ctx, err))
	}
	if res.StatusCode/100 != 2 {
		res.Body.Close()
		cancel()
		if res.StatusCode == 401 || res.StatusCode == 403 {
			return nil, healthErr(refused(res))
		}
		return nil, healthErr(statusHealth(res))
	}
	events := sseEvents(res.Body, stream.Done())
	var endpoint *url.URL
	for endpoint == nil {
		e, ok := <-events
		if !ok {
			res.Body.Close()
			cancel()
			if ctx.Err() != nil {
				return nil, errors.New("it didn't open its event stream in time")
			}
			return nil, errors.New("its event stream ended")
		}
		if e.name == "endpoint" {
			base, _ := url.Parse(s.URL)
			if endpoint, err = base.Parse(strings.TrimSpace(e.data)); err != nil {
				res.Body.Close()
				cancel()
				return nil, errors.New("endpoint: " + err.Error())
			}
		}
	}
	unbind()
	c := &streamConn{}
	c.send = func(ctx context.Context, b []byte) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(b))
		if err != nil {
			return err
		}
		for k, v := range hdr {
			req.Header[k] = v
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := checkClient.Do(req)
		if err != nil {
			return healthErr(netHealth(ctx, err))
		}
		io.Copy(io.Discard, io.LimitReader(res.Body, 1<<16))
		res.Body.Close()
		if res.StatusCode/100 != 2 {
			if res.StatusCode == 401 || res.StatusCode == 403 {
				return healthErr(refused(res))
			}
			return healthErr(statusHealth(res))
		}
		return nil
	}
	c.stopped = func() { cancel(); res.Body.Close() }
	go func() {
		for e := range events {
			if e.name == "" || e.name == "message" {
				c.read([]byte(e.data), changed)
			}
		}
		c.w.end(errors.New("its event stream ended"))
	}()
	return c, nil
}

// httpConn is a streamable HTTP server's session, held by the hub.
type httpConn struct {
	c   *httpSession
	ids atomic.Int64
	// reopen is held while a session the server forgot is opened again
	reopen sync.Mutex
}

func dialHTTP(ctx context.Context, s *Server) (*httpConn, error) {
	c, bad := newHTTPSession(ctx, s)
	if bad != nil {
		return nil, healthErr(*bad)
	}
	if h := c.initialize(ctx, initializeReq()); h != nil {
		return nil, healthErr(*h)
	}
	hc := &httpConn{c: c}
	hc.ids.Store(1)
	return hc, nil
}

func (h *httpConn) said() serverSays {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	return saysOf(h.c.init)
}

func (h *httpConn) ask(ctx context.Context, method string, params any) (*rpcMsg, error) {
	for try := 0; ; try++ {
		id := int(h.ids.Add(1))
		h.c.mu.Lock()
		session := h.c.session
		h.c.mu.Unlock()
		m, bad := h.c.call(ctx, id, request(id, method, params))
		if bad == nil {
			// a token renewed and taken is one to renew again next time
			// it runs out
			h.c.mu.Lock()
			h.c.renewed = false
			h.c.mu.Unlock()
			return m, nil
		}
		// a session the server no longer knows (it restarted) is opened
		// again, once
		if bad.Code == http.StatusNotFound && session != "" && try == 0 {
			h.reopen.Lock()
			h.c.mu.Lock()
			same := h.c.session == session
			if same {
				h.c.session = ""
			}
			h.c.mu.Unlock()
			var again *ServerHealth
			if same {
				again = h.c.initialize(ctx, initializeReq())
			}
			h.reopen.Unlock()
			if again != nil {
				return nil, healthErr(*again)
			}
			continue
		}
		return nil, healthErr(*bad)
	}
}

func (h *httpConn) alive() bool { return true }

func (h *httpConn) close() { h.c.end() }

var (
	_ hubConn = (*streamConn)(nil)
	_ hubConn = (*httpConn)(nil)
)

package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/yetone/magpie/internal/browserext"
)

// Magpie Browser: the agents magpie serves use the user's own browser,
// its tabs signed in to whatever the user is, through the Magpie Browser
// extension (internal/browserext). Not a browser of magpie's own: a fresh
// profile is signed in to nothing, and an agent's own browser tool stops
// working when its account changes.
//
// The extension holds a WebSocket to BrowserExtensionPath, taken only from
// its own origin on this computer. An agent calls the tools at
// BrowserMCPPath (Streamable HTTP, or `magpie mcp browser` over stdio),
// and each call is handed to the extension, which carries it out with the
// DevTools protocol in the tab and answers with MCP content. Only this
// computer's agents may: unlike the other servers of magpie's own, it is
// never served to another machine, shared or not, since it acts as the
// user wherever they are signed in.

// BrowserMCPPath is where the browser MCP server is served.
const BrowserMCPPath = "/mcp/magpie/browser"

// BrowserExtensionPath is where the extension connects.
const BrowserExtensionPath = "/v1/magpie/browser/extension"

// BrowserStatusPath tells whether the extension is connected.
const BrowserStatusPath = "/v1/magpie/browser"

// BrowserSessionHeader names the agent session a call is from, so each
// one goes on in the tab it was working in; `magpie mcp browser` sends one
// a process.
const BrowserSessionHeader = "X-Magpie-Browser-Session"

// browserCallWait is how long a call waits for the extension: a
// browser_wait waits up to 60s, a load 30s, and each acts then waits for the
// page to settle.
const browserCallWait = 100 * time.Second

// browserRelay is the extensions connected: each browser that has Magpie
// Browser (Chrome and Edge both, two profiles of one). A session goes on in
// the one it picked (browser_tabs use), else the last to connect.
type browserRelay struct {
	recs    browserRecs
	mu      sync.Mutex
	conns   []*browserConn
	chosen  map[string]*browserConn // session → the browser it picked
	pending map[string]chan json.RawMessage
	next    uint64
	ids     uint64
}

type browserConn struct {
	c       *websocket.Conn
	id      string // b1, b2: in the order they connected, while magpie runs
	version string
	agent   string
	since   time.Time
	wmu     sync.Mutex
}

// browserName is the browser a User-Agent names, with its major version:
// Edge, Opera, Vivaldi and Yandex say so; Brave, Arc and the rest of
// Chromium's say only Chrome.
func browserName(ua string) string {
	for _, b := range []struct{ token, name string }{{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Vivaldi/", "Vivaldi"}, {"YaBrowser/", "Yandex"}, {"Chrome/", "Chrome"}} {
		if i := strings.Index(ua, b.token); i >= 0 {
			v := ua[i+len(b.token):]
			if j := strings.IndexAny(v, ". "); j >= 0 {
				v = v[:j]
			}
			return strings.TrimSpace(b.name + " " + v)
		}
	}
	return "a Chromium browser"
}

func (c *browserConn) name() string { return browserName(c.agent) }

func (c *browserConn) send(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.c.Write(ctx, websocket.MessageText, b)
}

func (b *browserRelay) current() *browserConn { return b.forSession("") }

// forSession is the browser a session's calls go to: the one it picked
// while that one is connected, else the last to connect.
func (b *browserRelay) forSession(session string) *browserConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c := b.chosen[session]; c != nil && session != "" {
		return c
	}
	if len(b.conns) == 0 {
		return nil
	}
	return b.conns[len(b.conns)-1]
}

// all is every connected browser, the first to connect first
func (b *browserRelay) all() []*browserConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.conns)
}

// choose has a session's calls go to the connected browser named by its id
// (b2) or, failing that, the one whose name has it (edge), when only one
// does.
func (b *browserRelay) choose(session, which string) (*browserConn, error) {
	which = strings.ToLower(strings.TrimSpace(which))
	b.mu.Lock()
	defer b.mu.Unlock()
	var by []*browserConn
	for _, c := range b.conns {
		if c.id == which {
			by = []*browserConn{c}
			break
		}
		if which != "" && strings.Contains(strings.ToLower(c.name()), which) {
			by = append(by, c)
		}
	}
	switch {
	case len(b.conns) == 0:
		return nil, errBrowserAway
	case len(by) == 0:
		return nil, fmt.Errorf("no connected browser is %q: browser_tabs with action browsers lists them", which)
	case len(by) > 1:
		return nil, fmt.Errorf("%d connected browsers are %q: pick one by its id (browser_tabs with action browsers lists them)", len(by), which)
	}
	if b.chosen == nil {
		b.chosen = map[string]*browserConn{}
	}
	b.chosen[session] = by[0]
	return by[0], nil
}

func (b *browserRelay) add(c *browserConn) {
	b.mu.Lock()
	b.ids++
	c.id = "b" + strconv.FormatUint(b.ids, 10)
	b.conns = append(b.conns, c)
	b.mu.Unlock()
}

func (b *browserRelay) remove(c *browserConn) {
	b.mu.Lock()
	for i, x := range b.conns {
		if x == c {
			b.conns = append(b.conns[:i], b.conns[i+1:]...)
			break
		}
	}
	for k, v := range b.chosen {
		if v == c {
			delete(b.chosen, k)
		}
	}
	b.mu.Unlock()
}

func (b *browserRelay) answered(id string, result json.RawMessage) {
	b.mu.Lock()
	ch := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()
	if ch != nil {
		ch <- result
	}
}

// call hands a tool call to the extension and waits for its MCP result.
func (b *browserRelay) call(ctx context.Context, session, tool string, args json.RawMessage) (json.RawMessage, error) {
	c := b.forSession(session)
	if c == nil {
		return nil, errBrowserAway
	}
	b.mu.Lock()
	if b.pending == nil {
		b.pending = map[string]chan json.RawMessage{}
	}
	b.next++
	id := strconv.FormatUint(b.next, 10)
	ch := make(chan json.RawMessage, 1)
	b.pending[id] = ch
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, id)
		b.mu.Unlock()
	}()
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	if err := c.send(ctx, map[string]any{"id": id, "session": session, "tool": tool, "args": args}); err != nil {
		return nil, errors.New("couldn't reach Magpie Browser: " + err.Error())
	}
	t := time.NewTimer(browserCallWait)
	defer t.Stop()
	select {
	case r := <-ch:
		return r, nil
	case <-t.C:
		return nil, errors.New("Magpie Browser didn't answer in " + browserCallWait.String() + ": the page may be stuck; take a snapshot or a screenshot to see where it is")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

var errBrowserAway = errors.New("Magpie Browser isn't connected, so there is no browser to use. Ask the user to set it up: magpie's Library → Magpie Browser → Set up, which loads the extension in Chrome, Edge or another Chromium browser. Once installed it connects by itself whenever that browser and magpie are both running.")

// browserExtension takes the extension's WebSocket.
func (s *Server) browserExtension(w http.ResponseWriter, r *http.Request) {
	if !local(r) || !loopbackHost(r.Host) || r.Header.Get("Origin") != browserext.Origin {
		http.Error(w, "only the Magpie Browser extension, on this computer, connects here", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"chrome-extension://" + browserext.ID}})
	if err != nil {
		log.Printf("browser extension socket: %v", err)
		return
	}
	ws.SetReadLimit(64 << 20) // a full-page screenshot
	c := &browserConn{c: ws, since: time.Now()}
	ctx := r.Context()
	defer ws.CloseNow()
	added := false
	defer func() {
		if added {
			s.browser.remove(c)
			log.Printf("Magpie Browser disconnected")
		}
	}()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var m struct {
			Type    string          `json:"type"`
			ID      string          `json:"id"`
			Result  json.RawMessage `json:"result"`
			Version string          `json:"version"`
			Agent   string          `json:"agent"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch {
		case m.Type == "hello":
			c.version, c.agent = m.Version, m.Agent
			if !added {
				s.browser.add(c)
				added = true
				log.Printf("Magpie Browser %s connected", m.Version)
			}
			if browserext.Older(m.Version, browserext.Version()) {
				// an older extension: its folder gets this magpie's, which
				// the browser reads when it starts again (or the extension's
				// reload on chrome://extensions). The extension isn't told
				// to reload itself: an unpacked one that calls
				// chrome.runtime.reload() doesn't come back (Chrome for
				// Testing 1234, headless and not, loaded either way).
				browserext.Refresh(URL())
			}
		case m.Type == "ping":
			c.send(ctx, map[string]string{"type": "pong"})
		case m.ID != "":
			s.browser.answered(m.ID, m.Result)
		}
	}
}

// browserStatus says whether the extension is connected, for the GUI.
func (s *Server) browserStatus(w http.ResponseWriter, r *http.Request) {
	if !local(r) || fromBrowser(r) {
		http.Error(w, "Magpie Browser's state is told to this computer's magpie only", http.StatusForbidden)
		return
	}
	out := map[string]any{"connected": false, "version": browserext.Version()}
	if c := s.browser.current(); c != nil {
		out["connected"] = true
		out["extension"] = c.version
		out["browser"] = c.agent
		out["since"] = c.since
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) browserMCP(w http.ResponseWriter, r *http.Request) {
	// never another machine's, a gateway key or not: the tools act as the
	// user wherever their browser is signed in; and no web page's, whatever
	// Settings lists
	if !local(r) || fromBrowser(r) {
		http.Error(w, "magpie's browser MCP server answers agents on this computer only", http.StatusForbidden)
		return
	}
	serveMCP(w, r, "magpie's browser", "magpie-browser", 1<<20, s.browserMCPCall)
}

func (s *Server) browserMCPCall(r *http.Request, m mcpMessage) (any, map[string]any) {
	switch m.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": mcpVersion(m),
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "magpie-browser", "title": "Magpie Browser", "version": Version},
			"instructions":    browserInstructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": []any{browserOneTool()}}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, map[string]any{"code": -32602, "message": "invalid params: " + err.Error()}
		}
		one, shot := p.Name == "browser", false
		acted := map[string]any{} // the arguments, for a recording's frame
		if one {
			tool, args, wants, err := browserUnfold(p.Arguments)
			if err != nil {
				return toolResult(err.Error(), true), nil
			}
			json.Unmarshal(p.Arguments, &acted)
			if tool == browserRecordTool {
				return s.browserRecord(r, browserSession(r), acted["action"] == "record_stop"), nil
			}
			p.Name, p.Arguments, shot = tool, args, wants
		} else if !browserToolNamed(p.Name) {
			return nil, map[string]any{"code": -32602, "message": "unknown tool " + p.Name}
		}
		said := func(r map[string]any) map[string]any {
			if one {
				browserSays(r)
			}
			return r
		}
		session := browserSession(r)
		var tabs struct{ Action, Browser string }
		if p.Name == "browser_tabs" {
			json.Unmarshal(p.Arguments, &tabs)
			switch tabs.Action {
			case "browsers":
				return said(toolResult(s.browser.list(session), false)), nil
			case "use":
				c, err := s.browser.choose(session, tabs.Browser)
				if err != nil {
					return said(toolResult(err.Error(), true)), nil
				}
				return said(toolResult("Calls now go to "+c.id+" ("+c.name()+"). browser_tabs lists its tabs.", false)), nil
			}
		}
		out, err := s.browser.call(r.Context(), session, p.Name, p.Arguments)
		if err != nil {
			return said(toolResult(err.Error(), true)), nil
		}
		var result map[string]any
		if json.Unmarshal(out, &result) != nil || result["content"] == nil {
			return toolResult("Magpie Browser answered with something that isn't a tool result: update it (magpie's Library → Magpie Browser)", true), nil
		}
		// with two browsers connected, the list of tabs says whose they are
		if p.Name == "browser_tabs" && (tabs.Action == "" || tabs.Action == "list") {
			if all := s.browser.all(); len(all) > 1 {
				if c := s.browser.forSession(session); c != nil {
					if content, ok := result["content"].([]any); ok {
						result["content"] = append([]any{map[string]any{"type": "text", "text": fmt.Sprintf("These are %s's tabs (%s); %d browsers are connected: browser_tabs with action browsers lists them.", c.id, c.name(), len(all))}}, content...)
					}
				}
			}
		}
		rec := s.browser.recs.on(session) && result["isError"] != true && browserRecWorth(p.Name, tabs.Action)
		if (shot || rec) && result["isError"] != true {
			data, why := s.browserPicture(r, session)
			if shot {
				s.browserShot(result, data, why)
			}
			if rec && data != "" {
				name := strings.TrimPrefix(p.Name, "browser_")
				if a, _ := acted["action"].(string); a != "" {
					name = a
				}
				if f, err := browserRecFrame(data, browserRecSay(s.browser.recs.count(session)+1, name, acted)); err == nil {
					s.browser.recs.add(session, f)
				}
			}
		}
		return said(result), nil
	}
	return nil, map[string]any{"code": -32601, "message": "method not found: " + m.Method}
}

// browserPicture is a picture of session's window (base64), or why there
// is none.
func (s *Server) browserPicture(r *http.Request, session string) (data, why string) {
	out, err := s.browser.call(r.Context(), session, "browser_screenshot", nil)
	if err != nil {
		return "", err.Error()
	}
	var shot struct {
		Content []struct{ Type, Data string }
		IsError bool
	}
	if json.Unmarshal(out, &shot) == nil && !shot.IsError {
		for _, c := range shot.Content {
			if c.Type == "image" && c.Data != "" {
				return c.Data, ""
			}
		}
	}
	return "", "it didn't answer with a picture"
}

// browserShot adds a small picture of the window, data, to an action's
// result. One that couldn't be taken says why; the action still happened.
func (s *Server) browserShot(result map[string]any, data, why string) {
	content, _ := result["content"].([]any)
	if data != "" {
		if small, ok := browserSmall(data); ok {
			result["content"] = append(content, map[string]any{"type": "image", "data": small, "mimeType": "image/jpeg"})
			return
		}
		why = "it didn't answer with a picture"
	}
	result["content"] = append(content, map[string]any{"type": "text", "text": "(no screenshot: " + why + ")"})
}

// list is the connected browsers, the one session's calls go to marked *.
func (b *browserRelay) list(session string) string {
	all := b.all()
	if len(all) == 0 {
		return errBrowserAway.Error()
	}
	mine := b.forSession(session)
	lines := []string{fmt.Sprintf("%d browsers with Magpie Browser are connected (* is the one calls go to; browser_tabs with action use and browser picks another, by id or name):", len(all))}
	for _, c := range all {
		mark := " "
		if c == mine {
			mark = "*"
		}
		lines = append(lines, fmt.Sprintf("%s %s %s, extension %s, connected %s", mark, c.id, c.name(), c.version, c.since.Format("15:04")))
	}
	return strings.Join(lines, "\n")
}

// browserSession is which agent session a call is from: the header the
// stdio server sends, else the MCP session, else the agent.
func browserSession(r *http.Request) string {
	for _, h := range []string{BrowserSessionHeader, "Mcp-Session-Id"} {
		if v := boundedSession(r.Header.Get(h)); v != "" {
			return v
		}
	}
	if a := callerOf(r).agent; a != "" {
		return "agent:" + a
	}
	return "default"
}
